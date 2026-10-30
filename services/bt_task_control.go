package services

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"video-master/models"
)

func (s *BTService) Control(ctx context.Context, r BTControlRequest) (BTTaskView, error) {
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return BTTaskView{}, err
	}
	defer leave()
	task, _, err := btLoadTask(ctx, r.TaskID)
	if err != nil {
		return BTTaskView{}, err
	}
	local := r.Action == "abandon" || (r.Action == "cancel_draft" && task.Stage == "draft")
	if !local {
		if err = s.loadClients(ctx); err != nil {
			return BTTaskView{}, err
		}
		if err = s.writeAllowed(ctx); err != nil {
			return BTTaskView{}, err
		}
	}
	// Pure user observations/import do not hold a cache lock across callbacks.
	if r.Action == "reimport" || r.Action == "reconcile" {
		return s.controlTaskLocked(ctx, r)
	}
	var view BTTaskView
	err = s.withTaskCache(ctx, r.TaskID, func(scoped context.Context, _ *gorm.DB, _ *btCacheFiles) error {
		var err error
		view, err = s.controlTaskLocked(scoped, r)
		return err
	})
	return view, err
}
func (s *BTService) controlTaskLocked(ctx context.Context, r BTControlRequest) (BTTaskView, error) {
	task, files, err := btLoadTask(ctx, r.TaskID)
	if err != nil {
		return BTTaskView{}, err
	}
	if task.Revision != r.ExpectedRevision {
		return BTTaskView{}, ErrBTConflict
	}
	if r.Action == "abandon" {
		if !r.Confirmed || task.Stage == "released" || (task.Stage != "uncertain" && !task.UnresolvedWrite && task.PendingAction == "") {
			return BTTaskView{}, ErrBTConflict
		}
		if err = s.abandonTaskLocal(ctx, &task); err != nil {
			return BTTaskView{}, err
		}
		_ = s.cleanupTaskCaches(ctx, task)
		return s.Get(ctx, task.ID)
	}
	if r.Action == "cancel_draft" && task.Stage == "draft" {
		if err = s.updateTask(ctx, &task, map[string]any{"stage": "cancelled"}); err != nil {
			return BTTaskView{}, err
		}
		_ = s.cleanupTaskCaches(ctx, task)
		return s.Get(ctx, task.ID)
	}
	if task.ConnectionID != s.loadedID {
		return BTTaskView{}, ErrBTConflict
	}
	switch r.Action {
	case "stop", "start":
		if task.Stage != "active" {
			return BTTaskView{}, ErrBTConflict
		}
		stage := "queued_stop"
		if r.Action == "start" {
			stage = "queued_start"
		}
		err = s.updateTask(ctx, &task, map[string]any{"stage": stage, "pending_action": r.Action})
	case "reconcile":
		if task.Stage != "uncertain" && task.Stage != "abandoned" && task.Stage != "missing" && task.Stage != "conflict" {
			return BTTaskView{}, ErrBTConflict
		}
		err = s.reconcileTask(ctx, &task, files)
	case "reconfirm_submit":
		if !r.Confirmed || (task.Stage != "uncertain" && task.Stage != "abandoned") {
			return BTTaskView{}, ErrBTConflict
		}
		action := task.PendingAction
		if action == "restore" {
			if task.SelectionDigest == "" {
				err = s.prepareRestoredDraft(ctx, &task, files)
			} else {
				if !btConfirmedSelection(task, files) {
					return BTTaskView{}, ErrBTSelection
				}
				remote, _, e := s.remoteFiles(ctx, task, files, false, 2)
				if e != nil {
					return BTTaskView{}, e
				}
				fields := map[string]any{"stage": "active", "pending_action": "", "unresolved_write": true, "remote_state": remote.State, "error_code": "write_history_unresolved"}
				if task.Stage == "abandoned" {
					err = s.reactivateTask(ctx, &task, fields)
				} else {
					err = s.updateTask(ctx, &task, fields)
				}
			}
			break
		}
		if action == "" {
			if len(files) == 0 {
				action = "metadata"
			} else {
				action = "add"
			}
		}
		stage := ""
		switch action {
		case "add":
			if task.SelectionDigest == "" {
				return BTTaskView{}, ErrBTSelection
			}
			if _, err = s.readCache(ctx, task); err != nil {
				return BTTaskView{}, err
			}
			stage = "queued_add"
		case "metadata":
			if s.remote.CheckQBMetadataProfile(ctx) != nil {
				return BTTaskView{}, ErrBTMetadata
			}
			if _, err = s.readMetadataCache(ctx, task); err != nil {
				return BTTaskView{}, err
			}
			stage = "queued_metadata"
		case "priority_zero", "priority_select":
			stage = "selecting"
		case "start":
			stage = "queued_start"
		case "stop":
			stage = "queued_stop"
		case "cancel_stop", "cancel_delete":
			stage = "cancel_pending"
		default:
			return BTTaskView{}, ErrBTConflict
		}
		remote, _, findErr := s.findRemote(ctx, task)
		if findErr != nil && !errors.Is(findErr, errBTMissing) {
			return BTTaskView{}, findErr
		}
		if action == "priority_zero" || action == "priority_select" {
			if findErr != nil {
				return BTTaskView{}, findErr
			}
			if !btStopped(remote.State) {
				return BTTaskView{}, ErrBTChanged
			}
		}
		if (action == "add") && findErr == nil && !btStopped(remote.State) {
			return BTTaskView{}, ErrBTChanged
		}
		fields := map[string]any{"stage": stage, "error_code": "", "pending_action": action}
		if task.Stage == "abandoned" {
			err = s.reactivateTask(ctx, &task, fields)
		} else {
			err = s.updateTask(ctx, &task, fields)
		}
	case "cancel_draft":
		if task.Stage != "metadata_ready" && task.Stage != "metadata_wait" && task.Stage != "metadata_adding" && task.Stage != "queued_metadata" {
			return BTTaskView{}, ErrBTConflict
		}
		err = s.updateTask(ctx, &task, map[string]any{"stage": "cancel_pending", "pending_action": "cancel_stop"})
	case "reimport":
		if task.Stage != "active" {
			return BTTaskView{}, ErrBTConflict
		}
		err = s.observeFiles(ctx, &task, files)
	case "release":
		if !r.Confirmed || task.UnresolvedWrite || task.PendingAction != "" || (task.Stage != "active" && task.Stage != "missing" && task.Stage != "conflict" && task.Stage != "cancelled") {
			return BTTaskView{}, ErrBTConflict
		}
		remote, _, findErr := s.findRemote(ctx, task)
		if findErr != nil && !errors.Is(findErr, errBTMissing) {
			return BTTaskView{}, findErr
		}
		if findErr == nil && !btStopped(remote.State) {
			return BTTaskView{}, ErrBTChanged
		}
		if findErr == nil {
			rows, e := s.remote.QBFiles(ctx, remote.Hash)
			if e != nil {
				return BTTaskView{}, e
			}
			mapping, e := btMapFiles(files, rows)
			if e != nil || !btSelectionMatches(files, mapping) {
				return BTTaskView{}, ErrBTChanged
			}
			for _, f := range files {
				if f.Selected && mapping[f.ManifestFileID].Progress != 1 {
					return BTTaskView{}, ErrBTChanged
				}
			}
		}
		if err = btNoForeignFiles(task, files); err != nil {
			return BTTaskView{}, err
		}
		err = s.updateTask(ctx, &task, map[string]any{"stage": "released", "error_code": ""})
	default:
		return BTTaskView{}, ErrBTConflict
	}
	if err != nil {
		return BTTaskView{}, err
	}
	s.notify()
	return s.Get(ctx, task.ID)
}

