package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

const (
	localMetadataFailureLimit  = 50
	localMetadataBatchMaxItems = 500
)

type LocalMetadataFailure struct {
	VideoID   uint   `json:"video_id"`
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

type LocalMetadataBatchPreview struct {
	Requested int                    `json:"requested"`
	Diffs     []LocalMetadataDiff    `json:"diffs"`
	Failures  []LocalMetadataFailure `json:"failures"`
}

type LocalMetadataBatchApplyRequest struct {
	Requests []LocalMetadataApplyRequest `json:"requests"`
}

type LocalMetadataBatchResult struct {
	Requested int                        `json:"requested"`
	Succeeded int                        `json:"succeeded"`
	Failed    int                        `json:"failed"`
	Results   []LocalMetadataApplyResult `json:"results"`
	Failures  []LocalMetadataFailure     `json:"failures"`
}

type LocalMetadataBackfillStatus struct {
	Running        bool                   `json:"running"`
	Cancelled      bool                   `json:"cancelled"`
	Completed      bool                   `json:"completed"`
	Total          int                    `json:"total"`
	Processed      int                    `json:"processed"`
	Succeeded      int                    `json:"succeeded"`
	Skipped        int                    `json:"skipped"`
	Failed         int                    `json:"failed"`
	CurrentVideoID uint                   `json:"current_video_id"`
	StartedAt      *time.Time             `json:"started_at" ts_type:"string"`
	UpdatedAt      *time.Time             `json:"updated_at" ts_type:"string"`
	Failures       []LocalMetadataFailure `json:"failures"`
}

type localMetadataBackfill struct {
	mu      sync.Mutex
	status  LocalMetadataBackfillStatus
	cancel  context.CancelFunc
	worker  sync.WaitGroup
	emitter func(LocalMetadataBackfillStatus)
}

func (s *LocalMetadataService) PreviewBatch(videoIDs []uint) LocalMetadataBatchPreview {
	if len(videoIDs) > localMetadataBatchMaxItems {
		return LocalMetadataBatchPreview{Requested: len(videoIDs), Diffs: []LocalMetadataDiff{}, Failures: []LocalMetadataFailure{{
			ErrorCode: "batch_limit_exceeded", Message: "单次最多选择 500 个视频",
		}}}
	}
	videoIDs = uniqueSortedIDs(videoIDs)
	preview := LocalMetadataBatchPreview{Requested: len(videoIDs), Diffs: make([]LocalMetadataDiff, 0, len(videoIDs)), Failures: []LocalMetadataFailure{}}
	for _, videoID := range videoIDs {
		diff, err := s.GetDiff(videoID)
		if err != nil {
			preview.Failures = append(preview.Failures, localMetadataFailure(videoID, err))
			continue
		}
		preview.Diffs = append(preview.Diffs, *diff)
	}
	return preview
}

func (s *LocalMetadataService) ApplyBatch(request LocalMetadataBatchApplyRequest) LocalMetadataBatchResult {
	result := LocalMetadataBatchResult{Requested: len(request.Requests), Results: []LocalMetadataApplyResult{}, Failures: []LocalMetadataFailure{}}
	if len(request.Requests) > localMetadataBatchMaxItems {
		result.Failed = len(request.Requests)
		result.Failures = append(result.Failures, LocalMetadataFailure{ErrorCode: "batch_limit_exceeded", Message: "单次最多选择 500 个视频"})
		return result
	}
	seen := make(map[uint]struct{}, len(request.Requests))
	for _, item := range request.Requests {
		if item.VideoID == 0 {
			result.Failed++
			result.Failures = append(result.Failures, LocalMetadataFailure{ErrorCode: "invalid_request", Message: "视频 ID 不能为空"})
			continue
		}
		if _, exists := seen[item.VideoID]; exists {
			result.Failed++
			result.Failures = append(result.Failures, LocalMetadataFailure{VideoID: item.VideoID, ErrorCode: "duplicate_video", Message: "批次中存在重复视频"})
			continue
		}
		seen[item.VideoID] = struct{}{}
		applied, err := s.Apply(item)
		if err != nil {
			result.Failed++
			result.Failures = append(result.Failures, localMetadataFailure(item.VideoID, err))
			continue
		}
		result.Succeeded++
		result.Results = append(result.Results, *applied)
	}
	return result
}

func localMetadataFailure(videoID uint, err error) LocalMetadataFailure {
	code := "metadata_failed"
	switch {
	case errors.Is(err, ErrLocalMetadataConflict):
		code = "metadata_conflict"
	case errors.Is(err, ErrLocalMetadataOverwriteRequired):
		code = "overwrite_required"
	case errors.Is(err, ErrLocalMetadataNFOInvalid):
		code = "nfo_invalid"
	case errors.Is(err, ErrLocalMetadataNFOSymlink):
		code = "nfo_symlink"
	case errors.Is(err, ErrLocalMetadataNFOConflict):
		code = "nfo_conflict"
	case errors.Is(err, gorm.ErrRecordNotFound):
		code = "video_not_found"
	}
	message := stringsForLocalMetadataFailure(err)
	return LocalMetadataFailure{VideoID: videoID, ErrorCode: code, Message: message}
}

func stringsForLocalMetadataFailure(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

func (s *LocalMetadataService) SetBackfillEventEmitter(emitter func(LocalMetadataBackfillStatus)) {
	state := s.backfillState()
	state.mu.Lock()
	state.emitter = emitter
	state.mu.Unlock()
}

func (s *LocalMetadataService) StartBackfill(parent context.Context) (LocalMetadataBackfillStatus, error) {
	state := s.backfillState()
	if parent == nil {
		parent = context.Background()
	}
	state.mu.Lock()
	if state.status.Running {
		status := cloneLocalMetadataBackfillStatus(state.status)
		state.mu.Unlock()
		return status, nil
	}
	videos, err := s.backfillLoad(parent)
	if err != nil {
		state.mu.Unlock()
		return LocalMetadataBackfillStatus{}, fmt.Errorf("load metadata backfill videos: %w", err)
	}
	now := time.Now()
	ctx, cancel := context.WithCancel(parent)
	state.cancel = cancel
	state.status = LocalMetadataBackfillStatus{
		Running: true, Total: len(videos), StartedAt: &now, UpdatedAt: &now, Failures: []LocalMetadataFailure{},
	}
	status, emitter := cloneLocalMetadataBackfillStatus(state.status), state.emitter
	state.worker.Add(1)
	state.mu.Unlock()
	emitLocalMetadataBackfill(emitter, status)
	go s.runBackfill(ctx, videos)
	return status, nil
}

func (s *LocalMetadataService) BackfillStatus() LocalMetadataBackfillStatus {
	state := s.backfillState()
	state.mu.Lock()
	defer state.mu.Unlock()
	return cloneLocalMetadataBackfillStatus(state.status)
}

func (s *LocalMetadataService) CancelBackfill() error {
	state := s.backfillState()
	state.mu.Lock()
	cancel := state.cancel
	running := state.status.Running
	state.mu.Unlock()
	if !running || cancel == nil {
		return errors.New("local metadata backfill is not running")
	}
	cancel()
	return nil
}

// StopBackfill cancels a running backfill and waits for the worker to finish its
// current video, so shutdown never races with an in-flight transaction or image copy.
func (s *LocalMetadataService) StopBackfill() {
	state := s.backfillState()
	state.mu.Lock()
	cancel := state.cancel
	state.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	state.worker.Wait()
}

func (s *LocalMetadataService) runBackfill(ctx context.Context, videos []models.Video) {
	defer s.backfillState().worker.Done()
	for _, video := range videos {
		if ctx.Err() != nil {
			break
		}
		s.updateBackfill(func(status *LocalMetadataBackfillStatus) { status.CurrentVideoID = video.ID })
		applied, err := s.backfillProcess(ctx, video.ID)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				break
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				s.updateBackfill(func(status *LocalMetadataBackfillStatus) { status.Processed++; status.Skipped++ })
				continue
			}
			s.recordBackfillFailure(video.ID, err)
			continue
		}
		if !applied {
			s.updateBackfill(func(status *LocalMetadataBackfillStatus) { status.Processed++; status.Skipped++ })
			continue
		}
		s.updateBackfill(func(status *LocalMetadataBackfillStatus) { status.Processed++; status.Succeeded++ })
	}
	s.finishBackfill(ctx.Err() != nil)
}

