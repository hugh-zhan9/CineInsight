package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

// BTImportVerifier is implemented by the download coordinator. Each call must
// fetch a fresh remote snapshot and verify ownership, manifest, selection, file
// completion and non-checking/non-moving state against these immutable rows.
// Nil success is not a cached progress promise. Import calls it before and after
// library mutation; local path locks do not exclude qBittorrent or Finder writes.
type BTImportVerifier interface {
	VerifyBTImport(context.Context, models.BTTask, models.BTFile) error
}

// BTImportVerifyFunc adapts a read-only coordinator verification function.
type BTImportVerifyFunc func(context.Context, models.BTTask, models.BTFile) error

func (f BTImportVerifyFunc) VerifyBTImport(ctx context.Context, t models.BTTask, file models.BTFile) error {
	return f(ctx, t, file)
}

// BTImportResult reports library registration separately from download completion.
type BTImportResult struct {
	VideoID   uint
	State     string
	ErrorCode string
}

// BTImporter performs one guarded, in-place import at a time per caller.
type BTImporter struct {
	videos   *VideoService
	policy   *BTPathPolicy
	verifier BTImportVerifier
}

// NewBTImporter requires a fresh remote verifier and the same library database policy.
func NewBTImporter(videos *VideoService, policy *BTPathPolicy, verifier BTImportVerifier) *BTImporter {
	return &BTImporter{videos: videos, policy: policy, verifier: verifier}
}

type btImportGrant struct {
	path, real                                           string
	size, mtime                                          int64
	taskID, fileID                                       string
	taskRevision, attemptGeneration, eligibilityRevision uint64
}

func (g *btImportGrant) check(path string) error {
	if filepath.Clean(path) != g.path {
		return ErrBTPathUnsafe
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != g.real {
		return ErrBTPathUnsafe
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != g.size || info.ModTime().UnixNano() != g.mtime {
		return ErrBTPending
	}
	return nil
}

// Import imports only one selected completed video, leaving all payload bytes in
// place. Already imported paths repair the association after a prior crash.
func (i *BTImporter) Import(ctx context.Context, taskID, fileID string) (BTImportResult, error) {
	result := BTImportResult{State: "failed"}
	if i == nil || i.videos == nil || i.policy == nil || i.verifier == nil {
		return result, ErrBTPending
	}
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return result, err
	}
	defer unlock()
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		policyDB, err := i.policy.db(ctx)
		if err != nil {
			return err
		}
		if policyDB == nil {
			return gorm.ErrInvalidDB
		}
		pinned, err := db.DB()
		if err != nil {
			return err
		}
		configured, err := policyDB.DB()
		if err != nil {
			return err
		}
		if pinned != configured {
			return ErrBTPending
		}
		result, err = i.importWithDatabase(ctx, db, taskID, fileID)
		return err
	})
	return result, err
}

