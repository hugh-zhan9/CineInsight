package services

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// ListMetadataBatchEdits 是「最近的批量编辑」：最新的在前，至多 20 条。
func (s *MetadataBatchEditService) ListMetadataBatchEdits() ([]MetadataBatchEditListItem, error) {
	if database.DB == nil {
		return nil, gorm.ErrInvalidDB
	}
	var rows []models.MetadataBatchEdit
	if err := database.DB.Order("id DESC").Limit(metadataBatchKeep).Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]MetadataBatchEditListItem, 0, len(rows))
	for _, row := range rows {
		var summary MetadataBatchEditSummary
		if err := json.Unmarshal([]byte(row.SummaryJSON), &summary); err != nil {
			log.Printf("批量编辑摘要无法解析 batch_id=%d err=%v", row.ID, err)
		}
		if summary.Ops == nil {
			summary.Ops = []MetadataBatchOpCount{}
		}
		items = append(items, MetadataBatchEditListItem{
			ID: row.ID, Kind: row.Kind, CreatedAt: row.CreatedAt, UndoneAt: row.UndoneAt, ItemCount: row.ItemCount,
			Summary: summary, Undoable: row.UndoneAt == nil && row.ItemCount > 0,
		})
	}
	return items, nil
}

// UndoMetadataBatchEdit 撤销一次批量编辑（D-MC-05）：逐行比较当前值与 after_json，一致才恢复 before_json，
// 否则计入 changed_since；关联类只撤销本批次加上或去掉的那几个 ID。每 500 行一个事务，批与批之间检查取消；
// 恢复了的明细行在同一事务里删掉（批次的 item_count 与摘要不变），被取消后再撤销只处理剩下的行，
// 不会把已恢复的算成 changed_since；跑完（没被取消）才写 undone_at。
func (s *MetadataBatchEditService) UndoMetadataBatchEdit(parent context.Context, batchID uint) (result *MetadataBatchUndoResult, err error) {
	if database.DB == nil {
		return nil, gorm.ErrInvalidDB
	}
	// 记录已不在、已经撤销过属于请求本身的问题，在占运行位之前回绝，不记成任务中心的失败。
	// 两个撤销同时到达时后到的一个拿不到运行位（batch_edit_busy），所以这里不必在运行位里再查一遍。
	var batch models.MetadataBatchEdit
	if err := database.DB.First(&batch, batchID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMetadataBatchMissing
		}
		return nil, err
	}
	if batch.UndoneAt != nil {
		return nil, ErrBatchAlreadyUndone
	}
	// 进度总数是还剩的明细行：取消后再撤销时，已恢复的行已经删掉了。
	var remaining int64
	if err := database.DB.Model(&models.MetadataBatchEditItem{}).Where("batch_id = ?", batchID).Count(&remaining).Error; err != nil {
		return nil, err
	}
	ctx, end, err := s.begin(parent, int(remaining))
	if err != nil {
		return nil, err
	}
	defer func() { end(err) }()
	result = &MetadataBatchUndoResult{BatchID: batchID}
	lastID, done := uint(0), 0
	for {
		if ctx.Err() != nil {
			result.Cancelled = true
			break
		}
		var items []models.MetadataBatchEditItem
		if err := database.DB.Where("batch_id = ? AND id > ?", batchID, lastID).Order("id ASC").Limit(metadataBatchChunk).Find(&items).Error; err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		restored := 0
		if err := database.Transaction(func(tx *gorm.DB) error {
			restored = 0
			var err error
			restored, err = undoBatchItems(tx, batch.Kind, items)
			return err
		}); err != nil {
			log.Printf("批量编辑撤销失败 batch_id=%d done=%d err=%v", batchID, done, err)
			return nil, err
		}
		result.Restored += restored
		result.ChangedSince += len(items) - restored
		lastID = items[len(items)-1].ID
		done += len(items)
		s.progress(done)
	}
	if !result.Cancelled {
		if err := database.DB.Model(&models.MetadataBatchEdit{}).Where("id = ? AND undone_at IS NULL", batchID).
			Update("undone_at", time.Now()).Error; err != nil {
			return nil, err
		}
	}
	log.Printf("批量编辑撤销 batch_id=%d restored=%d changed_since=%d cancelled=%v", batchID, result.Restored, result.ChangedSince, result.Cancelled)
	return result, nil
}

