package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"video-master/models"
)

func btDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func btString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func btTaskUUID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}
func btLoadTask(ctx context.Context, id string) (models.BTTask, []models.BTFile, error) {
	var task models.BTTask
	var files []models.BTFile
	if !btTaskUUID(id) {
		return task, nil, ErrBTTask
	}
	err := btTaskOperation(ctx, func(db *gorm.DB) error {
		if err := db.First(&task, "id = ?", id).Error; err != nil {
			return ErrBTTask
		}
		if err := db.Where("task_id = ?", id).Order("position ASC").Find(&files).Error; err != nil {
			return ErrBTTask
		}
		return nil
	})
	return task, files, err
}
func (s *BTService) updateTask(ctx context.Context, task *models.BTTask, fields map[string]any) error {
	return s.updateTaskFenced(ctx, task, fields, false)
}
func (s *BTService) updateObservedTask(ctx context.Context, task *models.BTTask, fields map[string]any) error {
	return s.updateTaskFenced(ctx, task, fields, true)
}
func (s *BTService) updateTaskFenced(ctx context.Context, task *models.BTTask, fields map[string]any, observation bool) error {
	fields["revision"] = task.Revision + 1
	err := s.taskTransaction(ctx, task.ID, func(_ context.Context, tx *gorm.DB) error {
		query := tx.Model(&models.BTTask{}).Where("id = ? AND connection_id = ? AND revision = ? AND stage = ? AND attempt_generation = ?", task.ID, task.ConnectionID, task.Revision, task.Stage, task.AttemptGeneration)
		if observation {
			query = query.Where("observation_epoch = ?", task.ObservationEpoch)
		}
		result := query.Updates(fields)
		if result.Error != nil {
			return ErrBTTask
		}
		if result.RowsAffected != 1 {
			return ErrBTConflict
		}
		return tx.First(task, "id = ?", task.ID).Error
	})
	if err == nil {
		s.changed(*task)
	}
	return err
}
func btFileKind(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	for _, candidate := range strings.Split(defaultVideoExtensions, ",") {
		if ext == candidate {
			return "video"
		}
	}
	if ext == ".srt" || ext == ".ass" || ext == ".ssa" || ext == ".vtt" {
		return "subtitle"
	}
	return "other"
}
func btDefaultSelection(files []models.BTFile) map[string]bool {
	selected := map[string]bool{}
	stems := map[string]bool{}
	for _, f := range files {
		if f.Selectable && btFileKind(f.RelativePath) == "video" {
			selected[f.ManifestFileID] = true
			stems[strings.TrimSuffix(f.RelativePath, filepath.Ext(f.RelativePath))] = true
		}
	}
	for _, f := range files {
		if f.Selectable && btFileKind(f.RelativePath) == "subtitle" && stems[strings.TrimSuffix(f.RelativePath, filepath.Ext(f.RelativePath))] {
			selected[f.ManifestFileID] = true
		}
	}
	return selected
}
func btView(task models.BTTask, files []models.BTFile) BTTaskView {
	out := BTTaskView{ID: task.ID, Revision: task.Revision, Title: task.Title, IndexerID: task.IndexerID, IndexerName: task.IndexerName, Stage: task.Stage, RemoteState: task.RemoteState, SavePath: task.SaveRoot, ErrorCode: task.ErrorCode, Files: []BTFileView{}, Actions: []string{}}
	out.PendingAction = task.PendingAction
	out.UnresolvedWrite = task.UnresolvedWrite
	if task.UnresolvedWrite && out.ErrorCode == "" {
		out.ErrorCode = "write_history_unresolved"
	}
	switch task.Stage {
	case "add_sent":
		out.Stage = "adding"
	case "metadata_sent":
		out.Stage = "metadata_wait"
	case "priority_zero_sent", "priority_select_ready", "priority_select_sent":
		out.Stage = "selecting"
	case "start_ready", "start_sent":
		out.Stage = "starting"
	case "stop_sent":
		out.Stage = "active"
	case "cancel_stop_sent", "cancel_delete_sent":
		out.Stage = "cancel_pending"
	}
	var size, total int64
	weighted := float64(0)
	selected := 0
	complete := true
	for _, f := range files {
		total += f.Size
		videoID := f.VideoID
		importState := f.ImportState
		imported := f.ImportState == "imported" && f.PublicationState != "pending_new" && f.PublicationState != "pending_restore"
		if !imported {
			videoID = nil
			if f.ImportState == "imported" {
				importState = "importing"
			}
		}
		out.Files = append(out.Files, BTFileView{ID: f.ManifestFileID, Path: f.RelativePath, Size: strconv.FormatInt(f.Size, 10), Selected: f.Selected, Selectable: f.Selectable, Padding: f.Padding, Kind: btFileKind(f.RelativePath), Progress: f.Progress, ImportState: importState, VideoID: videoID, ErrorCode: f.ErrorCode})
		if f.Selected {
			if btFileKind(f.RelativePath) == "video" {
				out.SelectedVideoCount++
			}
			size += f.Size
			weighted += float64(f.Size) * f.Progress
			selected++
			if f.Progress != 1 {
				complete = false
			}
		}
		if imported {
			out.ImportedCount++
		}
		if f.ImportState == "failed" {
			out.FailedCount++
		}
	}
	out.SelectedBytes = strconv.FormatInt(size, 10)
	out.TotalBytes = strconv.FormatInt(total, 10)
	if size > 0 {
		out.Progress = weighted / float64(size)
	}
	out.DownloadComplete = selected > 0 && complete
	switch task.Stage {
	case "draft", "metadata_ready":
		expires := task.UpdatedAt.Add(24 * time.Hour)
		out.ExpiresAt = &expires
		out.Actions = []string{"cancel_draft"}
		if task.UnresolvedWrite || task.PendingAction != "" {
			out.Actions = append(out.Actions, "abandon")
		}
	case "metadata_wait", "metadata_adding", "queued_metadata":
		expires := task.UpdatedAt.Add(24 * time.Hour)
		out.ExpiresAt = &expires
		out.Actions = []string{"cancel_draft"}
		if task.UnresolvedWrite || task.PendingAction != "" {
			out.Actions = append(out.Actions, "abandon")
		}
	case "active":
		out.Actions = []string{"stop", "start", "reimport"}
		if task.UnresolvedWrite || task.PendingAction != "" {
			out.Actions = append(out.Actions, "abandon")
		} else if task.PendingAction == "" {
			out.Actions = append(out.Actions, "release")
		}
	case "uncertain":
		out.Actions = []string{"reconcile", "reconfirm_submit", "abandon"}
	case "abandoned":
		out.Actions = []string{"reconcile", "reconfirm_submit"}
	case "cancelled":
		if !task.UnresolvedWrite && task.PendingAction == "" {
			out.Actions = []string{"release"}
		}
	case "missing", "conflict":
		out.Actions = []string{"reconcile"}
		if task.UnresolvedWrite || task.PendingAction != "" {
			out.Actions = append(out.Actions, "abandon")
		} else if task.PendingAction == "" {
			out.Actions = append(out.Actions, "release")
		}
	}

	return out
}