// ExpireDrafts only removes unsubmitted private previews; root protection remains.
func (s *BTService) ExpireDrafts(ctx context.Context) error {
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer leave()
	cutoff := time.Now().Add(-24 * time.Hour)
	var candidates []models.BTTask
	err = btTaskOperation(ctx, func(db *gorm.DB) error {
		return db.Where("stage IN ? AND updated_at < ?", []string{"draft", "metadata_ready", "metadata_wait", "metadata_adding", "queued_metadata"}, cutoff).Order("created_at ASC,id ASC").Limit(200).Find(&candidates).Error
	})
	if err != nil {
		return ErrBTTask
	}
	for _, candidate := range candidates {
		err = s.withTaskCache(ctx, candidate.ID, func(ctx context.Context, db *gorm.DB, _ *btCacheFiles) error {
			var task models.BTTask
			if db.First(&task, "id = ?", candidate.ID).Error != nil {
				return ErrBTTask
			}
			if !sameBTCacheIntent(task, candidate) || !task.UpdatedAt.Before(cutoff) {
				return nil
			}
			stage, action := "cancel_pending", "cancel_stop"
			if task.Stage == "draft" {
				stage = "cancelled"
				action = ""
			}
			if err := s.updateTask(ctx, &task, map[string]any{"stage": stage, "pending_action": action}); err != nil {
				return err
			}
			if stage == "cancelled" {
				_ = s.cleanupTaskCaches(ctx, task)
			}
			return nil
		})
		if errors.Is(err, ErrBTTaskBusy) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return s.expireOrphanCaches(ctx)
}
