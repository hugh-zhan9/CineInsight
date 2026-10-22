package services

import (
	"errors"
	"fmt"
	"gorm.io/gorm"
	"time"
	"video-master/database"
	"video-master/models"
)

type taskFailureSnapshot struct {
	Claim         string
	Version       string
	SourceVersion string
	Failed        bool
	UpdatedAt     time.Time
}

func readTaskFailureSnapshot(db *gorm.DB, key string, videoID uint, checkFile bool) (taskFailureSnapshot, error) {
	out := taskFailureSnapshot{}
	switch key {
	case TaskFailureAI:
		var row models.AITaggingState
		if err := db.Where("video_id = ?", videoID).First(&row).Error; err != nil {
			return out, err
		}
		out.Failed = row.Status == models.AITaggingStateStatusFailed
		out.UpdatedAt = row.UpdatedAt
		out.Version = fmt.Sprintf("%s:%d:%s", row.UpdatedAt.UTC().Format(time.RFC3339Nano), row.AttemptCount, row.Status)
	case TaskFailureTechnical:
		var row models.VideoTechnicalMetadata
		if err := db.Where("video_id = ?", videoID).First(&row).Error; err != nil {
			return out, err
		}
		out.Failed = row.LastError != ""
		out.Claim = row.FailureRetryClaim
		out.UpdatedAt = row.UpdatedAt
		out.Version = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
		if row.LastAttemptSourceSize != nil && row.LastAttemptSourceModTimeNS != nil {
			out.SourceVersion = videoSourceVersion(videoID, *row.LastAttemptSourceSize, *row.LastAttemptSourceModTimeNS)
		}
	case TaskFailureProxy:
		var row models.VideoPlaybackProxy
		if err := db.Where("video_id = ?", videoID).First(&row).Error; err != nil {
			return out, err
		}
		out.Failed = row.Status == models.PlaybackProxyStatusFailed
		out.Claim = row.FailureRetryClaim
		out.UpdatedAt = row.UpdatedAt
		out.Version = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
		out.SourceVersion = videoSourceVersion(videoID, row.SourceSize, row.SourceModTimeNS)
	default:
		return out, fmt.Errorf("task_failure_owner_unknown")
	}
	if checkFile || out.SourceVersion == "" {
		var v models.Video
		if err := db.Select("id", "path", "is_stale").First(&v, videoID).Error; err != nil {
			return out, err
		}
		if checkFile && v.IsStale {
			return out, fmt.Errorf("offline")
		}
		fp, err := mediaProbeStat(v.Path)
		if err != nil {
			if checkFile {
				return out, fmt.Errorf("offline")
			}
		} else {
			out.SourceVersion = videoSourceVersion(videoID, fp.size, fp.modTimeNS)
		}
	}
	return out, nil
}
func taskFailureEligibility(db *gorm.DB, key string, item models.TaskFailureItem) (string, error) {
	reason, _, err := taskFailureEligibilitySnapshot(db, key, item)
	return reason, err
}
func taskFailureEligibilitySnapshot(db *gorm.DB, key string, item models.TaskFailureItem) (string, taskFailureSnapshot, error) {
	var empty taskFailureSnapshot
	var video models.Video
	q, err := applyScanRootScope(db.Model(&models.Video{}))
	if err != nil {
		return "", empty, err
	}
	err = q.Select("videos.id", "videos.is_stale").First(&video, item.MediaID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "deleted_or_out_of_scope", empty, nil
	}
	if err != nil {
		return "", empty, err
	}
	if video.IsStale {
		return "offline", empty, nil
	}
	snapshot, err := readTaskFailureSnapshot(db, key, item.MediaID, true)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "failure_no_longer_current", empty, nil
	}
	if err != nil {
		if err.Error() == "offline" {
			return "offline", empty, nil
		}
		return "", empty, err
	}
	if snapshot.Claim != "" {
		return "already_queued", empty, nil
	}
	if !snapshot.Failed {
		return "succeeded_or_queued", empty, nil
	}
	if snapshot.Version != item.StateVersion {
		return "failure_changed", empty, nil
	}
	if item.SourceVersion == "" || snapshot.SourceVersion != item.SourceVersion {
		return "source_changed", empty, nil
	}
	return "", snapshot, nil
}
func taskFailureResult(item taskFailureFrozen, status, reason string) TaskFailureRetryItemResult {
	return TaskFailureRetryItemResult{ItemID: item.Item.ID, MediaID: item.Item.MediaID, Status: status, Reason: reason}
}

// releaseTaskFailureClaims only releases the original failed attempt's own lease.
func releaseTaskFailureClaims(key, claimID string, items []taskFailureFrozen) {
	for _, item := range items {
		snapshot, err := readTaskFailureSnapshot(database.DB, key, item.Item.MediaID, false)
		if err != nil || snapshot.Version != item.Item.StateVersion {
			continue
		}
		switch key {
		case TaskFailureTechnical:
			database.DB.Model(&models.VideoTechnicalMetadata{}).Where("video_id = ? AND failure_retry_claim = ? AND updated_at = ?", item.Item.MediaID, claimID, snapshot.UpdatedAt).UpdateColumn("failure_retry_claim", "")
		case TaskFailureProxy:
			database.DB.Model(&models.VideoPlaybackProxy{}).Where("video_id = ? AND failure_retry_claim = ? AND updated_at = ?", item.Item.MediaID, claimID, snapshot.UpdatedAt).UpdateColumn("failure_retry_claim", "")
		}
	}
}
