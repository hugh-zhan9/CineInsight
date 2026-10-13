package services

import (
	"fmt"
	"strings"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 批量编辑在一个事务里的写入：标量列用条件更新，关联用 INSERT … ON CONFLICT DO NOTHING 或按行删除。
// 不调用会自开事务的服务方法（SQLite immediate 事务嵌套会死锁），各服务的事务内小函数可以复用。

func writeBatchChanges(tx *gorm.DB, plan *batchPlan, state *batchMediaState, changes []batchItemChange) error {
	for _, field := range plan.fields {
		if field.scalar >= 0 {
			ids := []uint{}
			for _, change := range changes {
				if change.field == field.field {
					ids = append(ids, change.mediaID)
				}
			}
			if len(ids) == 0 {
				continue
			}
			if err := writeBatchScalar(tx, plan.kind, field.field, plan.ops[field.scalar].Value, ids); err != nil {
				return err
			}
			continue
		}
		added, removed := map[uint][]uint{}, map[uint][]uint{}
		for _, change := range changes {
			if change.field != field.field {
				continue
			}
			for _, relID := range change.added {
				added[relID] = append(added[relID], change.mediaID)
			}
			for _, relID := range change.removed {
				removed[relID] = append(removed[relID], change.mediaID)
			}
		}
		if field.add >= 0 {
			for _, relID := range plan.ops[field.add].IDs {
				if err := addBatchRelation(tx, plan.kind, field.field, relID, added[relID], state.ids); err != nil {
					return err
				}
			}
		}
		if field.remove >= 0 {
			for _, relID := range plan.ops[field.remove].IDs {
				if len(removed[relID]) == 0 {
					continue
				}
				if err := deleteBatchRelations(tx, plan.kind, field.field, relID, removed[relID]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// writeBatchScalar 只改值不同的行（条件更新）；经 Model 更新会顺带刷新 updated_at，与单条改评分一致。
func writeBatchScalar(tx *gorm.DB, kind, field string, value *float64, ids []uint) error {
	column := batchScalarColumn(field)
	query := tx.Model(batchMediaModel(kind)).Where("id IN ?", ids)
	var result *gorm.DB
	if value == nil {
		result = query.Where(column+" IS NOT NULL").Update(column, nil)
	} else {
		// 年份按整数传参：Postgres 按列类型推断参数类型，浮点数给整数列会编码失败。
		arg := batchScalarArg(field, *value)
		result = query.Where("("+column+" IS NULL OR "+column+" <> ?)", arg).Update(column, arg)
	}
	notifyBatchYearChanged(result, kind, field)
	return result.Error
}

// notifyBatchYearChanged：批量改了视频年份，想看自动匹配的输入变了（D-P2-03），防抖后重跑一轮。
func notifyBatchYearChanged(result *gorm.DB, kind, field string) {
	if result.Error == nil && result.RowsAffected > 0 && kind == BatchEditKindVideo && field == BatchEditFieldYear {
		notifyLibraryMatchInputsChanged("batch-edit-year")
	}
}

// addBatchRelation 加上一个对象。保持各自入口的既有语义：
//   - 标签：作废同条目同标签的待审 AI 候选（同 AddTagToVideo / AddTagToImage）；
//   - 人物：这批目标上人脸链路的写入记录释放，关系归用户所有（同 AddPersonVideos / AddPersonImages）；
//   - 作品集：接在现有末位之后，按 ID 升序（同 AddCollectionVideos）。
func addBatchRelation(tx *gorm.DB, kind, field string, relID uint, mediaIDs, targetIDs []uint) error {
	if len(mediaIDs) > 0 {
		if err := insertBatchRelations(tx, kind, field, relID, mediaIDs); err != nil {
			return err
		}
	}
	switch field {
	case BatchEditFieldTag:
		for _, mediaID := range mediaIDs {
			var err error
			if kind == BatchEditKindImage {
				_, err = SupersedeImageCandidatesForManualTag(tx, mediaID, relID)
			} else {
				_, err = SupersedeCandidatesForManualTag(tx, mediaID, relID)
			}
			if err != nil {
				return fmt.Errorf("作废对应的 AI 候选失败: %w", err)
			}
		}
		// 用户明确加了这个标签：本批目标上由规则插入的同一关系交还用户（D-RA-04，含本来就有、这次没改变的条目），
		// 之后 AI 自动打标把它当人工标签。
		if err := claimRuleInsertedTagsTx(tx, kind, targetIDs, relID); err != nil {
			return fmt.Errorf("交还规则标签失败: %w", err)
		}
	case BatchEditFieldPerson:
		return releaseFaceRelationWrites(tx, relID, batchFaceMediaKind(kind), targetIDs)
	}
	return nil
}

func batchFaceMediaKind(kind string) string {
	if kind == BatchEditKindImage {
		return models.FaceMediaKindImage
	}
	return models.FaceMediaKindVideo
}

// insertBatchRelations 给这些条目加上一个对象的关系；已有的跳过（ON CONFLICT DO NOTHING）。
func insertBatchRelations(tx *gorm.DB, kind, field string, relID uint, mediaIDs []uint) error {
	switch {
	case field == BatchEditFieldCollection:
		return appendBatchCollectionVideos(tx, relID, mediaIDs)
	case field == BatchEditFieldPerson && kind == BatchEditKindImage:
		rows := make([]models.ImagePerson, 0, len(mediaIDs))
		for _, mediaID := range mediaIDs {
			rows = append(rows, models.ImagePerson{ImageID: mediaID, PersonID: relID})
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	case field == BatchEditFieldPerson:
		rows := make([]models.VideoPerson, 0, len(mediaIDs))
		for _, mediaID := range mediaIDs {
			rows = append(rows, models.VideoPerson{VideoID: mediaID, PersonID: relID})
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	}
	table := batchRelationTableFor(kind, field)
	placeholders := make([]string, 0, len(mediaIDs))
	args := make([]any, 0, len(mediaIDs)*2)
	for _, mediaID := range mediaIDs {
		placeholders = append(placeholders, "(?, ?)")
		args = append(args, mediaID, relID)
	}
	return tx.Exec("INSERT INTO "+table.name+"("+table.mediaColumn+", "+table.relColumn+") VALUES "+
		strings.Join(placeholders, ", ")+" ON CONFLICT DO NOTHING", args...).Error
}

// appendBatchCollectionVideos 把视频按给定顺序接在作品集现有末位之后（同 AddCollectionVideos 的位置规则）。
func appendBatchCollectionVideos(tx *gorm.DB, collectionID uint, videoIDs []uint) error {
	if err := lockActiveCollection(tx, collectionID); err != nil {
		return err
	}
	var maxPosition int
	if err := tx.Model(&models.CollectionVideo{}).Where("collection_id = ?", collectionID).
		Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
		return err
	}
	rows := make([]models.CollectionVideo, 0, len(videoIDs))
	for _, videoID := range videoIDs {
		maxPosition++
		rows = append(rows, models.CollectionVideo{CollectionID: collectionID, VideoID: videoID, Position: maxPosition})
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// deleteBatchRelations 去掉这些条目与一个对象的关系。去人物关联不删除人物本身（D-MC-04，不同于详情编辑）；
// 移出作品集后压紧位置（同 RemoveCollectionVideo）。
func deleteBatchRelations(tx *gorm.DB, kind, field string, relID uint, mediaIDs []uint) error {
	table := batchRelationTableFor(kind, field)
	if field == BatchEditFieldCollection {
		if err := lockActiveCollection(tx, relID); err != nil {
			return err
		}
	}
	if err := tx.Exec("DELETE FROM "+table.name+" WHERE "+table.relColumn+" = ? AND "+table.mediaColumn+" IN ?", relID, mediaIDs).Error; err != nil {
		return err
	}
	if field == BatchEditFieldCollection {
		return compactCollectionPositions(tx, relID)
	}
	return nil
}
