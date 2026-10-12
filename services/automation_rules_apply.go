package services

import (
	"context"
	"log"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 账本与写入（D-RA-03、需求设计文档 §3.3）：每个 (规则, 媒体, 动作, 目标) 至多处理一次。
// 关系本来就存在时也记一行（inserted=false），用户以后去掉这个标签，规则也不会加回来。

type automationLedgerKey struct {
	RuleID   uint
	MediaID  uint
	Action   string
	TargetID uint
}

// automationPendingWrite 是一次待执行的动作。
type automationPendingWrite struct {
	RuleID  uint
	MediaID uint
	Action  AutomationRuleAction
}

func (w automationPendingWrite) key() automationLedgerKey {
	return automationLedgerKey{RuleID: w.RuleID, MediaID: w.MediaID, Action: w.Action.writeKind(), TargetID: w.Action.targetID()}
}

// loadAutomationLedger 读出这些规则对这批媒体（≤500）已处理过的组合。
func loadAutomationLedger(ctx context.Context, kind string, ruleIDs, mediaIDs []uint) (map[automationLedgerKey]bool, error) {
	ledger := map[automationLedgerKey]bool{}
	if len(ruleIDs) == 0 || len(mediaIDs) == 0 {
		return ledger, nil
	}
	var rows []models.AutomationRuleWrite
	if err := database.DB.WithContext(ctx).Select("rule_id", "media_id", "action", "target_id").
		Where("media_kind = ? AND rule_id IN ? AND media_id IN ?", kind, ruleIDs, mediaIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.RuleID == nil {
			continue // 查询只取在世规则的行；无主行不参与「处理过」判定。
		}
		ledger[automationLedgerKey{RuleID: *row.RuleID, MediaID: row.MediaID, Action: row.Action, TargetID: row.TargetID}] = true
	}
	return ledger, nil
}

// automationPendingFor 求值一批媒体：条件满足、且账本里没有的动作才待执行。matched 是有待执行动作的媒体 id（按出现顺序）。
func automationPendingFor(rules []*compiledAutomationRule, facts []automationMediaFacts, ledger map[automationLedgerKey]bool) (writes []automationPendingWrite, matched []uint) {
	for _, item := range facts {
		found := false
		for _, rule := range rules {
			if !rule.matches(item) {
				continue
			}
			for _, action := range rule.Actions {
				write := automationPendingWrite{RuleID: rule.ID, MediaID: item.ID, Action: action}
				if ledger[write.key()] {
					continue
				}
				writes = append(writes, write)
				found = true
			}
		}
		if found {
			matched = append(matched, item.ID)
		}
	}
	return writes, matched
}

// applyAutomationWrites 执行待写动作，每批最多 500 条、一批一个短事务（不调用会自开事务的服务方法）。
// 返回真正插入的关系数。批与批之间检查取消；已提交的批次保留。
func applyAutomationWrites(ctx context.Context, kind string, writes []automationPendingWrite) (int, error) {
	added := 0
	for start := 0; start < len(writes); start += automationBatchSize {
		if err := ctx.Err(); err != nil {
			return added, err
		}
		end := min(start+automationBatchSize, len(writes))
		batch := writes[start:end]
		batchAdded := 0
		err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
			batchAdded = 0
			alive, err := automationLiveTargetsTx(tx, kind, batch)
			if err != nil {
				return err
			}
			now := time.Now()
			for _, write := range batch {
				if !alive.ok(write) {
					continue
				}
				inserted, handled, err := applyAutomationWriteTx(tx, kind, write, now)
				if err != nil {
					return err
				}
				if handled && inserted {
					batchAdded++
				}
			}
			return nil
		})
		if err != nil {
			return added, err
		}
		added += batchAdded
	}
	return added, nil
}

// automationLiveTargets 是事务内复核的结果：媒体仍未删除、规则仍在、标签仍可用（未删除、非自动）、作品集仍在。
type automationLiveTargets struct {
	media, rules, tags, collections map[uint]bool
}

func (a automationLiveTargets) ok(write automationPendingWrite) bool {
	if !a.media[write.MediaID] || !a.rules[write.RuleID] {
		return false
	}
	if write.Action.Type == automationActionAddToCollection {
		return a.collections[write.Action.CollectionID]
	}
	return a.tags[write.Action.TagID]
}