func loadLocalMetadataBackfillVideos(ctx context.Context) ([]models.Video, error) {
	var videos []models.Video
	err := database.DB.WithContext(ctx).Select("id", "name").Order("id ASC").Find(&videos).Error
	return videos, err
}

func (s *LocalMetadataService) processLocalMetadataBackfillVideo(ctx context.Context, videoID uint) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	diff, err := s.GetDiff(videoID)
	if err != nil {
		return false, err
	}
	if diff.Status == LocalMetadataStateMissing || !localMetadataDiffHasDefaults(diff) {
		return false, nil
	}
	result, err := s.ApplyDefaults(videoID)
	if err != nil {
		return false, err
	}
	// ApplyDefaults re-reads the sources and yields nil when they vanished in between.
	if result == nil {
		return false, nil
	}
	return len(result.AppliedFields) > 0, nil
}

func localMetadataDiffHasDefaults(diff *LocalMetadataDiff) bool {
	return diff.Title.DefaultSelected || diff.OriginalTitle.DefaultSelected || diff.Description.DefaultSelected ||
		diff.People.DefaultSelected || diff.Collection.DefaultSelected || diff.Poster.DefaultSelected || diff.Fanart.DefaultSelected
}

func (s *LocalMetadataService) recordBackfillFailure(videoID uint, err error) {
	s.updateBackfill(func(status *LocalMetadataBackfillStatus) {
		status.Processed++
		status.Failed++
		if len(status.Failures) < localMetadataFailureLimit {
			status.Failures = append(status.Failures, localMetadataFailure(videoID, err))
		}
	})
}

func (s *LocalMetadataService) updateBackfill(update func(*LocalMetadataBackfillStatus)) {
	state := s.backfillState()
	state.mu.Lock()
	update(&state.status)
	now := time.Now()
	state.status.UpdatedAt = &now
	status, emitter := cloneLocalMetadataBackfillStatus(state.status), state.emitter
	state.mu.Unlock()
	emitLocalMetadataBackfill(emitter, status)
}

func (s *LocalMetadataService) finishBackfill(cancelled bool) {
	state := s.backfillState()
	state.mu.Lock()
	state.status.Running = false
	state.status.Completed = true
	state.status.Cancelled = cancelled
	state.status.CurrentVideoID = 0
	now := time.Now()
	state.status.UpdatedAt = &now
	state.cancel = nil
	status, emitter := cloneLocalMetadataBackfillStatus(state.status), state.emitter
	state.mu.Unlock()
	emitLocalMetadataBackfill(emitter, status)
}

func cloneLocalMetadataBackfillStatus(status LocalMetadataBackfillStatus) LocalMetadataBackfillStatus {
	status.Failures = append([]LocalMetadataFailure(nil), status.Failures...)
	return status
}

func emitLocalMetadataBackfill(emitter func(LocalMetadataBackfillStatus), status LocalMetadataBackfillStatus) {
	if emitter != nil {
		emitter(status)
	}
}
