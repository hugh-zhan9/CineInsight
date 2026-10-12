package services

import (
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

// 目标失效与改写（需求设计文档 §3.3「目标失效」）以及 AI 自动打标的排除口径（§3.4）。
// 这里的函数都只接调用方的事务（标签删除 / 合并、作品集删除），不自开事务。

// dropAutomationRuleTargetTx：标签或作品集被删除时，删掉指向它的账本行，从规则动作里去掉它；
// 规则没剩下动作就停用并注明 target_deleted（D-RA-05）。action 取账本口径 tag / collection。
func dropAutomationRuleTargetTx(tx *gorm.DB, action string, targetID uint) error {
	if err := tx.Where("action = ? AND target_id = ?", action, targetID).Delete(&models.AutomationRuleWrite{}).Error; err != nil {
		return err
	}
	return rewriteAutomationRuleActionsTx(tx, action, func(id uint) (uint, bool) {
		if id == targetID {
			return 0, false
		}
		return id, true
	})
}

// rewriteAutomationRuleTagTargetsTx：标签合并时把规则动作与账本里的来源标签改成目标标签。
// 改写后与账本唯一键冲突的（同一规则、同一媒体已有目标标签的行，或几个来源各有一行）只保留一行：
// 目标标签原有的那行优先，否则留 id 最小的来源行。
//
// 必须在合并改写 video_tags / image_tags 之前调用：要按合并前的关系判断合并后的标签归谁。
func rewriteAutomationRuleTagTargetsTx(tx *gorm.DB, sourceIDs []uint, targetID uint) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	if err := releaseMergedRuleTagsTx(tx, append([]uint{targetID}, sourceIDs...)); err != nil {
		return err
	}
	sources := make(map[uint]bool, len(sourceIDs))
	for _, id := range sourceIDs {
		sources[id] = true
	}
	if err := rewriteAutomationRuleActionsTx(tx, automationWriteTag, func(id uint) (uint, bool) {
		if sources[id] {
			return targetID, true
		}
		return id, true
	}); err != nil {
		return err
	}
	// 同一（规则，媒体）合并后只留一行：先把这一组的 inserted 取「或」写到每一行上，留哪一行都一样。
	// 归属释放（releaseMergedRuleTagsTx）已经跑过，这时还为真的组说明这条媒体上合并的标签全是规则插入的。
	if err := orMergedRuleWriteInsertedTx(tx, append([]uint{targetID}, sourceIDs...)); err != nil {
		return err
	}
	const sameCombo = `keep.rule_id = automation_rule_writes.rule_id AND keep.media_kind = automation_rule_writes.media_kind
		AND keep.media_id = automation_rule_writes.media_id AND keep.action = automation_rule_writes.action`
	if err := tx.Exec(`DELETE FROM automation_rule_writes WHERE action = ? AND target_id IN ?
		AND EXISTS (SELECT 1 FROM automation_rule_writes keep WHERE `+sameCombo+` AND keep.target_id = ?)`,
		automationWriteTag, sourceIDs, targetID).Error; err != nil {
		return err
	}
	if err := tx.Exec(`DELETE FROM automation_rule_writes WHERE action = ? AND target_id IN ?
		AND EXISTS (SELECT 1 FROM automation_rule_writes keep WHERE `+sameCombo+` AND keep.target_id IN ? AND keep.id < automation_rule_writes.id)`,
		automationWriteTag, sourceIDs, sourceIDs).Error; err != nil {
		return err
	}
	return tx.Model(&models.AutomationRuleWrite{}).Where("action = ? AND target_id IN ?", automationWriteTag, sourceIDs).
		Update("target_id", targetID).Error
}

// orMergedRuleWriteInsertedTx：在世规则在同一媒体上、指向这些（将合并的）标签的账本行里只要有一行 inserted=true，
// 就把这一组都标成 true。先查出行 id 再分批改，不在 UPDATE 里自引用账本表。无主行不去重，不参与。
func orMergedRuleWriteInsertedTx(tx *gorm.DB, tagIDs []uint) error {
	var ids []uint
	if err := tx.Table("automation_rule_writes AS arw").
		Where("arw.action = ? AND arw.target_id IN ? AND arw.inserted = ? AND arw.rule_id IS NOT NULL", automationWriteTag, tagIDs, false).
		Where(`EXISTS (SELECT 1 FROM automation_rule_writes peer WHERE peer.rule_id = arw.rule_id AND peer.media_kind = arw.media_kind
			AND peer.media_id = arw.media_id AND peer.action = arw.action AND peer.target_id IN ? AND peer.inserted = ?)`, tagIDs, true).
		Pluck("arw.id", &ids).Error; err != nil {
		return err
	}
	for start := 0; start < len(ids); start += automationBatchSize {
		chunk := ids[start:min(start+automationBatchSize, len(ids))]
		if err := tx.Model(&models.AutomationRuleWrite{}).Where("id IN ?", chunk).Update("inserted", true).Error; err != nil {
			return err
		}
	}
	return nil
}