func (i *BTImporter) importWithDatabase(ctx context.Context, db *gorm.DB, taskID, fileID string) (result BTImportResult, err error) {
	result = BTImportResult{State: "failed"}
	var isolateVideoID uint
	var revokeEligibility bool
	var task models.BTTask
	var file models.BTFile
	if err = db.First(&task, "id = ?", taskID).Error; err != nil {
		return result, err
	}
	if err = db.First(&file, "task_id = ? AND manifest_file_id = ?", taskID, fileID).Error; err != nil {
		return result, err
	}
	defer func() {
		if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, database.ErrMaintenance) {
			return
		}
		code := result.ErrorCode
		if code == "" {
			code = "import_failed"
		}
		result.ErrorCode = code
		_ = db.Transaction(func(tx *gorm.DB) error {
			guard := tx.Model(&models.BTTask{}).Where("id = ? AND revision = ? AND attempt_generation = ?", task.ID, task.Revision, task.AttemptGeneration).UpdateColumn("revision", task.Revision)
			if guard.Error != nil {
				return guard.Error
			}
			if guard.RowsAffected != 1 {
				return ErrBTPending
			}
			updates := map[string]any{"error_code": code}
			var current models.BTFile
			if e := tx.First(&current, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID).Error; e != nil {
				return e
			}
			if revokeEligibility || result.State == "blocked_user_delete" || current.ImportState != "imported" {
				state := "failed"
				if result.State == "blocked_user_delete" {
					state = result.State
				}
				updates["import_state"] = state
			}
			if revokeEligibility {
				updates["eligibility_revision"] = 0
			}
			changed := tx.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ? AND eligibility_revision = ?", task.ID, file.ManifestFileID, file.EligibilityRevision).Updates(updates)
			if changed.Error != nil {
				return changed.Error
			}
			if changed.RowsAffected != 1 {
				return ErrBTPending
			}
			// A stale importer must not hide a video published by a newer task
			// generation. Qualification revocation and isolation share this CAS.
			if isolateVideoID != 0 {
				return tx.Model(&models.Video{}).Where("id = ?", isolateVideoID).Updates(map[string]any{"is_stale": true, "stale_reason": models.StaleReasonReadError}).Error
			}
			return nil
		})
	}()
	if !file.Selected || !file.Selectable || file.Padding || file.Progress != 1 || file.EligibilityRevision == 0 || file.QBFileIndex == nil || *file.QBFileIndex < 0 || task.Stage != "active" || !btImportRemoteState(task.RemoteState) {
		return result, ErrBTPending
	}
	rel := filepath.FromSlash(file.RelativePath)
	if rel == "" || filepath.IsAbs(rel) || filepath.Clean(rel) != rel || strings.Contains(rel, "\\") {
		return result, ErrBTPathUnsafe
	}
	path := filepath.Join(task.SaveRoot, rel)
	if !btWithin(path, task.SaveRoot) {
		return result, ErrBTPathUnsafe
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !btWithin(real, task.CanonicalRoot) {
		return result, ErrBTPathUnsafe
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
		return result, ErrBTPending
	}
	if code, err := btImportScope(db, path); err != nil {
		result.ErrorCode = code
		return result, err
	}

	if err = i.verifier.VerifyBTImport(ctx, task, file); err != nil {
		return result, err
	}
	grant := &btImportGrant{path: filepath.Clean(path), real: real, size: info.Size(), mtime: info.ModTime().UnixNano(), taskID: task.ID, fileID: file.ManifestFileID, taskRevision: task.Revision, attemptGeneration: task.AttemptGeneration, eligibilityRevision: file.EligibilityRevision}
	unchanged := func() error {
		var currentTask models.BTTask
		var currentFile models.BTFile
		if err := db.First(&currentTask, "id = ?", task.ID).Error; err != nil {
			return err
		}
		if err := db.First(&currentFile, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID).Error; err != nil {
			return err
		}
		if currentTask.Revision != task.Revision || currentTask.AttemptGeneration != task.AttemptGeneration || currentFile.EligibilityRevision != file.EligibilityRevision || !currentFile.Selected || currentFile.Progress != 1 {
			return ErrBTPending
		}
		if _, err := btImportScope(db, path); err != nil {
			return err
		}
		return grant.check(path)
	}
	if err = unchanged(); err != nil {
		return result, err
	}
	video, _, addErr := i.videos.addScannedVideoWithBTGrant(path, grant)
	if errors.Is(addErr, ErrVideoBlockedByUserDelete) {
		result.State = "blocked_user_delete"
		result.ErrorCode = "user_deleted"
		return result, addErr
	}
	if addErr != nil && !errors.Is(addErr, ErrVideoExists) {
		return result, addErr
	}
	if video == nil {
		return result, ErrBTPending
	}
	err = i.verifier.VerifyBTImport(ctx, task, file)
	if err == nil {
		err = unchanged()
	}
	if err != nil {
		revokeEligibility = true
		if addErr == nil || grant.check(path) != nil {
			isolateVideoID = video.ID
		}
		result.ErrorCode = "external_changed"
		return result, err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		// CAS guard both the task generation and the eligibility used for this import.
		guard := tx.Model(&models.BTTask{}).Where("id = ? AND revision = ? AND attempt_generation = ?", task.ID, task.Revision, task.AttemptGeneration).UpdateColumn("revision", task.Revision)
		if guard.Error != nil {
			return guard.Error
		}
		if guard.RowsAffected != 1 {
			return ErrBTPending
		}
		update := tx.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ? AND eligibility_revision = ? AND selected = ?", task.ID, file.ManifestFileID, file.EligibilityRevision, true).Updates(map[string]any{"import_state": "imported", "video_id": video.ID, "observed_size": grant.size, "observed_mtime": grant.mtime, "error_code": ""})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrBTPending
		}
		var publishing models.BTFile
		if e := tx.First(&publishing, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID).Error; e != nil {
			return e
		}
		if publishing.PublicationState == BTImportPublicationPendingRestore {
			if e := rebuildSubtitleIndexTx(tx, *video); e != nil {
				return e
			}
		}
		release := tx.Model(&models.Video{}).Where("id = ? AND path = ?", video.ID, path).Updates(map[string]any{"is_stale": false, "stale_reason": ""})
		if release.Error != nil {
			return release.Error
		}
		if release.RowsAffected != 1 {
			return ErrBTPending
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if err = i.publishVerified(ctx, db, task, file, video.ID); err != nil {
		return result, err
	}
	return BTImportResult{State: "imported", VideoID: video.ID}, nil
}

func btImportRemoteState(state string) bool {
	switch state {
	case "downloading", "stalledDL", "queuedDL", "stoppedDL", "forcedDL", "uploading", "stalledUP", "queuedUP", "stoppedUP", "forcedUP":
		return true
	}
	return false
}

func btImportScope(db *gorm.DB, path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "path_not_in_library", ErrBTPathUnsafe
	}
	settings, allowed, err := btAuthorizedScanPath(db, path, real)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "path_not_in_library", ErrBTPending
	}
	extensions := settings.VideoExtensions
	if extensions == "" {
		extensions = defaultVideoExtensions
	}
	for _, ext := range strings.Split(extensions, ",") {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		if strings.EqualFold(filepath.Ext(path), ext) && !isKnownNonVideoSourcePath(path) {
			return "", nil
		}
	}
	return "not_video", ErrBTPending
}