// btVisibleView treats video_id as a logical association, not a permanent
// playback grant. Reuse the library's current visibility query and path identity.
func btVisibleView(db *gorm.DB, task models.BTTask, files []models.BTFile) (BTTaskView, error) {
	view := btView(task, files)
	var connection models.BTConnection
	if db.Select("id", "slot", "enabled").First(&connection, "id = ?", task.ConnectionID).Error != nil {
		return BTTaskView{}, ErrBTTask
	}
	if connection.Slot != "local" {
		view.Actions = []string{}
		view.ReadOnlyReason = "connection_retired"
	} else if !connection.Enabled {
		// Disabling remote work must not remove the local exit path.
		view.Actions = []string{}
		if task.Stage != "released" && (task.Stage == "uncertain" || task.UnresolvedWrite || task.PendingAction != "") {
			view.Actions = append(view.Actions, "abandon")
		}
		if task.Stage == "draft" {
			view.Actions = append(view.Actions, "cancel_draft")
		}
	}
	ids := []uint{}
	for _, file := range view.Files {
		if file.VideoID != nil {
			ids = append(ids, *file.VideoID)
		}
	}
	if len(ids) == 0 {
		return view, nil
	}
	query, err := visibleVideoQuery(db.Model(&models.Video{}).Where("videos.id IN ?", ids))
	if err != nil {
		return BTTaskView{}, ErrBTTask
	}
	var videos []models.Video
	if query.Select("videos.id", "videos.path").Find(&videos).Error != nil {
		return BTTaskView{}, ErrBTTask
	}
	visible := map[uint]string{}
	for _, video := range videos {
		visible[video.ID] = filepath.Clean(video.Path)
	}
	for i, file := range view.Files {
		if file.VideoID != nil && visible[*file.VideoID] != filepath.Clean(filepath.Join(task.SaveRoot, filepath.FromSlash(file.Path))) {
			view.Files[i].VideoID = nil
		}
	}
	projectBTDuplicates(db, task, files, &view)
	return view, nil
}
func (s *BTService) Get(ctx context.Context, id string) (BTTaskView, error) {
	if !btTaskUUID(id) {
		return BTTaskView{}, ErrBTTask
	}
	var view BTTaskView
	err := btTaskOperation(ctx, func(db *gorm.DB) error {
		var task models.BTTask
		var files []models.BTFile
		if db.First(&task, "id = ?", id).Error != nil || db.Where("task_id = ?", id).Order("position ASC").Find(&files).Error != nil {
			return ErrBTTask
		}
		var err error
		view, err = btVisibleView(db, task, files)
		return err
	})
	return view, err
}
func (s *BTService) List(ctx context.Context, beforeID string, limit int) ([]BTTaskView, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	out := []BTTaskView{}
	err := btTaskOperation(ctx, func(db *gorm.DB) error {
		q := db.Model(&models.BTTask{})
		if beforeID != "" {
			if !btTaskUUID(beforeID) {
				return ErrBTTask
			}
			var cursor models.BTTask
			if err := db.First(&cursor, "id = ?", beforeID).Error; err != nil {
				return ErrBTTask
			}
			q = q.Where("created_at < ? OR (created_at = ? AND id < ?)", cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
		}
		var tasks []models.BTTask
		if q.Order("created_at DESC,id DESC").Limit(limit).Find(&tasks).Error != nil {
			return ErrBTTask
		}
		for _, task := range tasks {
			var files []models.BTFile
			if db.Where("task_id = ?", task.ID).Order("position ASC").Find(&files).Error != nil {
				return ErrBTTask
			}
			view, err := btVisibleView(db, task, files)
			if err != nil {
				return err
			}
			view.Files = nil
			out = append(out, view)
		}
		return nil
	})
	return out, err
}
func btSelectionDigest(ids []string) string {
	copyIDs := append([]string{}, ids...)
	sort.Strings(copyIDs)
	return btDigest([]byte(strings.Join(copyIDs, "\n")))
}
func (s *BTService) recoverTransient(ctx context.Context) error {
	stages := []string{"queued_metadata", "queued_add", "adding", "selecting", "priority_select_ready", "start_ready", "starting", "metadata_adding", "metadata_wait", "queued_stop", "queued_start", "cancel_pending", "add_sent", "metadata_sent", "priority_zero_sent", "priority_select_sent", "start_sent", "stop_sent", "cancel_stop_sent", "cancel_delete_sent"}
	for {
		var tasks []models.BTTask
		if err := btTaskOperation(ctx, func(db *gorm.DB) error {
			return db.Where("stage IN ?", stages).Order("id ASC").Limit(200).Find(&tasks).Error
		}); err != nil {
			return err
		}
		if len(tasks) == 0 {
			return nil
		}
		for _, task := range tasks {
			if err := s.updateTask(ctx, &task, map[string]any{"stage": "uncertain", "pending_action": btActionForStage(task.Stage), "unresolved_write": true, "error_code": "restart_reconcile"}); err != nil {
				return err
			}
		}
	}
}
func btIsNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
