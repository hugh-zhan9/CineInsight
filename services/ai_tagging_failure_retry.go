package services

import (
	"context"
	"gorm.io/gorm"
	"time"
	"video-master/database"
	"video-master/models"
)

func (s *AITaggingService) SetTaskFailureService(store *TaskFailureService) {
	s.failureRecorder.configure(store, TaskFailureAI)
}
func (s *AITaggingService) TaskFailureWarning() string { return s.failureRecorder.Warning() }
func (s *AITaggingService) claimTaskFailures(ctx context.Context, items []taskFailureFrozen, progress func(TaskFailureRetryItemResult)) []TaskFailureRetryItemResult {
	results := make([]TaskFailureRetryItemResult, 0, len(items))
	publish := func(result TaskFailureRetryItemResult) {
		results = append(results, result)
		if progress != nil {
			progress(result)
		}
	}
	for _, item := range items {
		if ctx.Err() != nil {
			publish(taskFailureResult(item, "skipped", "cancelled"))
			continue
		}
		reason := item.Reason
		if reason != "" {
			publish(taskFailureResult(item, "skipped", reason))
			continue
		}
		err := database.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			var snapshot taskFailureSnapshot
			reason, snapshot, err = taskFailureEligibilitySnapshot(tx, TaskFailureAI, item.Item)
			if err != nil || reason != "" {
				return err
			}

			if s.beforeFailureClaim != nil {
				s.beforeFailureClaim(tx)
			}
			now := time.Now()
			r := tx.Model(&models.AITaggingState{}).Where("video_id = ? AND status = ? AND updated_at = ?", item.Item.MediaID, models.AITaggingStateStatusFailed, snapshot.UpdatedAt).Updates(map[string]any{"failure_retry_source_version": item.Item.SourceVersion, "status": models.AITaggingStateStatusPending, "skip_reason": aiTaggingSkipReasonManualRetry, "evidence_fingerprint": "", "last_error": "", "updated_at": now})
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected == 0 {
				reason = "failure_changed"
				return nil
			}
			return tx.Model(&models.AITagCandidate{}).Where("video_id = ? AND status = ?", item.Item.MediaID, models.AITagCandidateStatusPending).Update("status", models.AITagCandidateStatusSuperseded).Error
		})
		if err != nil {
			publish(taskFailureResult(item, "failed", "enqueue_failed"))
		} else if reason != "" {
			publish(taskFailureResult(item, "skipped", reason))
		} else {
			publish(taskFailureResult(item, "accepted", "queued"))
		}
	}
	s.Trigger()
	return results
}