const (
	BTImportPublicationPendingNew     = "pending_new"
	BTImportPublicationPendingRestore = "pending_restore"
	BTImportPublicationDone           = "done"
)

// recordProvisional participates in the same transaction as video creation or
// restoration, so a process exit cannot lose the first-publication obligation.
func (g *btImportGrant) recordProvisional(tx *gorm.DB, videoID uint, kind string) error {
	guard := tx.Model(&models.BTTask{}).Where("id = ? AND revision = ? AND attempt_generation = ?", g.taskID, g.taskRevision, g.attemptGeneration).UpdateColumn("revision", g.taskRevision)
	if guard.Error != nil {
		return guard.Error
	}
	if guard.RowsAffected != 1 {
		return ErrBTPending
	}
	var file models.BTFile
	if err := tx.Where("task_id = ? AND manifest_file_id = ? AND eligibility_revision = ? AND selected = ?", g.taskID, g.fileID, g.eligibilityRevision, true).First(&file).Error; err != nil {
		return err
	}
	if file.PublicationState == BTImportPublicationPendingNew {
		kind = BTImportPublicationPendingNew
	}
	update := tx.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ? AND eligibility_revision = ? AND selected = ?", g.taskID, g.fileID, g.eligibilityRevision, true).Updates(map[string]any{"video_id": videoID, "publication_state": kind})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return ErrBTPending
	}
	return nil
}

// publishVerified may replay after an exit between the callback and its receipt.
// Only new BT records trigger new-video rules; restores use existing-record hooks.
// P002 must call Import again for imported files with either pending state after
// reconnect, even though the file is already associated with a library record.
func (i *BTImporter) publishVerified(ctx context.Context, db *gorm.DB, task models.BTTask, file models.BTFile, videoID uint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var current models.BTFile
	if err := db.First(&current, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID).Error; err != nil {
		return err
	}
	state := current.PublicationState
	if state != BTImportPublicationPendingNew && state != BTImportPublicationPendingRestore {
		return nil
	}
	if current.ImportState != "imported" || current.VideoID == nil || *current.VideoID != videoID || current.EligibilityRevision != file.EligibilityRevision {
		return ErrBTPending
	}
	var video models.Video
	if err := db.First(&video, videoID).Error; err != nil {
		return err
	}
	if video.IsStale {
		return ErrBTPending
	}
	if state == BTImportPublicationPendingNew {
		i.videos.observeLocalMetadata(videoID, true)
		i.videos.notifyNewVideo(videoID)
	} else {
		i.videos.observeLocalMetadata(videoID, false)
		notifyLibraryMatchInputsChanged("bt-import-restore")
	}
	done := db.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ? AND publication_state = ? AND video_id = ? AND import_state = ? AND eligibility_revision = ?", task.ID, file.ManifestFileID, state, videoID, "imported", file.EligibilityRevision).Update("publication_state", BTImportPublicationDone)
	if done.Error != nil {
		return done.Error
	}
	if done.RowsAffected != 1 {
		return ErrBTPending
	}
	return nil
}
