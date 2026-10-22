package services

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

func (s *PlaybackProxyService) SetTaskFailureService(store *TaskFailureService) {
	s.failureRecorder.configure(store, TaskFailureProxy)
}
func (s *PlaybackProxyService) TaskFailureWarning() string { return s.failureRecorder.Warning() }

type taskFailureLease struct {
	ClaimID string
	Item    taskFailureFrozen
}

var errTaskFailureSkip = errors.New("task_failure_skip")

func (s *PlaybackProxyService) claimTaskFailures(ctx context.Context, items []taskFailureFrozen, progress func(TaskFailureRetryItemResult)) []TaskFailureRetryItemResult {
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
		claimID := uuid.NewString()
		_, err := s.enqueueItemsGuarded(context.WithoutCancel(ctx), []uint{item.Item.MediaID}, "", func() error {
			if ctx.Err() != nil {
				reason = "cancelled"
				return errTaskFailureSkip
			}
			if _, busy := s.inFlight[item.Item.MediaID]; busy {
				reason = "already_running"
				return errTaskFailureSkip
			}
			err := database.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var snapshot taskFailureSnapshot
				var err error
				reason, snapshot, err = taskFailureEligibilitySnapshot(tx, TaskFailureProxy, item.Item)
				if err != nil {
					return err
				}
				if reason != "" {
					return errTaskFailureSkip
				}
				r := tx.Model(&models.VideoPlaybackProxy{}).Where("video_id = ? AND status = ? AND updated_at = ? AND failure_retry_claim = ''", item.Item.MediaID, models.PlaybackProxyStatusFailed, snapshot.UpdatedAt).UpdateColumn("failure_retry_claim", claimID)
				if r.Error != nil {
					return r.Error
				}
				if r.RowsAffected != 1 {
					reason = "failure_changed"
					return errTaskFailureSkip
				}
				return nil
			})
			if err != nil {
				return err
			}
			if s.failureRetryLeases == nil {
				s.failureRetryLeases = map[uint]taskFailureLease{}
			}
			s.failureRetryLeases[item.Item.MediaID] = taskFailureLease{ClaimID: claimID, Item: item}
			return nil
		})
		if errors.Is(err, errTaskFailureSkip) {
			publish(taskFailureResult(item, "skipped", reason))
		} else if err != nil {
			releaseTaskFailureClaims(TaskFailureProxy, claimID, []taskFailureFrozen{item})
			publish(taskFailureResult(item, "failed", "enqueue_failed"))
		} else {
			publish(taskFailureResult(item, "accepted", "queued"))
		}
	}
	return results
}
func (s *PlaybackProxyService) releaseFailureClaimsLocked() {
	for _, lease := range s.failureRetryLeases {
		releaseTaskFailureClaims(TaskFailureProxy, lease.ClaimID, []taskFailureFrozen{lease.Item})
	}
	s.failureRetryLeases = nil
}