// rewriteAutomationRuleActionsTx 逐条改写引用某类目标的规则动作：mapID 返回新 id 与是否保留；改写后重复的动作去重。
// 动作被删光的规则停用并注明 target_deleted。解析不了的规则不动（由求值时停用）。
func rewriteAutomationRuleActionsTx(tx *gorm.DB, action string, mapID func(uint) (uint, bool)) error {
	var rules []models.AutomationRule
	if err := tx.Find(&rules).Error; err != nil {
		return err
	}
	for _, rule := range rules {
		_, actions, err := decodeAutomationRule(rule)
		if err != nil {
			continue
		}
		kept := make([]AutomationRuleAction, 0, len(actions))
		seen := map[automationLedgerKey]bool{}
		changed := false
		for _, item := range actions {
			if item.writeKind() == action {
				id, keep := mapID(item.targetID())
				if !keep {
					changed = true
					continue
				}
				if id != item.targetID() {
					changed = true
					if item.Type == automationActionAddToCollection {
						item.CollectionID = id
					} else {
						item.TagID = id
					}
				}
			}
			key := automationLedgerKey{Action: item.writeKind(), TargetID: item.targetID()}
			if seen[key] {
				changed = true
				continue
			}
			seen[key] = true
			kept = append(kept, item)
		}
		if !changed {
			continue
		}
		if err := saveAutomationRuleActionsTx(tx, rule, kept); err != nil {
			return err
		}
	}
	return nil
}

func saveAutomationRuleActionsTx(tx *gorm.DB, rule models.AutomationRule, actions []AutomationRuleAction) error {
	payload, err := encodeAutomationJSON(actions)
	if err != nil {
		return err
	}
	updates := map[string]any{"actions_json": payload, "updated_at": time.Now()}
	if len(actions) == 0 {
		updates["enabled"] = false
		updates["disabled_reason"] = automationDisabledTargetDeleted
	}
	return tx.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).Updates(updates).Error
}

// automationRuleInsertedTagExists 是「这条（媒体，标签）关系是规则真正插入的」的 SQL 条件（D-RA-04）：
// AI 自动打标判断「已有人工标签」时要把它排除。mediaColumn / tagColumn 是外层查询里的列名，返回条件与参数。
func automationRuleInsertedTagExists(kind, mediaColumn, tagColumn string) (string, []any) {
	return `EXISTS (
			SELECT 1 FROM automation_rule_writes
			WHERE automation_rule_writes.media_kind = ?
				AND automation_rule_writes.media_id = ` + mediaColumn + `
				AND automation_rule_writes.action = ?
				AND automation_rule_writes.target_id = ` + tagColumn + `
				AND automation_rule_writes.inserted = ?
		)`, []any{kind, automationWriteTag, true}
}

// automationRuleInsertedTagIDs 返回规则真正插入到这条媒体上的标签 id（D-RA-04）。
func automationRuleInsertedTagIDs(db *gorm.DB, kind string, mediaID uint) (map[uint]bool, error) {
	var ids []uint
	if err := db.Model(&models.AutomationRuleWrite{}).
		Where("media_kind = ? AND media_id = ? AND action = ? AND inserted = ?", kind, mediaID, automationWriteTag, true).
		Pluck("target_id", &ids).Error; err != nil {
		return nil, err
	}
	excluded := make(map[uint]bool, len(ids))
	for _, id := range ids {
		excluded[id] = true
	}
	return excluded, nil
}

// claimRuleInsertedTagsTx 是「用户手动动了这个标签」：账本里这些媒体上由规则真正插入的同一标签改成 inserted=false。
// 手动加上：之后 AI 自动打标把它当人工标签（D-RA-04）；手动去掉：账本行留着挡住规则再加回，但不再记作规则插入，
// 以后经别的路径加回时不会被当成规则标签。只接调用方的事务；手动加/去标签（单条、批量、批量编辑）都在
// 改关系的同一事务里调用。媒体 id 按 500 一批，避开参数个数上限。
func claimRuleInsertedTagsTx(tx *gorm.DB, mediaKind string, mediaIDs []uint, tagID uint) error {
	mediaIDs = uniqueUintIDs(mediaIDs)
	for start := 0; start < len(mediaIDs); start += automationBatchSize {
		chunk := mediaIDs[start:min(start+automationBatchSize, len(mediaIDs))]
		if err := tx.Model(&models.AutomationRuleWrite{}).
			Where("media_kind = ? AND action = ? AND target_id = ? AND inserted = ? AND media_id IN ?", mediaKind, automationWriteTag, tagID, true, chunk).
			Update("inserted", false).Error; err != nil {
			return err
		}
	}
	return nil
}

// releaseMergedRuleTagsTx 在标签合并改写关系之前调用：合并后来源与目标在一条媒体上并成一个标签，
// 只要这条媒体合并前挂着的来源或目标标签里有一个不是规则插入的（人工的），合并后的标签就归用户，
// 这条媒体上这些标签的规则账本行一律改成 inserted=false（D-RA-04）。先查出行 id 再分批改，不在
// UPDATE 里自引用账本表。
func releaseMergedRuleTagsTx(tx *gorm.DB, tagIDs []uint) error {
	for _, side := range []struct{ kind, table, column string }{
		{AutomationMediaVideo, "video_tags", "video_id"},
		{AutomationMediaImage, "image_tags", "image_id"},
	} {
		ruleInserted, ruleArgs := automationRuleInsertedTagExists(side.kind, "rel."+side.column, "rel.tag_id")
		args := append([]any{tagIDs}, ruleArgs...)
		var ids []uint
		if err := tx.Table("automation_rule_writes AS arw").
			Where("arw.media_kind = ? AND arw.action = ? AND arw.inserted = ? AND arw.target_id IN ?", side.kind, automationWriteTag, true, tagIDs).
			Where(`EXISTS (SELECT 1 FROM `+side.table+` rel WHERE rel.`+side.column+` = arw.media_id AND rel.tag_id IN ? AND NOT `+ruleInserted+`)`, args...).
			Pluck("arw.id", &ids).Error; err != nil {
			return err
		}
		for start := 0; start < len(ids); start += automationBatchSize {
			chunk := ids[start:min(start+automationBatchSize, len(ids))]
			if err := tx.Model(&models.AutomationRuleWrite{}).Where("id IN ?", chunk).Update("inserted", false).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
