package services

import (
	"context"
	"time"

	"gorm.io/gorm"
	"video-master/models"
	"video-master/services/btclient"
)

func btNextObservationEpoch(task models.BTTask, files []models.BTFile) (uint64, error) {
	maximum := task.ObservationEpoch
	for _, file := range files {
		if file.EligibilityRevision > maximum {
			maximum = file.EligibilityRevision
		}
	}
	if maximum == ^uint64(0) {
		return 0, ErrBTTask
	}
	return maximum + 1, nil
}

// recordObservation advances qualification without invalidating the user's
// command confirmation. The command and observation CAS together fence both a
// newer user intent and another observer. P003 still checks task command/attempt
// and the exact file eligibility epoch before and after import.
func (s *BTService) recordObservation(ctx context.Context, task *models.BTTask, files []models.BTFile, mapping map[string]btclient.QBFile, remoteState string) error {
	next, err := btNextObservationEpoch(*task, files)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	err = btTaskTransaction(ctx, func(tx *gorm.DB) error {
		fields := map[string]any{"observation_epoch": next, "remote_state": remoteState, "last_observed_at": now}
		if mapping != nil {
			fields["error_code"] = ""
		}
		result := tx.Model(&models.BTTask{}).Where("id = ? AND connection_id = ? AND revision = ? AND stage = ? AND attempt_generation = ? AND observation_epoch = ?", task.ID, task.ConnectionID, task.Revision, task.Stage, task.AttemptGeneration, task.ObservationEpoch).Updates(fields)
		if result.Error != nil {
			return ErrBTTask
		}
		if result.RowsAffected != 1 {
			return ErrBTConflict
		}
		if mapping != nil {
			for _, file := range files {
				if file.Padding {
					continue
				}
				observed, ok := mapping[file.ManifestFileID]
				if !ok {
					return ErrBTChanged
				}
				result = tx.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID).Updates(map[string]any{"progress": observed.Progress, "eligibility_revision": next, "qb_file_index": observed.Index})
				if result.Error != nil {
					return ErrBTTask
				}
				if result.RowsAffected != 1 {
					return ErrBTConflict
				}
			}
		}
		return nil
	})
	if err == nil {
		task.ObservationEpoch = next
		task.RemoteState = remoteState
		task.LastObservedAt = &now
		if mapping != nil {
			task.ErrorCode = ""
		}
		s.changed(*task)
	}
	return err
}
