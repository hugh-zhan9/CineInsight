package services

import (
	"context"
	"strings"

	"gorm.io/gorm"
	"video-master/models"
)

// A claim is persisted before a request can leave this process. The sent stage
// cannot be automatically replayed by another coordinator or after cancellation.
// Historical uncertainty is sticky: a later acknowledged request cannot prove
// that an older timed-out request will never arrive.
type btWriteClaim struct{ priorUnresolved bool }

func (s *BTService) claimWrite(ctx context.Context, t *models.BTTask, action string) (btWriteClaim, error) {
	claim := btWriteClaim{priorUnresolved: t.UnresolvedWrite}
	err := s.taskTransaction(ctx, t.ID, func(_ context.Context, tx *gorm.DB) error {
		result := tx.Model(&models.BTTask{}).Where("id = ? AND connection_id = ? AND revision = ? AND stage = ? AND attempt_generation = ?", t.ID, t.ConnectionID, t.Revision, t.Stage, t.AttemptGeneration).
			Where("EXISTS (SELECT 1 FROM bt_connections c WHERE c.id = bt_tasks.connection_id AND c.slot = ? AND c.revision = ? AND c.enabled = ?)", "local", s.loadedRevision, true).
			Updates(map[string]any{"stage": action + "_sent", "pending_action": action, "unresolved_write": true, "attempt_generation": t.AttemptGeneration + 1, "revision": t.Revision + 1, "error_code": ""})
		if result.Error != nil {
			return ErrBTTask
		}
		if result.RowsAffected != 1 {
			return ErrBTConflict
		}
		if tx.First(t, "id = ?", t.ID).Error != nil {
			return ErrBTTask
		}
		return nil
	})
	if err == nil {
		s.changed(*t)
	}
	return claim, err
}
func (s *BTService) acknowledgeWrite(ctx context.Context, t *models.BTTask, claim btWriteClaim, stage string, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["stage"] = stage
	if _, specified := fields["pending_action"]; !specified {
		fields["pending_action"] = btActionForStage(stage)
	}
	fields["unresolved_write"] = claim.priorUnresolved
	fields["error_code"] = ""
	if claim.priorUnresolved {
		fields["error_code"] = "write_history_unresolved"
	}
	return s.updateTask(ctx, t, fields)
}
func btSentStage(stage string) bool { return strings.HasSuffix(stage, "_sent") }
func btActionForStage(stage string) string {
	switch stage {
	case "queued_add", "adding", "add_sent":
		return "add"
	case "queued_metadata", "metadata_adding", "metadata_wait", "metadata_sent":
		return "metadata"
	case "selecting", "priority_zero_sent":
		return "priority_zero"
	case "priority_select_ready", "priority_select_sent":
		return "priority_select"
	case "starting", "start_ready", "queued_start", "start_sent":
		return "start"
	case "queued_stop", "stop_sent":
		return "stop"
	case "cancel_pending", "cancel_stop_sent":
		return "cancel_stop"
	case "cancel_delete_sent":
		return "cancel_delete"
	}
	return ""
}

// reactivateTask closes the count/retire race when a non-active tombstone is
// explicitly reopened. Parent epoch and task revision move in one transaction.
func (s *BTService) reactivateTask(ctx context.Context, t *models.BTTask, fields map[string]any) error {
	fields["revision"] = t.Revision + 1
	err := s.taskTransaction(ctx, t.ID, func(_ context.Context, tx *gorm.DB) error {
		var c models.BTConnection
		if tx.First(&c, "id = ? AND slot = ? AND revision = ? AND enabled = ?", t.ConnectionID, "local", s.loadedRevision, true).Error != nil {
			return ErrBTConflict
		}
		if err := ClaimBTTaskEpoch(tx, c.ID, c.Revision, c.TaskEpoch); err != nil {
			return err
		}
		changed := tx.Model(&models.BTTask{}).Where("id = ? AND connection_id = ? AND revision = ? AND stage = ? AND attempt_generation = ?", t.ID, t.ConnectionID, t.Revision, t.Stage, t.AttemptGeneration).Updates(fields)
		if changed.Error != nil {
			return ErrBTTask
		}
		if changed.RowsAffected != 1 {
			return ErrBTConflict
		}
		if tx.First(t, "id = ?", t.ID).Error != nil {
			return ErrBTTask
		}
		return nil
	})
	if err == nil {
		s.changed(*t)
	}
	return err
}

func btConfirmedSelection(t models.BTTask, files []models.BTFile) bool {
	if t.SelectionDigest == "" {
		return false
	}
	ids := []string{}
	for _, f := range files {
		if f.Selected {
			if f.Padding || !f.Selectable {
				return false
			}
			ids = append(ids, f.ManifestFileID)
		}
	}
	return len(ids) > 0 && btSelectionDigest(ids) == t.SelectionDigest
}
func (s *BTService) abandonTaskLocal(ctx context.Context, t *models.BTTask) error {
	err := s.taskTransaction(ctx, t.ID, func(_ context.Context, tx *gorm.DB) error {
		var c models.BTConnection
		if tx.First(&c, "id = ? AND slot = ?", t.ConnectionID, "local").Error != nil {
			return ErrBTConflict
		}
		// Exiting management is allowed even while disabled. Fence retirement with
		// the same parent epoch, without invoking enabled-only task admission.
		parent := tx.Model(&models.BTConnection{}).Where("id = ? AND slot = ? AND revision = ? AND task_epoch = ?", c.ID, "local", c.Revision, c.TaskEpoch).UpdateColumn("task_epoch", gorm.Expr("task_epoch + 1"))
		if parent.Error != nil {
			return ErrBTTask
		}
		if parent.RowsAffected != 1 {
			return ErrBTConflict
		}
		row := tx.Model(&models.BTTask{}).Where("id = ? AND connection_id = ? AND revision = ? AND stage = ? AND attempt_generation = ?", t.ID, t.ConnectionID, t.Revision, t.Stage, t.AttemptGeneration).Updates(map[string]any{"stage": "abandoned", "revision": t.Revision + 1})
		if row.Error != nil {
			return ErrBTTask
		}
		if row.RowsAffected != 1 {
			return ErrBTConflict
		}
		if tx.First(t, "id = ?", t.ID).Error != nil {
			return ErrBTTask
		}
		return nil
	})
	if err == nil {
		s.changed(*t)
	}
	return err
}

// Keep the acknowledged terminal state and its cache retirement in one guard.
func (s *BTService) acknowledgeAndCleanup(ctx context.Context, t *models.BTTask, claim btWriteClaim, stage string, fields map[string]any) error {
	return s.withTaskCache(ctx, t.ID, func(ctx context.Context, _ *gorm.DB, _ *btCacheFiles) error {
		if err := s.acknowledgeWrite(ctx, t, claim, stage, fields); err != nil {
			return err
		}
		if stage == "cancelled" || !t.UnresolvedWrite {
			_ = s.cleanupTaskCaches(ctx, *t)
		}
		return nil
	})
}
