package services

import (
	"context"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"time"
	"video-master/database"
	"video-master/models"
)

func (s *TechnicalBackfillService) SetTaskFailureService(store *TaskFailureService) {
	s.failureRecorder.configure(store, TaskFailureTechnical)
}
func (s *TechnicalBackfillService) TaskFailureWarning() string { return s.failureRecorder.Warning() }

// claimTaskFailures reserves the existing owner briefly, then performs conditional
// claims without its status mutex. Status and cancellation remain available.
func (s *TechnicalBackfillService) claimTaskFailures(ctx context.Context, items []taskFailureFrozen, progress func(TaskFailureRetryItemResult)) []TaskFailureRetryItemResult {
	results := make([]TaskFailureRetryItemResult, 0, len(items))
	publish := func(result TaskFailureRetryItemResult) {
		results = append(results, result)
		if progress != nil {
			progress(result)
		}
	}
	ownerCtx, ownerCancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		ownerCancel()
		for _, item := range items {
			publish(taskFailureResult(item, "skipped", "already_running"))
		}
		return results
	}
	now := time.Now()
	s.status = TechnicalBackfillStatus{Running: true, Preparing: true, StartedAt: &now, UpdatedAt: &now, Failures: []TechnicalBackfillFailure{}}
	s.cancel = ownerCancel
	s.pauseHook = nil
	s.worker.Add(1)
	registry := s.registry
	s.mu.Unlock()
	registry.Begin(BackgroundTaskTechnical)
	s.failureRecorder.begin()
	// Explicit submission cancellation stops new claims. Already accepted work is
	// still governed by ownerCtx and the existing technical Cancel/maintenance path.
	claimCtx, claimCancel := context.WithCancel(ownerCtx)
	stopForward := context.AfterFunc(ctx, claimCancel)
	defer stopForward()
	defer claimCancel()
	claimID := uuid.NewString()
	candidates := []technicalBackfillCandidate{}
	claimed := []taskFailureFrozen{}
	for _, item := range items {
		reason := item.Reason
		if claimCtx.Err() != nil {
			reason = "cancelled"
		}
		if reason != "" {
			publish(taskFailureResult(item, "skipped", reason))
			continue
		}
		err := database.DB.WithContext(claimCtx).Transaction(func(tx *gorm.DB) error {
			var snapshot taskFailureSnapshot
			var err error
			reason, snapshot, err = taskFailureEligibilitySnapshot(tx, TaskFailureTechnical, item.Item)
			if err != nil || reason != "" {
				return err
			}
			if hook := s.beforeFailureClaim; hook != nil {
				hook(tx)
			}
			r := tx.Model(&models.VideoTechnicalMetadata{}).Where("video_id = ? AND last_error <> '' AND updated_at = ? AND failure_retry_claim = ''", item.Item.MediaID, snapshot.UpdatedAt).UpdateColumn("failure_retry_claim", claimID)
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				reason = "failure_changed"
			}
			return nil
		})
		switch {
		case claimCtx.Err() != nil && err != nil:
			publish(taskFailureResult(item, "skipped", "cancelled"))
		case err != nil:
			publish(taskFailureResult(item, "failed", "enqueue_failed"))
		case reason != "":
			publish(taskFailureResult(item, "skipped", reason))
		default:
			candidates = append(candidates, technicalBackfillCandidate{ID: item.Item.MediaID, SourceVersion: item.Item.SourceVersion})
			claimed = append(claimed, item)
			publish(taskFailureResult(item, "accepted", "queued"))
		}
	}
	s.mu.Lock()
	s.status.Preparing = false
	s.status.Total = len(candidates)
	s.mu.Unlock()
	// Transfer the reservation's wait-group ownership to the normal worker.
	go func() {
		defer s.worker.Done()
		defer registry.End(BackgroundTaskTechnical)
		defer releaseTaskFailureClaims(TaskFailureTechnical, claimID, claimed)
		s.run(ownerCtx, candidates)
	}()
	return results
}
