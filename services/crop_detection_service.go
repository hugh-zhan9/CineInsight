package services

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
)

const cropDetectionAlgorithm = 1

var errCropDetectionConflict = errors.New("crop_detection_conflict")

// cropDetectionInfrastructureError distinguishes source-query failures from media failures.
type cropDetectionInfrastructureError struct{ cause error }

func (e *cropDetectionInfrastructureError) Error() string { return e.cause.Error() }
func (e *cropDetectionInfrastructureError) Unwrap() error { return e.cause }

// CropDetectionStatus uses the same task projection as other per-video backfills.
type CropDetectionStatus = FrameHashStatus

// CropDetectionService owns detection state; the editing service owns generated drafts.
type CropDetectionService struct {
	edit      *VideoEditService
	slot      *MediaWorkSlot
	mu        sync.Mutex
	stopMu    sync.Mutex
	wg        sync.WaitGroup
	status    CropDetectionStatus
	cancel    context.CancelFunc
	stopping  bool
	emitter   func(CropDetectionStatus)
	registry  *BackgroundTaskRegistry
	pauseHook TaskPauseHook
	detect    func(context.Context, uint) (EditCropResult, error)
}

func NewCropDetectionService(edit *VideoEditService, slot *MediaWorkSlot) *CropDetectionService {
	s := &CropDetectionService{edit: edit, slot: slot}
	s.detect = s.detectVideo
	return s
}
func (s *CropDetectionService) SetBackgroundTaskRegistry(r *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = r
	s.mu.Unlock()
}
func (s *CropDetectionService) SetEventEmitter(f func(CropDetectionStatus)) {
	s.mu.Lock()
	s.emitter = f
	s.mu.Unlock()
}
func (s *CropDetectionService) Status() CropDetectionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneFrameHashStatus(s.status)
}
func (s *CropDetectionService) update(f func(*CropDetectionStatus)) {
	s.mu.Lock()
	f(&s.status)
	now := time.Now()
	s.status.UpdatedAt = &now
	v, e := cloneFrameHashStatus(s.status), s.emitter
	s.mu.Unlock()
	if e != nil {
		e(v)
	}
}
func (s *CropDetectionService) Start(ctx context.Context) (CropDetectionStatus, error) {
	return s.start(ctx, nil, true)
}
func (s *CropDetectionService) StartWithPauseHook(ctx context.Context, h TaskPauseHook) (CropDetectionStatus, error) {
	return s.start(ctx, h, false)
}
func (s *CropDetectionService) start(parent context.Context, h TaskPauseHook, retry bool) (CropDetectionStatus, error) {
	if parent == nil {
		parent = context.Background()
	}
	if parent.Err() != nil {
		return CropDetectionStatus{}, parent.Err()
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		releaseTaskPauseHook(h)
		return CropDetectionStatus{}, errors.New("黑边识别正在停止")
	}
	releasing := clearReplacedPauseHook(&s.pauseHook, h)
	if s.status.Running {
		v := cloneFrameHashStatus(s.status)
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		releaseTaskPauseHook(h)
		return v, nil
	}
	s.pauseHook = h
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	now := time.Now()
	s.status = CropDetectionStatus{Running: true, Preparing: true, StartedAt: &now, UpdatedAt: &now, Failures: []FrameHashFailure{}}
	v := cloneFrameHashStatus(s.status)
	s.wg.Add(1)
	r := s.registry
	s.mu.Unlock()
	releaseTaskPauseHook(releasing)
	r.Begin(BackgroundTaskCropDetection)
	go func() {
		defer s.wg.Done()
		defer r.End(BackgroundTaskCropDetection)
		defer releaseTaskPauseHook(h)
		s.run(ctx, h, retry)
	}()
	return v, nil
}
func (s *CropDetectionService) Cancel() error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
func (s *CropDetectionService) StopAndWait() {
	if s == nil {
		return
	}
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	s.mu.Lock()
	s.stopping = true
	c := s.cancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
	s.wg.Wait()
}
func (s *CropDetectionService) Resume() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
}
func (s *CropDetectionService) run(ctx context.Context, h TaskPauseHook, retry bool) {
	defer s.update(func(v *CropDetectionStatus) {
		v.Running = false
		v.Preparing = false
		v.Cancelled = ctx.Err() != nil
		v.Completed = ctx.Err() == nil
		v.Gate = TaskGateState{}
	})
	var cursor uint
	for {
		db, err := editDB(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.batchError(err)
			}
			return
		}
		q, err := applyLibraryFilter(db.Model(&models.Video{}), LibraryFilter{}, time.Now())
		if err != nil {
			if ctx.Err() == nil {
				s.batchError(err)
			}
			return
		}
		var rows []models.Video
		if err = q.Where("videos.id > ? AND videos.is_stale = ?", cursor, false).Order("videos.id ASC").Limit(100).Find(&rows).Error; err != nil {
			if ctx.Err() == nil {
				s.batchError(err)
			}
			return
		}
		if len(rows) == 0 {
			return
		}
		s.update(func(v *CropDetectionStatus) { v.Preparing = false; v.Total += len(rows) })
		for _, video := range rows {
			cursor = video.ID
			if ctx.Err() != nil {
				return
			}
			s.mu.Lock()
			hook := s.pauseHook
			s.mu.Unlock()
			if hook != nil {
				if err = hook.Wait(ctx, func(g TaskGateState) { s.update(func(v *CropDetectionStatus) { v.Gate = g }) }); err != nil {
					return
				}
			}
			if err = s.process(ctx, video, retry); err != nil {
				if ctx.Err() == nil {
					s.batchError(err)
				}
				return
			}
		}
	}
}
func (s *CropDetectionService) batchError(err error) {
	s.update(func(v *CropDetectionStatus) {
		v.Failed++
		v.Failures = append(v.Failures, FrameHashFailure{Error: WithoutAbsolutePaths(err).Error()})
	})
}
func (s *CropDetectionService) process(ctx context.Context, v models.Video, retry bool) error {
	s.update(func(st *CropDetectionStatus) { st.CurrentVideoID = v.ID; st.CurrentVideoName = v.Name })
	db, err := editDB(ctx)
	if err != nil {
		return err
	}
	pending, pendingErr := editReplacementPathPendingError(db, v.Path)
	if pendingErr != nil {
		return pendingErr
	}
	if pending {
		s.skipped()
		return nil
	}
	size, mtime, err := editFileFingerprint(v.Path)
	if err != nil {
		s.skipped()
		return nil
	}
	version := editCropSourceVersion(size, mtime)
	var old models.VideoCropDetectionState
	err = db.First(&old, "video_id = ?", v.ID).Error
	exists := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if exists && old.SourceVersion == version && old.AlgorithmVersion == cropDetectionAlgorithm && (!retry || old.Status != "failed") {
		s.skipped()
		return nil
	}
	if err = s.slot.Acquire(ctx); err != nil {
		return err
	}
	result, detectErr := s.detect(ctx, v.ID)
	s.slot.Release()
	var infrastructure *cropDetectionInfrastructureError
	if errors.As(detectErr, &infrastructure) {
		return infrastructure.cause
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	afterSize, afterTime, statErr := editFileFingerprint(v.Path)
	if statErr != nil || editCropSourceVersion(afterSize, afterTime) != version {
		s.skipped()
		return nil
	}
	state := models.VideoCropDetectionState{VideoID: v.ID, SourceVersion: version, AlgorithmVersion: cropDetectionAlgorithm, Revision: old.Revision + 1, Status: result.Status, ResultJSON: "{}", CheckedAt: time.Now()}
	if detectErr != nil {
		state.Status = "failed"
		state.LastError = WithoutAbsolutePaths(detectErr).Error()
	} else {
		if result.SourceVersion != version {
			s.skipped()
			return nil
		}
		raw, e := json.Marshal(result)
		if e != nil {
			return e
		}
		state.ResultJSON = string(raw)
	}
	var deliveredProjectID uint
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		var current models.Video
		visible, e := applyLibraryFilter(tx.Model(&models.Video{}), LibraryFilter{}, time.Now())
		if e != nil {
			return e
		}
		if e = visible.Where("videos.id = ? AND videos.is_stale = ?", v.ID, false).First(&current).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return errCropDetectionConflict
			}
			return e
		}
		lastSize, lastTime, statError := editFileFingerprint(v.Path)
		if statError != nil || editCropSourceVersion(lastSize, lastTime) != version || current.Path != v.Path || current.IsStale {
			return errCropDetectionConflict
		}
		if exists {
			r := tx.Model(&models.VideoCropDetectionState{}).Where("video_id = ? AND revision = ?", v.ID, old.Revision).Updates(map[string]any{"source_version": state.SourceVersion, "algorithm_version": state.AlgorithmVersion, "revision": state.Revision, "status": state.Status, "result_json": state.ResultJSON, "project_id": nil, "last_error": state.LastError, "checked_at": state.CheckedAt})
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return errCropDetectionConflict
			}
		} else {
			insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state)
			if insert.Error != nil {
				return insert.Error
			}
			if insert.RowsAffected != 1 {
				return errCropDetectionConflict
			}
		}
		if state.Status == "detected" {
			id, e := s.edit.createDetectedCropDraftTx(tx, current, result)
			if e != nil {
				return e
			}
			updated := tx.Model(&models.VideoCropDetectionState{}).Where("video_id = ? AND revision = ?", v.ID, state.Revision).Update("project_id", id)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return errCropDetectionConflict
			}
			deliveredProjectID = id
			return nil
		}
		return nil
	})
	if errors.Is(err, errCropDetectionConflict) {
		s.skipped()
		return nil
	}
	if err != nil {
		return err
	}
	// Publish the existing edit-owner event only after the state and draft transaction commits.
	if deliveredProjectID != 0 {
		s.edit.notifyDetectedCropDraft(deliveredProjectID)
	}
	s.update(func(st *CropDetectionStatus) {
		st.Processed++
		if detectErr != nil {
			st.Failed++
			if len(st.Failures) < 50 {
				st.Failures = append(st.Failures, FrameHashFailure{VideoID: v.ID, Name: v.Name, Error: state.LastError})
			}
		} else {
			st.Succeeded++
		}
	})
	return nil
}
func (s *CropDetectionService) skipped() {
	s.update(func(v *CropDetectionStatus) { v.Processed++; v.Skipped++ })
}
func (s *CropDetectionService) detectVideo(ctx context.Context, id uint) (EditCropResult, error) {
	if _, err := s.edit.findFFprobe(); err != nil {
		return EditCropResult{}, &cropDetectionInfrastructureError{cause: err}
	}
	ffmpeg, err := s.edit.findFFmpeg()
	if err != nil {
		return EditCropResult{}, &cropDetectionInfrastructureError{cause: err}
	}
	pre := &EditPreflight{}
	sources, err := s.edit.collectEditSources(ctx, pre, []uint{id})
	if err != nil {
		return EditCropResult{}, &cropDetectionInfrastructureError{cause: err}
	}
	if len(pre.Errors) > 0 {
		return EditCropResult{}, editError(pre.Errors[0].Code, "%s", pre.Errors[0].Message)
	}
	return s.edit.detectCropSource(ctx, ffmpeg, sources[id], nil)
}
