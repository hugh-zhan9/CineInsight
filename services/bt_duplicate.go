package services

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

type BTDuplicateMatchView struct {
	VideoID  uint   `json:"video_id"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Relation string `json:"relation"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Size     string `json:"size"`
}
type BTDuplicateView struct {
	Status         string                 `json:"status"`
	Matches        []BTDuplicateMatchView `json:"matches"`
	UnhashedVideos int64                  `json:"unhashed_videos"`
	Error          string                 `json:"error"`
	VersionError   string                 `json:"version_error"`
}

func projectBTDuplicates(db *gorm.DB, task models.BTTask, files []models.BTFile, view *BTTaskView) {
	subjects := make([]downloadDuplicateSubject, len(files))
	for i, file := range files {
		if view.Files[i].VideoID != nil {
			subjects[i] = downloadDuplicateSubject{VideoID: *view.Files[i].VideoID, Path: filepath.Join(task.SaveRoot, filepath.FromSlash(file.RelativePath)), Raw: file.DuplicateResult}
		}
	}
	for i, result := range resolveDuplicateSubjectViews(db, subjects) {
		if result == nil {
			continue
		}
		out := &BTDuplicateView{Status: result.Status, Matches: []BTDuplicateMatchView{}, UnhashedVideos: result.UnhashedVideos, Error: result.Error, VersionError: result.VersionError}
		for _, match := range result.Matches {
			out.Matches = append(out.Matches, BTDuplicateMatchView{VideoID: match.VideoID, Title: match.Title, Kind: match.Kind, Relation: match.Relation, Width: match.Width, Height: match.Height, Size: strconv.FormatInt(match.Size, 10)})
		}
		view.Files[i].Duplicate = out
	}
}

type BTDuplicateCheck func(context.Context, string, uint) (DownloadDuplicateResult, error)

// BTDuplicateService owns one cancellable local worker. Its queue is the empty
// result field in BTFile; wakeups are hints, not a second persistent task source.
type BTDuplicateService struct {
	check                  BTDuplicateCheck
	changed                func(string, uint64)
	mu                     sync.Mutex
	paused, stopped        bool
	cancel                 context.CancelFunc
	done                   chan struct{}
	activeCancel           context.CancelFunc
	activeDone             chan struct{}
	wake                   chan struct{}
	cursorTask, cursorFile string
}

func NewBTDuplicateService(check BTDuplicateCheck, changed func(string, uint64)) *BTDuplicateService {
	return &BTDuplicateService{check: check, changed: changed, paused: true, wake: make(chan struct{}, 1)}
}
func (s *BTDuplicateService) Enqueue(_, _ string) {
	if s != nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}
func (s *BTDuplicateService) Start(ctx context.Context) {
	s.mu.Lock()
	if s.done != nil || s.stopped {
		s.mu.Unlock()
		return
	}
	life, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.mu.Unlock()
	go s.loop(life)
	s.Enqueue("", "")
}
func (s *BTDuplicateService) Resume() {
	s.mu.Lock()
	if !s.stopped {
		s.paused = false
	}
	s.mu.Unlock()
	s.Enqueue("", "")
}
func (s *BTDuplicateService) Quiesce(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.paused = true
	if s.activeCancel != nil {
		s.activeCancel()
	}
	done := s.activeDone
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (s *BTDuplicateService) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.stopped = true
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err := s.Quiesce(ctx); err != nil {
		return err
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (s *BTDuplicateService) loop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
		// Bound each pass; persistent cursor moves even for invisible/changed files.
		for i := 0; i < 20; i++ {
			s.mu.Lock()
			if s.paused || s.stopped {
				s.mu.Unlock()
				break
			}
			job, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			s.activeCancel = cancel
			s.activeDone = done
			s.mu.Unlock()
			found, _ := s.checkNext(job)
			cancel()
			s.mu.Lock()
			close(done)
			s.activeCancel = nil
			s.activeDone = nil
			s.mu.Unlock()
			if !found {
				break
			}
		}
	}
}
func (s *BTDuplicateService) checkNext(ctx context.Context) (bool, error) {
	if s.check == nil {
		return false, nil
	}
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	found := false
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var file models.BTFile
		q := db.Where("import_state = ? AND publication_state NOT IN ? AND duplicate_result = ? AND video_id IS NOT NULL AND selected = ? AND padding = ?", "imported", []string{BTImportPublicationPendingNew, BTImportPublicationPendingRestore}, "", true, false)
		if s.cursorTask != "" {
			q = q.Where("task_id > ? OR (task_id = ? AND manifest_file_id > ?)", s.cursorTask, s.cursorTask, s.cursorFile)
		}
		result := q.Order("task_id ASC,manifest_file_id ASC").Limit(1).Find(&file)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			s.cursorTask = ""
			s.cursorFile = ""
			return nil
		}
		found = true
		s.cursorTask = file.TaskID
		s.cursorFile = file.ManifestFileID
		var task models.BTTask
		if db.First(&task, "id = ?", file.TaskID).Error != nil {
			return ErrBTTask
		}
		path := filepath.Join(task.SaveRoot, filepath.FromSlash(file.RelativePath))
		if !btDuplicateSourceValid(db, task, file, path) {
			return nil
		}
		before, err := os.Stat(path)
		if err != nil {
			return nil
		}
		checkContext, cancelCheck := context.WithTimeout(ctx, downloadDuplicateCheckTimeout)
		checked, checkErr := s.check(checkContext, path, *file.VideoID)
		cancelCheck()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if checkErr != nil {
			checked = DownloadDuplicateResult{Status: DownloadDuplicateFailed, Error: downloadDuplicateErrorText(checkErr), Matches: []DownloadDuplicateMatch{}, CheckedAt: time.Now(), VideoID: *file.VideoID, SourceSize: before.Size(), SourceModTimeNS: before.ModTime().UnixNano()}
		}
		if checked.VideoID != *file.VideoID || checked.SourceSize != before.Size() || checked.SourceModTimeNS != before.ModTime().UnixNano() {
			return nil
		}
		if !btDuplicateSourceValid(db, task, file, path) {
			return nil
		}
		raw := encodeDownloadDuplicateResult(checked)
		if raw == "" {
			return ErrBTTask
		}
		update := db.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ? AND video_id = ? AND import_state = ? AND publication_state = ? AND observed_size = ? AND observed_mtime = ? AND duplicate_result = ?", file.TaskID, file.ManifestFileID, *file.VideoID, file.ImportState, file.PublicationState, file.ObservedSize, file.ObservedMtime, "").Update("duplicate_result", raw)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 1 && s.changed != nil {
			s.changed(task.ID, task.Revision)
		}
		return nil
	})
	return found, err
}
func btDuplicateSourceValid(db *gorm.DB, task models.BTTask, file models.BTFile, path string) bool {
	if file.VideoID == nil || file.ImportState != "imported" || file.PublicationState == BTImportPublicationPendingNew || file.PublicationState == BTImportPublicationPendingRestore {
		return false
	}
	query, err := visibleVideoQuery(db.Model(&models.Video{}))
	if err != nil {
		return false
	}
	var video models.Video
	if query.Where("videos.id = ? AND videos.path = ?", *file.VideoID, path).First(&video).Error != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !btPathInside(task.CanonicalRoot, real) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() == file.ObservedSize && info.ModTime().UnixNano() == file.ObservedMtime
}