// undoBatchItems 在一个事务里撤销一批明细，返回恢复了的行数。恢复了的明细行随即删掉；
// 移出过视频的作品集最后压紧一次位置。
func undoBatchItems(tx *gorm.DB, kind string, items []models.MetadataBatchEditItem) (int, error) {
	restored := []uint{}
	compact := map[uint]struct{}{}
	for _, item := range items {
		var ok bool
		var err error
		if batchFieldIsScalar(item.Field) {
			ok, err = undoBatchScalar(tx, kind, item)
		} else {
			ok, err = undoBatchRelation(tx, kind, item, compact)
		}
		if err != nil {
			return 0, err
		}
		if ok {
			restored = append(restored, item.ID)
		}
	}
	for collectionID := range compact {
		if err := compactCollectionPositions(tx, collectionID); err != nil {
			return 0, err
		}
	}
	if len(restored) > 0 {
		if err := tx.Where("id IN ?", restored).Delete(&models.MetadataBatchEditItem{}).Error; err != nil {
			return 0, err
		}
	}
	return len(restored), nil
}

// undoBatchScalar 只在当前值仍等于 after 时改回 before（条件更新，影响 1 行才算恢复）。
// 已在回收站里的条目照样恢复；已被永久删除的不会命中，计入 changed_since。
func undoBatchScalar(tx *gorm.DB, kind string, item models.MetadataBatchEditItem) (bool, error) {
	var before, after *float64
	if err := json.Unmarshal([]byte(item.BeforeJSON), &before); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(item.AfterJSON), &after); err != nil {
		return false, err
	}
	column := batchScalarColumn(item.Field)
	query := tx.Unscoped().Model(batchMediaModel(kind)).Where("id = ?", item.MediaID)
	if after == nil {
		query = query.Where(column + " IS NULL")
	} else {
		query = query.Where(column+" = ?", batchScalarArg(item.Field, *after))
	}
	var value any
	if before != nil {
		value = batchScalarArg(item.Field, *before)
	}
	result := query.Update(column, value)
	return result.RowsAffected == 1, result.Error
}

func batchScalarArg(field string, value float64) any {
	if field == BatchEditFieldYear {
		return int(value)
	}
	return value
}

// undoBatchRelation 只动本批次加上（after）与去掉（before）的那几个对象：它们仍是改后的样子
// （加上的都还在、去掉的都还不在）才恢复，否则整行计入 changed_since。
// 条目已被永久删除、或要加回的对象已不在时也算 changed_since。
func undoBatchRelation(tx *gorm.DB, kind string, item models.MetadataBatchEditItem, compact map[uint]struct{}) (bool, error) {
	var removed, added []uint
	if err := json.Unmarshal([]byte(item.BeforeJSON), &removed); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(item.AfterJSON), &added); err != nil {
		return false, err
	}
	var mediaCount int64
	if err := tx.Unscoped().Model(batchMediaModel(kind)).Where("id = ?", item.MediaID).Count(&mediaCount).Error; err != nil {
		return false, err
	}
	if mediaCount == 0 {
		return false, nil
	}
	table := batchRelationTableFor(kind, item.Field)
	var present []uint
	if err := tx.Table(table.name).Where(table.mediaColumn+" = ? AND "+table.relColumn+" IN ?", item.MediaID, append(append([]uint{}, added...), removed...)).
		Pluck(table.relColumn, &present).Error; err != nil {
		return false, err
	}
	presentSet := idSet(present)
	for _, relID := range added {
		if _, ok := presentSet[relID]; !ok {
			return false, nil
		}
	}
	for _, relID := range removed {
		if _, ok := presentSet[relID]; ok {
			return false, nil
		}
	}
	if len(removed) > 0 {
		var existing int64
		if err := tx.Model(batchReferenceModel(item.Field)).Where("id IN ?", removed).Count(&existing).Error; err != nil {
			return false, err
		}
		if existing != int64(len(removed)) {
			return false, nil
		}
	}
	if len(added) > 0 {
		if err := tx.Exec("DELETE FROM "+table.name+" WHERE "+table.mediaColumn+" = ? AND "+table.relColumn+" IN ?", item.MediaID, added).Error; err != nil {
			return false, err
		}
		if item.Field == BatchEditFieldCollection {
			for _, collectionID := range added {
				compact[collectionID] = struct{}{}
			}
		}
	}
	// 加回去掉的关系。作品集不记原来的位置：撤销「移出作品集」会把视频接在作品集末尾（对话框里有说明）。
	for _, relID := range removed {
		if err := insertBatchRelations(tx, kind, item.Field, relID, []uint{item.MediaID}); err != nil {
			return false, err
		}
	}
	return true, nil
}

func batchReferenceModel(field string) any {
	switch field {
	case BatchEditFieldTag:
		return &models.Tag{}
	case BatchEditFieldPerson:
		return &models.Person{}
	default:
		return &models.MediaCollection{}
	}
}
