package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
)

const editReplacementManifest = "replace-manifest.json"

var errReplacementNoReplace = errors.New("replacement exclusive rename unavailable")

type replacementQuarantine struct {
	Name     string `json:"name"`
	Identity string `json:"identity"`
}
type editReplacementJournal struct {
	SourceMode      uint32                  `json:"source_mode"`
	Quarantines     []replacementQuarantine `json:"quarantines,omitempty"`
	ItemID          uint                    `json:"item_id"`
	Source          editPlanSource          `json:"source"`
	SourceUpdatedAt time.Time               `json:"source_updated_at"`
	OldSHA          string                  `json:"old_sha"`
	NewSHA          string                  `json:"new_sha"`
	OldIdentity     string                  `json:"old_identity"`
	TargetIdentity  string                  `json:"target_identity"`
	Phase           string                  `json:"phase"`
}

func replacementPending() error {
	return editError("replace_recovery_pending", "原片替换尚待恢复，请保持磁盘连接后重试恢复")
}
func replacementJournalExists(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Lstat(filepath.Join(dir, editReplacementManifest))
	return !errors.Is(err, os.ErrNotExist)
}
func replacementSHA(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", replacementPending()
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", replacementPending()
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return "", replacementPending()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func replacementIdentity(path string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return stableFileIdentity(info)
}
func replacementHasSHA(path, sha string) bool {
	actual, err := replacementSHA(path)
	return err == nil && sha != "" && actual == sha
}
func replacementSafeSource(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return editError("replace_path_unsafe", "来源路径无效")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return editError("replace_path_unsafe", "替换来源不能经过符号链接")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	snap, err := NewBTPathPolicy(nil).Snapshot(context.Background())
	if err != nil {
		return err
	}
	managed, err := snap.hasMarker(path)
	if err != nil {
		return err
	}
	if managed {
		return editError("replace_managed_source", "托管下载文件不能原位替换")
	}
	return nil
}
func writeReplacementJournal(dir string, j *editReplacementJournal) error {
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "replace-manifest.tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(dir, editReplacementManifest)); err != nil {
		return err
	}
	return syncSubtitleParentDirectory(dir)
}
func readReplacementJournal(item models.VideoEditItem, plan editPlan) (*editReplacementJournal, error) {
	dir := editWorkdirFor(plan, item.ID)
	if (item.WorkDir != "" && item.WorkDir != dir) || len(plan.Sources) != 1 || plan.Kind != "crop" || plan.PublishMode != "replace_source" {
		return nil, replacementPending()
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, replacementPending()
	}
	manifestInfo, err := os.Lstat(filepath.Join(dir, editReplacementManifest))
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > 16384 {
		return nil, replacementPending()
	}
	f, err := os.Open(filepath.Join(dir, editReplacementManifest))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var j editReplacementJournal
	if err = json.NewDecoder(io.LimitReader(f, 16384)).Decode(&j); err != nil {
		return nil, replacementPending()
	}
	if j.ItemID != item.ID || j.Source.Path != plan.Sources[0].Path || j.Source.VideoID != plan.Sources[0].VideoID || len(j.OldSHA) != 64 || len(j.NewSHA) != 64 || j.OldIdentity == "" {
		return nil, replacementPending()
	}
	if err = replacementSafeSourceParent(j.Source.Path); err != nil {
		return nil, err
	}
	return &j, nil
}
func replacementSafeSourceParent(path string) error {
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		i, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return replacementPending()
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

// editReplacementPathPending is a projection of persisted item journals, never a second authority.
func editReplacementPathPending(db *gorm.DB, path string) bool {
	pending, err := editReplacementPathPendingError(db, path)
	return pending || err != nil
}

func editReplacementPathPendingError(db *gorm.DB, path string) (bool, error) {
	if db == nil {
		return false, errors.New("replacement database unavailable")
	}
	var items []models.VideoEditItem
	if err := db.Where("work_dir <> '' OR plan_json LIKE ?", "%replace_source%").Find(&items).Error; err != nil {
		return false, err
	}
	for _, item := range items {
		var p editPlan
		if json.Unmarshal([]byte(item.PlanJSON), &p) != nil {
			continue
		}
		if p.PublishMode != "replace_source" {
			continue
		}
		for _, src := range p.Sources {
			if filepath.Clean(src.Path) == filepath.Clean(path) && (item.ErrorCode == "replace_recovery_pending" || replacementJournalExists(replacementWorkDir(item))) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *VideoEditService) replacementCheckpoint(phase string) error {
	if s.replacementHook != nil {
		return s.replacementHook(phase)
	}
	return nil
}
func (s *VideoEditService) publishEditReplacement(ctx context.Context, item models.VideoEditItem, plan editPlan, staging string) error {
	release, err := lockLibraryPaths()
	if err != nil {
		return err
	}
	defer release()
	ctx = context.WithoutCancel(ctx)
	if len(plan.Sources) != 1 || plan.Kind != "crop" {
		return editError("recipe_invalid", "原片替换来源无效")
	}
	source := plan.Sources[0]
	if err = replacementSafeSource(source.Path); err != nil {
		return err
	}
	if err = editSourcesUnchanged(plan); err != nil {
		return err
	}
	if !replacementHasSHA(source.Path, source.SHA256) {
		return editError("source_changed", "来源内容已变化")
	}
	var video models.Video
	db, err := editDB(ctx)
	if err != nil {
		return err
	}
	if err = db.First(&video, source.VideoID).Error; err != nil {
		return err
	}
	if video.Path != source.Path || video.Size != source.RecordSize || !video.UpdatedAt.Equal(source.UpdatedAt) {
		return editError("source_changed", "片库记录已变化")
	}
	if err = s.syncPublishFile(staging); err != nil {
		return err
	}
	out, err := os.Stat(staging)
	if err != nil {
		return err
	}
	free, err := s.diskFree(plan.OutputDir)
	if err != nil || free < uint64(out.Size()) {
		return editError("disk_full", "替换需要额外一份完整成品空间")
	}
	sourceInfo, err := os.Stat(source.Path)
	if err != nil {
		return err
	}
	if err = os.Chmod(staging, sourceInfo.Mode().Perm()); err != nil {
		return err
	}
	sha, err := replacementSHA(staging)
	if err != nil {
		return err
	}
	dir := editWorkdirFor(plan, item.ID)
	j := &editReplacementJournal{ItemID: item.ID, Source: source, SourceUpdatedAt: video.UpdatedAt, OldSHA: source.SHA256, NewSHA: sha, OldIdentity: replacementIdentity(source.Path), SourceMode: uint32(sourceInfo.Mode().Perm()), Phase: "prepared"}
	if j.OldIdentity == "" {
		return editError("replace_path_unsafe", "无法确认来源文件身份")
	}
	if err = writeReplacementJournal(dir, j); err != nil {
		return err
	}
	fail := func(e error) error {
		if r := s.rollbackReplacement(dir, j); r != nil {
			return replacementPending()
		}
		if r := cleanupRestoredReplacement(dir, j); r != nil {
			return replacementPending()
		}
		return e
	}
	if err = s.replacementCheckpoint("prepared"); err != nil {
		return replacementPending()
	}
	backup := filepath.Join(dir, "original.backup")
	if _, err = os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return replacementPending()
	}
	if err = s.renameReplacementChecked(source.Path, backup); err != nil {
		if !replacementRenameUnsupported(err) {
			return fail(err)
		}
		// Only this application-owned, empty destination may use ordinary rename.
		if err = os.Rename(source.Path, backup); err != nil {
			return fail(err)
		}
	}
	if err = syncSubtitleParentDirectory(dir); err != nil {
		return replacementPending()
	}
	if err = s.replacementCheckpoint("backup_directory_synced"); err != nil {
		return replacementPending()
	}
	if err = syncSubtitleParentDirectory(plan.OutputDir); err != nil {
		return replacementPending()
	}
	if replacementIdentity(backup) != j.OldIdentity || !replacementHasSHA(backup, j.OldSHA) {
		return replacementPending()
	}
	if err = s.replacementCheckpoint("backed_up"); err != nil {
		return replacementPending()
	}
	if err = s.installReplacement(dir, j, staging, "copying", j.NewSHA); err != nil {
		return fail(err)
	}
	if err = s.replacementCheckpoint("installed"); err != nil {
		return replacementPending()
	}
	if err = s.commitReplacement(ctx, item, plan, j); err != nil {
		// A lost transaction response must be resolved before touching either file.
		current, e := loadEditItemByID(ctx, item.ID)
		if e != nil {
			return replacementPending()
		}
		if current.Status != models.VideoEditStatusCompleted {
			return fail(err)
		}
	}
	if err = s.replacementCheckpoint("committed"); err != nil {
		return replacementPending()
	}
	return s.finishReplacement(ctx, item, plan, j)
}

func (s *VideoEditService) installReplacement(dir string, j *editReplacementJournal, from, phase, sha string) error {
	j.Phase, j.TargetIdentity = phase, ""
	if err := writeReplacementJournal(dir, j); err != nil {
		return err
	}
	if err := s.renameReplacementChecked(from, j.Source.Path); err == nil {
		j.TargetIdentity = replacementIdentity(j.Source.Path)
		if err = writeReplacementJournal(dir, j); err != nil {
			return err
		}
	} else {
		if !replacementRenameUnsupported(err) {
			return err
		}
		in, err := os.Open(from)
		if err != nil {
			return err
		}
		defer in.Close()
		f, err := os.OpenFile(j.Source.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err == nil {
			j.TargetIdentity = stableFileIdentity(info)
		}
		if err == nil && j.TargetIdentity == "" {
			err = replacementPending()
		}
		if err == nil {
			err = writeReplacementJournal(dir, j)
		}
		if err == nil {
			err = s.replacementCheckpoint(phase + "_created")
		}
		if err == nil {
			_, err = io.Copy(f, in)
		}
		if err == nil {
			err = f.Sync()
		}
		ce := f.Close()
		if err == nil {
			err = ce
		}
		if err != nil {
			return err
		}
	}
	if phase == "copying" {
		if err := os.Chmod(j.Source.Path, os.FileMode(j.SourceMode)); err != nil {
			return err
		}
		if err := fsyncEnhancementFile(j.Source.Path); err != nil {
			return err
		}
	}
	if err := syncSubtitleParentDirectory(filepath.Dir(j.Source.Path)); err != nil {
		return err
	}
	if !replacementHasSHA(j.Source.Path, sha) {
		return replacementPending()
	}
	return nil
}
func (s *VideoEditService) rollbackReplacement(dir string, j *editReplacementJournal) error {
	backup := filepath.Join(dir, "original.backup")
	if replacementHasSHA(j.Source.Path, j.OldSHA) {
		if j.Phase == "restoring" && j.TargetIdentity != "" && replacementIdentity(j.Source.Path) == j.TargetIdentity {
			return restoreReplacementMetadata(j.Source.Path, filepath.Join(dir, "original.backup"), j.Source.ModTimeNS)
		}
		return nil
	}
	if !replacementHasSHA(backup, j.OldSHA) || replacementIdentity(backup) != j.OldIdentity {
		return replacementPending()
	}
	if _, err := os.Lstat(j.Source.Path); err == nil {
		if j.TargetIdentity == "" || replacementIdentity(j.Source.Path) != j.TargetIdentity {
			return replacementPending()
		}
		qdir, e := os.MkdirTemp(dir, "quarantine-")
		if e != nil {
			return e
		}
		rel := filepath.Join(filepath.Base(qdir), "payload")
		j.Quarantines = append(j.Quarantines, replacementQuarantine{Name: rel, Identity: j.TargetIdentity})
		if e = writeReplacementJournal(dir, j); e != nil {
			return e
		}
		quarantine := filepath.Join(dir, rel)
		if e = s.renameReplacementChecked(j.Source.Path, quarantine); e != nil {
			if !replacementRenameUnsupported(e) {
				return e
			}
			if e = os.Rename(j.Source.Path, quarantine); e != nil {
				return e
			}
		}
		if replacementIdentity(quarantine) != j.TargetIdentity {
			return replacementPending()
		}
		if e = syncSubtitleParentDirectory(qdir); e != nil {
			return e
		}
		if e = syncSubtitleParentDirectory(filepath.Dir(j.Source.Path)); e != nil {
			return e
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.renameReplacementChecked(backup, j.Source.Path); err == nil {
		if !replacementHasSHA(j.Source.Path, j.OldSHA) || replacementIdentity(j.Source.Path) != j.OldIdentity {
			return replacementPending()
		}
		if err = syncSubtitleParentDirectory(filepath.Dir(j.Source.Path)); err != nil {
			return err
		}
		return syncSubtitleParentDirectory(dir)
	} else if !replacementRenameUnsupported(err) {
		return err
	}
	// Copy restore keeps the unique old backup until bytes and metadata are durable.
	j.Phase, j.TargetIdentity = "restoring", ""
	if err := writeReplacementJournal(dir, j); err != nil {
		return err
	}
	in, err := os.Open(backup)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := os.OpenFile(j.Source.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err == nil {
		j.TargetIdentity = stableFileIdentity(info)
	}
	if err == nil {
		err = writeReplacementJournal(dir, j)
	}
	if err == nil {
		err = s.replacementCheckpoint("restoring_created")
	}
	if err == nil {
		_, err = io.Copy(f, in)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if !replacementHasSHA(j.Source.Path, j.OldSHA) {
		return replacementPending()
	}
	if err = restoreReplacementMetadata(j.Source.Path, backup, j.Source.ModTimeNS); err != nil {
		return err
	}
	return syncSubtitleParentDirectory(filepath.Dir(j.Source.Path))
}

func (s *VideoEditService) commitReplacement(ctx context.Context, item models.VideoEditItem, plan editPlan, j *editReplacementJournal) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.publishTimeout)
	defer cancel()
	info, err := os.Stat(j.Source.Path)
	if err != nil {
		return err
	}
	now := s.now()
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		result := tx.Model(&models.Video{}).Where("id = ? AND path = ? AND size = ? AND updated_at = ?", j.Source.VideoID, j.Source.Path, j.Source.RecordSize, j.SourceUpdatedAt).Updates(map[string]any{"size": info.Size(), "width": plan.Video.Width, "height": plan.Video.Height, "resolution": fmt.Sprintf("%dx%d", plan.Video.Width, plan.Video.Height), "duration": float64(plan.DurationMS) / 1000, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return editError("source_changed", "片库记录变化，未提交替换")
		}
		if err := invalidateReplacementDerived(tx, j.Source.VideoID); err != nil {
			return err
		}
		if s.publishTxHook != nil {
			if err := s.publishTxHook(tx); err != nil {
				return err
			}
		}
		result = tx.Model(&models.VideoEditItem{}).Where("id = ? AND status = ? AND phase = ?", item.ID, models.VideoEditStatusRunning, models.VideoEditPhasePublish).Updates(map[string]any{"status": models.VideoEditStatusCompleted, "phase": models.VideoEditPhaseDone, "progress": 1.0, "output_video_id": j.Source.VideoID, "output_path": j.Source.Path, "output_name": filepath.Base(j.Source.Path), "finished_at": now, "error_code": "", "error_message": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return editError("edit_project_conflict", "替换项状态变化")
		}
		return syncShortVideoTagForVideo(tx, j.Source.VideoID)
	})
	if s.replacementCommitReply != nil {
		return s.replacementCommitReply(err)
	}
	return err
}

func invalidateReplacementDerived(tx *gorm.DB, id uint) error {
	for _, model := range []any{&models.VideoTechnicalMetadata{}, &models.MediaStream{}, &models.VideoPerceptualHash{}, &models.VideoFrameHashSequence{}, &models.SceneVisualSegment{}, &models.SceneIndexState{}, &models.SubtitleIndexState{}, &models.SubtitleSegment{}, &models.VideoPlaybackProxy{}} {
		if err := tx.Where("video_id = ?", id).Delete(model).Error; err != nil {
			return err
		}
	}
	// Semantic metadata is installed separately by PrepareSemanticVectorStorage,
	// including on SQLite without vector search. Absence is legal; SQL failures are not.
	for _, model := range []any{&models.VideoSemanticIndex{}, &models.SemanticIndexAttempt{}} {
		statement := &gorm.Statement{DB: tx}
		if err := statement.Parse(model); err != nil {
			return err
		}
		present, err := replacementOptionalTableExists(tx, statement.Schema.Table)
		if err != nil {
			return err
		}
		if present {
			if err = tx.Where("video_id = ?", id).Delete(model).Error; err != nil {
				return err
			}
		}
	}
	// Search ranks the raw PG vectors directly, so deleting metadata alone would
	// leave the old crop searchable. This table has no GORM model and is optional.
	if tx.Dialector.Name() == "postgres" {
		present, err := replacementOptionalTableExists(tx, "video_semantic_vectors")
		if err != nil {
			return err
		}
		if present {
			if err = tx.Exec("DELETE FROM video_semantic_vectors WHERE video_id = ?", id).Error; err != nil {
				return err
			}
		}
	}
	var stale []models.FaceObservation
	if err := tx.Where("media_kind = ? AND media_id = ?", models.FaceMediaKindVideo, id).Find(&stale).Error; err != nil {
		return err
	}
	if err := tx.Where("media_kind = ? AND media_id = ?", models.FaceMediaKindVideo, id).Delete(&models.FaceObservation{}).Error; err != nil {
		return err
	}
	_, err := recomputeFaceClustersTx(tx.Statement.Context, tx, faceClusterIDsOf(stale))
	return err
}
func (s *VideoEditService) finishReplacement(ctx context.Context, item models.VideoEditItem, plan editPlan, j *editReplacementJournal) error {
	if err := s.replacementCheckpoint("cleanup"); err != nil {
		return replacementPending()
	}
	dir := editWorkdirFor(plan, item.ID)
	if err := validateReplacementQuarantines(dir, j); err != nil {
		return err
	}
	if err := invalidateReplacementThumbnails(s.dataDir, j.Source.VideoID); err != nil {
		return replacementPending()
	}
	if !replacementHasSHA(j.Source.Path, j.NewSHA) {
		return replacementPending()
	}
	if err := s.clearReplacementRecoveryFields(ctx, item.ID, models.VideoEditStatusCompleted); err != nil {
		return err
	}
	if err := s.replacementCheckpoint("cleanup_db_cleared"); err != nil {
		return replacementPending()
	}
	backup := filepath.Join(dir, "original.backup")
	if _, err := os.Lstat(backup); err == nil {
		if replacementIdentity(backup) != j.OldIdentity || !replacementHasSHA(backup, j.OldSHA) {
			return replacementPending()
		}
		if err = os.Remove(backup); err != nil {
			return replacementPending()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(filepath.Join(dir, editReplacementManifest)); err != nil {
		return replacementPending()
	}
	removeEditWorkdir(dir)
	return nil
}

// RetryReplacementRecovery retries only file recovery; it never rebuilds or re-encodes a work directory.
func (s *VideoEditService) RetryReplacementRecovery(ctx context.Context, itemID uint) (view *EditProjectView, returnedErr error) {
	release, err := lockLibraryPaths()
	if err != nil {
		return nil, err
	}
	defer release()
	ctx = context.WithoutCancel(ctx)
	item, err := loadEditItemByID(ctx, itemID)
	if err != nil {
		return nil, err
	}
	var plan editPlan
	if json.Unmarshal([]byte(item.PlanJSON), &plan) != nil || plan.PublishMode != "replace_source" || plan.Kind != "crop" || len(plan.Sources) != 1 {
		return nil, editError("edit_project_conflict", "此项不属于原文件替换恢复")
	}
	dir := editWorkdirFor(plan, item.ID)
	if !replacementJournalExists(dir) {
		// Duplicate requests after a confirmed cleanup are read-only and idempotent.
		if item.WorkDir == "" && item.ErrorCode == "" && (item.Status == models.VideoEditStatusCompleted || item.Status == models.VideoEditStatusInterrupted) {
			return s.GetProject(ctx, item.ProjectID)
		}
		return nil, editError("edit_project_conflict", "没有待恢复的原文件替换日志")
	}
	item.WorkDir = replacementWorkDir(item)
	j, err := readReplacementJournal(item, plan)
	if err != nil {
		return nil, err
	}
	// Mark only a validated journal still owned by this item; absent/foreign evidence must not create work.
	defer func() {
		if returnedErr != nil && replacementJournalExists(dir) {
			_ = s.markReplacementPending(ctx, itemID)
		}
	}()
	if item.Status == models.VideoEditStatusCompleted {
		err = s.finishReplacement(ctx, item, plan, j)
	} else if replacementHasSHA(j.Source.Path, j.NewSHA) && replacementHasSHA(filepath.Join(item.WorkDir, "original.backup"), j.OldSHA) {
		// Recovery owns only this old running/publish item; preserve completed facts.
		db, e := editDB(ctx)
		if e != nil {
			return nil, e
		}
		r := db.Model(&models.VideoEditItem{}).Where("id = ? AND status = ?", item.ID, item.Status).Updates(map[string]any{"status": models.VideoEditStatusRunning, "phase": models.VideoEditPhasePublish})
		err = r.Error
		if err == nil && r.RowsAffected != 1 {
			return nil, replacementPending()
		}
		if err == nil {
			err = s.commitReplacement(ctx, item, plan, j)
		}
		if err != nil {
			// Do not restore after an ambiguous commit response until the persisted fact is readable.
			fact, readErr := loadEditItemByID(ctx, item.ID)
			if readErr != nil {
				return nil, replacementPending()
			}
			if fact.Status == models.VideoEditStatusCompleted {
				err = nil
			} else {
				err = s.rollbackReplacement(item.WorkDir, j)
			}
		}
		fact, readErr := loadEditItemByID(ctx, item.ID)
		if readErr != nil {
			return nil, replacementPending()
		}
		if fact.Status == models.VideoEditStatusCompleted {
			err = s.finishReplacement(ctx, item, plan, j)
		}
	} else {
		err = s.rollbackReplacement(item.WorkDir, j)
	}
	if err != nil {
		return nil, replacementPending()
	}
	current, e := loadEditItemByID(ctx, item.ID)
	if e != nil {
		return nil, e
	}
	if current.Status != models.VideoEditStatusCompleted {
		if err = s.clearReplacementRecoveryFields(ctx, item.ID, current.Status); err != nil {
			return nil, err
		}
		if err = s.replacementCheckpoint("cleanup_db_cleared"); err != nil {
			return nil, replacementPending()
		}
		if err = cleanupRestoredReplacement(item.WorkDir, j); err != nil {
			_ = s.markReplacementPending(ctx, item.ID)
			return nil, replacementPending()
		}
	}
	s.recoverProjectStatus(ctx, item.ProjectID)
	s.emitItemByID(ctx, item.ID)
	return s.GetProject(ctx, item.ProjectID)
}
func (s *VideoEditService) recoverReplacements(ctx context.Context) {
	db, err := editDB(ctx)
	if err != nil {
		return
	}
	var items []models.VideoEditItem
	if db.Where("work_dir <> '' OR plan_json LIKE ?", "%replace_source%").Find(&items).Error != nil {
		return
	}
	for _, item := range items {
		if !replacementJournalExists(replacementWorkDir(item)) {
			continue
		}
		if _, err = s.RetryReplacementRecovery(ctx, item.ID); err != nil {
			_ = s.markReplacementPending(ctx, item.ID)
		}
	}
}

func cleanupRestoredReplacement(dir string, j *editReplacementJournal) error {
	if err := validateReplacementQuarantines(dir, j); err != nil {
		return err
	}
	if !replacementHasSHA(j.Source.Path, j.OldSHA) {
		return replacementPending()
	}
	backup := filepath.Join(dir, "original.backup")
	if _, e := os.Lstat(backup); e == nil {
		if replacementIdentity(backup) != j.OldIdentity || !replacementHasSHA(backup, j.OldSHA) {
			return replacementPending()
		}
		if e = os.Remove(backup); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e := os.Remove(filepath.Join(dir, editReplacementManifest)); e != nil {
		return e
	}
	removeEditWorkdir(dir)
	return nil
}

func replacementVideoIDPending(db *gorm.DB, id uint) bool {
	var v models.Video
	if e := db.Unscoped().First(&v, id).Error; e != nil {
		return false
	}
	return editReplacementPathPending(db, v.Path)
}

func (s *VideoEditService) markReplacementPending(ctx context.Context, id uint) error {
	db, err := editDB(ctx)
	if err != nil {
		return err
	}
	item, err := loadEditItemByID(ctx, id)
	if err != nil {
		return err
	}
	var plan editPlan
	if json.Unmarshal([]byte(item.PlanJSON), &plan) != nil || plan.PublishMode != "replace_source" || plan.Kind != "crop" || len(plan.Sources) != 1 {
		return editError("edit_project_conflict", "此项不属于原文件替换恢复")
	}
	if _, err = readReplacementJournal(item, plan); err != nil {
		return err
	}
	updates := map[string]any{"error_code": "replace_recovery_pending", "error_message": "磁盘恢复后重试恢复", "work_dir": replacementWorkDir(item)}
	if item.Status != models.VideoEditStatusCompleted {
		updates["status"] = models.VideoEditStatusInterrupted
	}
	r := db.Model(&models.VideoEditItem{}).Where("id = ? AND status = ?", id, item.Status).Updates(updates)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return replacementPending()
	}
	return nil
}

func restoreReplacementMetadata(path, backup string, mtime int64) error {
	info, err := os.Stat(backup)
	if err != nil {
		return err
	}
	if err = os.Chmod(path, info.Mode().Perm()); err != nil {
		return err
	}
	at := time.Unix(0, mtime)
	if err = os.Chtimes(path, at, at); err != nil {
		return err
	}
	return fsyncEnhancementFile(path)
}

func validateReplacementQuarantines(dir string, j *editReplacementJournal) error {
	for _, q := range j.Quarantines {
		if filepath.Clean(q.Name) != q.Name || filepath.IsAbs(q.Name) || filepath.Base(q.Name) != "payload" || !strings.HasPrefix(filepath.Base(filepath.Dir(q.Name)), "quarantine-") || filepath.Dir(filepath.Dir(q.Name)) != "." {
			return replacementPending()
		}
		parent := filepath.Join(dir, filepath.Dir(q.Name))
		i, e := os.Lstat(parent)
		if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return replacementPending()
		}
		target := filepath.Join(dir, q.Name)
		if _, e = os.Lstat(target); e == nil {
			if q.Identity == "" || replacementIdentity(target) != q.Identity {
				return replacementPending()
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	return nil
}

// replacementWorkDir derives the existing item's journal location, including the DB-clear/FS-cleanup window.
func replacementWorkDir(item models.VideoEditItem) string {
	if item.WorkDir != "" {
		return item.WorkDir
	}
	var plan editPlan
	if json.Unmarshal([]byte(item.PlanJSON), &plan) != nil || plan.PublishMode != "replace_source" {
		return ""
	}
	return editWorkdirFor(plan, item.ID)
}
func (s *VideoEditService) clearReplacementRecoveryFields(ctx context.Context, id uint, expected string) error {
	if err := s.replacementCheckpoint("cleanup_db"); err != nil {
		return err
	}
	db, err := editDB(ctx)
	if err != nil {
		return err
	}
	updates := map[string]any{"work_dir": "", "publish_target": "", "staged_size": 0, "error_code": "", "error_message": ""}
	if expected != models.VideoEditStatusCompleted {
		updates["status"] = models.VideoEditStatusInterrupted
	}
	r := db.Model(&models.VideoEditItem{}).Where("id = ? AND status = ?", id, expected).Updates(updates)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return replacementPending()
	}
	return nil
}

// guardReplacementProject serializes only the journal check, never media-slot preflight under a path read lock.
func (s *VideoEditService) guardReplacementProject(ctx context.Context, id uint) error {
	release, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	items, err := loadEditItems(ctx, id)
	if err != nil {
		return err
	}
	for _, item := range items {
		if replacementJournalExists(replacementWorkDir(item)) {
			return replacementPending()
		}
	}
	return nil
}

// renameReplacementChecked preserves ordinary errors. EINVAL is a capability result only
// after the identical primitive rejects valid arguments on a private same-volume probe.
func (s *VideoEditService) renameReplacementChecked(from, to string) error {
	rename := s.replacementRename
	if rename == nil {
		rename = replacementNativeRename
	}
	original := rename(from, to)
	if original == nil || replacementRenameUnsupported(original) || !replacementInvalidRenameArgument(original) {
		return original
	}
	if !filepath.IsAbs(from) || !filepath.IsAbs(to) || filepath.Clean(from) != from || filepath.Clean(to) != to || from == to {
		return original
	}
	source, err := os.Lstat(from)
	if err != nil || !source.Mode().IsRegular() {
		return original
	}
	parent := filepath.Dir(to)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || !replacementSameVolume(source, parentInfo) {
		return original
	}
	if _, err = os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
		return original
	}
	unsupported, cleanupErr := probeReplacementRename(parent, parentInfo, rename)
	if cleanupErr != nil {
		return &replacementRenameProbeError{original: original, detail: cleanupErr}
	}
	if unsupported {
		return errors.Join(errReplacementNoReplace, original)
	}
	return original
}

// probeReplacementRename never calls the checked wrapper, so probes cannot recurse.
// Cleanup removes only this probe's known inode and empty directory, never a tree.
func probeReplacementRename(parent string, parentInfo os.FileInfo, rename func(string, string) error) (unsupported bool, resultErr error) {
	dir, err := os.MkdirTemp(parent, ".cineinsight-rename-probe-")
	if err != nil {
		return false, err
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return false, err
	}
	from, to := filepath.Join(dir, "source"), filepath.Join(dir, "destination")
	var owned os.FileInfo
	defer func() {
		current, e := os.Lstat(dir)
		if e != nil || !current.IsDir() || !os.SameFile(current, dirInfo) {
			resultErr = errors.Join(resultErr, replacementPending())
			unsupported = false
			return
		}
		for _, path := range []string{from, to} {
			info, e := os.Lstat(path)
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e != nil {
				resultErr = errors.Join(resultErr, e)
				continue
			}
			if owned == nil || !info.Mode().IsRegular() || !os.SameFile(info, owned) {
				resultErr = errors.Join(resultErr, replacementPending())
				continue
			}
			if e = os.Remove(path); e != nil {
				resultErr = errors.Join(resultErr, e)
			}
		}
		if e = os.Remove(dir); e != nil {
			resultErr = errors.Join(resultErr, e)
		}
		if resultErr != nil {
			unsupported = false
		}
	}()
	file, err := os.OpenFile(from, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	owned, err = file.Stat()
	if err == nil && !replacementSameVolume(owned, parentInfo) {
		err = replacementPending()
	}
	if err == nil {
		_, err = file.Write([]byte("rename capability probe"))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	if _, err = os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
		return false, replacementPending()
	}
	probeErr := rename(from, to)
	if probeErr == nil {
		return false, nil
	}
	if !replacementInvalidRenameArgument(probeErr) && !replacementRenameUnsupported(probeErr) {
		return false, nil
	}
	// Unsupported must leave the probe source untouched and the destination absent.
	remaining, err := os.Lstat(from)
	if err != nil || !remaining.Mode().IsRegular() || !os.SameFile(remaining, owned) {
		return false, replacementPending()
	}
	if _, err = os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
		return false, replacementPending()
	}
	return true, nil
}

// Only the original EINVAL is unwrapped: a cleanup ENOTSUP is not proof of rename capability.
type replacementRenameProbeError struct {
	original error
	detail   error
}

func (e *replacementRenameProbeError) Error() string {
	return fmt.Sprintf("%v (rename capability probe: %v)", e.original, e.detail)
}
func (e *replacementRenameProbeError) Unwrap() error { return e.original }

// replacementOptionalTableExists is restricted to separately installed semantic
// storage. Catalog errors stay visible, unlike Migrator.HasTable's bool-only API.
func replacementOptionalTableExists(tx *gorm.DB, table string) (bool, error) {
	switch tx.Dialector.Name() {
	case "sqlite":
		var count int64
		err := tx.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'view') AND name = ?", table).Scan(&count).Error
		return count > 0, err
	case "postgres":
		var present bool
		err := tx.Raw("SELECT to_regclass(?) IS NOT NULL", table).Scan(&present).Error
		return present, err
	default:
		return false, fmt.Errorf("unsupported optional semantic catalog backend: %s", tx.Dialector.Name())
	}
}
