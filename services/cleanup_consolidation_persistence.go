package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

var errConsolidationPersistence = errors.New("集中整理日志持久化失败")

func decodeConsolidation(ctx context.Context, task models.CleanupConsolidationTask) (cleanupConsolidationPlan, cleanupConsolidationJournal, error) {
	var plan cleanupConsolidationPlan
	journal := cleanupConsolidationJournal{SchemaVersion: cleanupConsolidationSchemaVersion}
	var stored models.CleanupConsolidationTaskPlan
	if err := database.DB.WithContext(ctx).Where("task_id = ?", task.ID).First(&stored).Error; err != nil {
		return plan, journal, err
	}
	if err := json.Unmarshal([]byte(stored.PlanJSON), &plan); err != nil {
		return plan, journal, err
	}
	if plan.SchemaVersion != cleanupConsolidationSchemaVersion || plan.Analysis == nil || plan.Preview.PreviewID != task.PreviewID || len(plan.Preview.Items) != task.Total {
		return plan, journal, fmt.Errorf("无法读取集中整理计划版本或数量")
	}
	var rows []models.CleanupConsolidationTaskItem
	if err := database.DB.WithContext(ctx).Where("task_id = ?", task.ID).Order("position").Find(&rows).Error; err != nil {
		return plan, journal, err
	}
	if len(rows) != task.Total {
		return plan, journal, fmt.Errorf("集中整理明细缺失，保留任务与文件")
	}
	journal.Items = make([]cleanupConsolidationItemJournal, len(rows))
	for i, row := range rows {
		var item cleanupConsolidationStoredItem
		if err := json.Unmarshal([]byte(row.JournalJSON), &item); err != nil {
			return plan, journal, err
		}
		expected := plan.Preview.Items[i]
		if row.Position != i || item.SchemaVersion != cleanupConsolidationSchemaVersion || item.Item.VideoID != expected.VideoID || len(item.Item.Files) != len(expected.Files) {
			return plan, journal, fmt.Errorf("集中整理日志与确认清单不一致")
		}
		journal.Items[i] = item.Item
	}
	var current models.CleanupConsolidationTask
	if err := database.DB.WithContext(ctx).Select("id", "version", "status").First(&current, task.ID).Error; err != nil {
		return plan, journal, err
	}
	if current.Version != task.Version || current.Status != task.Status {
		return plan, journal, ErrConsolidationConflict
	}
	restoreConsolidationFingerprints(&plan)
	return plan, journal, nil
}

func createConsolidationRecords(ctx context.Context, tx *gorm.DB, task *models.CleanupConsolidationTask, planJSON string, journal cleanupConsolidationJournal) error {
	if err := tx.WithContext(ctx).Create(task).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Create(&models.CleanupConsolidationTaskPlan{TaskID: task.ID, PlanJSON: planJSON}).Error; err != nil {
		return err
	}
	for start := 0; start < len(journal.Items); start += 100 {
		end := start + 100
		if end > len(journal.Items) {
			end = len(journal.Items)
		}
		rows := make([]models.CleanupConsolidationTaskItem, 0, end-start)
		for i := start; i < end; i++ {
			raw, err := json.Marshal(cleanupConsolidationStoredItem{SchemaVersion: cleanupConsolidationSchemaVersion, Item: journal.Items[i]})
			if err != nil {
				return err
			}
			rows = append(rows, models.CleanupConsolidationTaskItem{TaskID: task.ID, Position: i, JournalJSON: string(raw)})
		}
		if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
			return err
		}
	}
	return nil
}

// save 只写当前项及父 CAS，不改变内存版本；调用方必须在事务提交成功后 acceptSave。
func (e *consolidationExecution) save(tx *gorm.DB, index int, now time.Time) error {
	updates := map[string]interface{}{"updated_at": now, "completed": e.task.Completed, "bytes_done": e.task.BytesDone, "version": e.task.Version + 1, "status": e.task.Status, "error": e.task.Error, "active_slot": e.task.ActiveSlot, "finished_at": e.task.FinishedAt}
	result := tx.Model(&models.CleanupConsolidationTask{}).Where("id = ? AND version = ? AND status = ?", e.task.ID, e.task.Version, "running").Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConsolidationConflict
	}
	if index >= 0 {
		raw, err := json.Marshal(cleanupConsolidationStoredItem{SchemaVersion: cleanupConsolidationSchemaVersion, Item: e.journal.Items[index]})
		if err != nil {
			return err
		}
		result = tx.Model(&models.CleanupConsolidationTaskItem{}).Where("task_id = ? AND position = ?", e.task.ID, index).Update("journal_json", string(raw))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConsolidationConflict
		}
	}
	return nil
}

func (e *consolidationExecution) acceptSave(now time.Time) {
	e.task.Version++
	e.task.UpdatedAt = now
}

func (e *consolidationExecution) persistItem(index int) error {
	now := time.Now()
	if err := database.Transaction(func(tx *gorm.DB) error { return e.save(tx, index, now) }); err != nil {
		return fmt.Errorf("%w: %w", errConsolidationPersistence, err)
	}
	e.acceptSave(now)
	e.notify()
	return nil
}

func (e *consolidationExecution) persist() error { return e.persistItem(-1) }
