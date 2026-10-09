package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func storedConsolidationExecution(t *testing.T) *consolidationExecution {
	t.Helper()
	s, p, _, _, _ := prepareConsolidationRun(t, "near")
	plan := *s.consolidationPreview
	journal := newConsolidationJournal(*p)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	slot := "video_cleanup"
	task := models.CleanupConsolidationTask{PreviewID: p.PreviewID, OwnerScope: "test", ActiveSlot: &slot, Status: "running", Version: 1, Total: len(p.Items)}
	if err := database.Transaction(func(tx *gorm.DB) error {
		return createConsolidationRecords(context.Background(), tx, &task, string(raw), journal)
	}); err != nil {
		t.Fatal(err)
	}
	return &consolidationExecution{task: task, plan: plan, journal: journal}
}

type consolidationFailCommitPool struct{ gorm.ConnPool }

func (pool consolidationFailCommitPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := pool.ConnPool.(gorm.TxBeginner).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &consolidationFailCommitTx{Tx: tx}, nil
}

type consolidationFailCommitTx struct{ *sql.Tx }

func (tx consolidationFailCommitTx) Commit() error {
	_ = tx.Tx.Rollback()
	return errors.New("injected commit failure")
}

func TestCleanupConsolidationStorageRollsBackParentAndItem(t *testing.T) {
	for _, failure := range []string{"parent-cas", "child-update", "commit"} {
		t.Run(failure, func(t *testing.T) {
			e := storedConsolidationExecution(t)
			var before models.CleanupConsolidationTaskItem
			if err := database.DB.Where("task_id = ?", e.task.ID).First(&before).Error; err != nil {
				t.Fatal(err)
			}
			wantVersion := e.task.Version
			if failure == "parent-cas" {
				wantVersion++
				if err := database.DB.Model(&e.task).Update("version", wantVersion).Error; err != nil {
					t.Fatal(err)
				}
				e.task.Version--
			}
			if failure == "child-update" {
				const callback = "consolidation:fail-child"
				if err := database.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "cleanup_consolidation_task_items" {
						tx.AddError(errors.New("injected child failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer database.DB.Callback().Update().Remove(callback)
			}
			pool := database.DB.Statement.ConnPool
			if failure == "commit" {
				database.DB.Statement.ConnPool = consolidationFailCommitPool{ConnPool: pool}
			}
			version := e.task.Version
			e.journal.Items[0].Phase = "verified"
			e.journal.Items[0].Error = "must rollback"
			err := e.persistItem(0)
			database.DB.Statement.ConnPool = pool
			if err == nil {
				t.Fatal("failed transaction succeeded")
			}
			if e.task.Version != version {
				t.Fatal("memory version advanced before commit", e.task.Version, version)
			}
			var after models.CleanupConsolidationTaskItem
			var parent models.CleanupConsolidationTask
			if err := database.DB.First(&after, before.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.DB.First(&parent, e.task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.JournalJSON != before.JournalJSON || parent.Version != wantVersion {
				t.Fatal("transaction exposed mixed state", after.JournalJSON, parent.Version, wantVersion)
			}
		})
	}
}

func TestCleanupConsolidationCreateRecordsIsAtomic(t *testing.T) {
	s, p, a, _, _ := prepareConsolidationRun(t, "near")
	const callback = "consolidation:fail-create-item"
	if err := database.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "cleanup_consolidation_task_items" {
			tx.AddError(errors.New("injected item create failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Create().Remove(callback)
	if _, err := s.StartConsolidation(context.Background(), p.PreviewID, false); err == nil {
		t.Fatal("partial create accepted")
	}
	for _, model := range []interface{}{&models.CleanupConsolidationTask{}, &models.CleanupConsolidationTaskPlan{}, &models.CleanupConsolidationTaskItem{}} {
		var count int64
		if err := database.DB.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("partial evidence remained", model, count, err)
		}
	}
	if string(mustReadBytes(t, a.Path)) != "aaaa" {
		t.Fatal("files changed before all records created")
	}
}

func TestCleanupConsolidationConsistentReadRejectsMixedVersion(t *testing.T) {
	e := storedConsolidationExecution(t)
	const callback = "consolidation:mixed-read"
	if err := database.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "cleanup_consolidation_task_items" {
			if err := database.DB.Model(&models.CleanupConsolidationTask{}).Where("id = ?", e.task.ID).Update("version", e.task.Version+1).Error; err != nil {
				t.Error(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Query().Remove(callback)
	if _, _, err := decodeConsolidation(context.Background(), e.task); !errors.Is(err, ErrConsolidationConflict) {
		t.Fatal("mixed parent/item version returned", err)
	}
}

func TestCleanupConsolidationFinalItemErrorIsPersisted(t *testing.T) {
	s, p, _, _, _ := prepareConsolidationRun(t, "near")
	s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
		if phase == "publishing" {
			return errors.New("final-item-proof")
		}
		return nil
	}}
	task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, task.ID)
	if state.Status != "failed" || state.Items[0].Phase != "planned" || !strings.Contains(state.Items[0].Error, "final-item-proof") {
		t.Fatal("final item cleanup/error missing", state.Status, state.Items)
	}
}

func TestCleanupConsolidationRecoveryPersistenceFailureKeepsSlot(t *testing.T) {
	s, p, _, _, _ := prepareConsolidationRun(t, "near")
	s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
		if phase == "source_staged" {
			return ErrConsolidationConflict
		}
		return nil
	}}
	task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	before := waitConsolidation(t, s, task.ID)
	const callback = "consolidation:recover-child-failure"
	if err := database.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "cleanup_consolidation_task_items" {
			tx.AddError(errors.New("recovery item persistence failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Update().Remove(callback)
	if err := s.RecoverConsolidations(context.Background()); err == nil {
		t.Fatal("recovery child failure ignored")
	}
	var parent models.CleanupConsolidationTask
	if err := database.DB.First(&parent, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if parent.Status != "running" || parent.ActiveSlot == nil || parent.Version != before.Version {
		t.Fatal("failed reconciliation released slot or advanced parent", parent)
	}
}