func automationLiveTargetsTx(tx *gorm.DB, kind string, batch []automationPendingWrite) (automationLiveTargets, error) {
	mediaIDs, ruleIDs, tagIDs, collectionIDs := []uint{}, []uint{}, []uint{}, []uint{}
	for _, write := range batch {
		mediaIDs, ruleIDs = append(mediaIDs, write.MediaID), append(ruleIDs, write.RuleID)
		if write.Action.Type == automationActionAddToCollection {
			collectionIDs = append(collectionIDs, write.Action.CollectionID)
		} else {
			tagIDs = append(tagIDs, write.Action.TagID)
		}
	}
	live := automationLiveTargets{}
	var mediaModel any = &models.Video{}
	if kind == AutomationMediaImage {
		mediaModel = &models.Image{}
	}
	var err error
	if live.media, err = automationExistingIDs(tx.Model(mediaModel), mediaIDs); err != nil {
		return live, err
	}
	if live.rules, err = automationExistingIDs(tx.Model(&models.AutomationRule{}), ruleIDs); err != nil {
		return live, err
	}
	if live.tags, err = automationExistingIDs(tx.Model(&models.Tag{}).Where("COALESCE(automatic_kind, '') = ''"), tagIDs); err != nil {
		return live, err
	}
	live.collections, err = automationExistingIDs(tx.Model(&models.MediaCollection{}), collectionIDs)
	return live, err
}

func automationExistingIDs(query *gorm.DB, ids []uint) (map[uint]bool, error) {
	found := map[uint]bool{}
	ids = uniqueUintIDs(ids)
	if len(ids) == 0 {
		return found, nil
	}
	var existing []uint
	if err := query.Where("id IN ?", ids).Pluck("id", &existing).Error; err != nil {
		return nil, err
	}
	for _, id := range existing {
		found[id] = true
	}
	return found, nil
}

// applyAutomationWriteTx 先占账本（ON CONFLICT DO NOTHING：别的路径已处理过就跳过），再写关系，
// 真插入了才把账本标成 inserted 并作废对应的待审 AI 候选（与手动加标签同口径，D-PC28 规则 2）。
func applyAutomationWriteTx(tx *gorm.DB, kind string, write automationPendingWrite, now time.Time) (inserted bool, handled bool, err error) {
	key := write.key()
	claim := tx.Exec(`INSERT INTO automation_rule_writes(rule_id, media_kind, media_id, action, target_id, inserted, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, key.RuleID, kind, key.MediaID, key.Action, key.TargetID, false, now)
	if claim.Error != nil {
		return false, false, claim.Error
	}
	if claim.RowsAffected == 0 {
		return false, false, nil
	}
	if inserted, err = insertAutomationRelationTx(tx, kind, write, now); err != nil || !inserted {
		return false, true, err
	}
	if err := tx.Model(&models.AutomationRuleWrite{}).
		Where("rule_id = ? AND media_kind = ? AND media_id = ? AND action = ? AND target_id = ?", key.RuleID, kind, key.MediaID, key.Action, key.TargetID).
		Update("inserted", true).Error; err != nil {
		return false, true, err
	}
	if write.Action.Type == automationActionAddTag {
		var superseded []uint
		if kind == AutomationMediaImage {
			superseded, err = SupersedeImageCandidatesForManualTag(tx, write.MediaID, write.Action.TagID)
		} else {
			superseded, err = SupersedeCandidatesForManualTag(tx, write.MediaID, write.Action.TagID)
		}
		if err != nil {
			return false, true, err
		}
		if len(superseded) > 0 {
			log.Printf("规则加标签作废 AI 候选 rule=%d %s=%d tag_id=%d candidates=%d", write.RuleID, kind, write.MediaID, write.Action.TagID, len(superseded))
		}
	}
	return true, true, nil
}

// insertAutomationRelationTx 写一条关系，已存在则不动；返回是否真的插入了。
// 加入作品集与 AddCollectionVideos 同口径：先锁作品集行，排在现有最大位置之后。
func insertAutomationRelationTx(tx *gorm.DB, kind string, write automationPendingWrite, now time.Time) (bool, error) {
	var result *gorm.DB
	switch {
	case write.Action.Type == automationActionAddToCollection:
		if err := lockActiveCollection(tx, write.Action.CollectionID); err != nil {
			return false, err
		}
		var maxPosition int
		if err := tx.Model(&models.CollectionVideo{}).Where("collection_id = ?", write.Action.CollectionID).
			Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
			return false, err
		}
		result = tx.Exec(`INSERT INTO collection_videos(collection_id, video_id, position, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, write.Action.CollectionID, write.MediaID, maxPosition+1, now, now)
	case kind == AutomationMediaImage:
		result = tx.Exec("INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING", write.MediaID, write.Action.TagID)
	default:
		result = tx.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING", write.MediaID, write.Action.TagID)
	}
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}
