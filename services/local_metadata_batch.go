package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// LocalMetadataBatchPersonDecision 是同一批次内一个规范化来源名的唯一一次决策（D-PC32）：
// 前端每个来源名只展示一次选择，结果以 BatchResolutions 回传。
type LocalMetadataBatchPersonDecision struct {
	SourceName      string                `json:"source_name"`
	NormalizedName  string                `json:"normalized_name"`
	Matches         []LocalMetadataEntity `json:"matches"`
	DefaultMode     string                `json:"default_mode"`
	DefaultEntityID uint                  `json:"default_entity_id"`
	VideoIDs        []uint                `json:"video_ids"`
}

type LocalMetadataBatchPreview struct {
	Requested int                    `json:"requested"`
	Diffs     []LocalMetadataDiff    `json:"diffs"`
	Failures  []LocalMetadataFailure `json:"failures"`
	// PeopleDecisions 把各视频里同名的来源人物合并成一条决策，按规范化名字排序。
	PeopleDecisions []LocalMetadataBatchPersonDecision `json:"people_decisions"`
}

type LocalMetadataBatchApplyRequest struct {
	Requests []LocalMetadataApplyRequest `json:"requests"`
	// BatchResolutions 以规范化来源名为键，对每个视频的人物字段生效；某个请求自己带了同名
	// resolution 时以请求自己的为准。create_new 在批次内首次应用时创建人物，之后复用同一 ID。
	BatchResolutions map[string]LocalMetadataResolution `json:"batch_resolutions"`
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
	mu       sync.Mutex
	status   LocalMetadataBackfillStatus
	cancel   context.CancelFunc
	worker   sync.WaitGroup
	emitter  func(LocalMetadataBackfillStatus)
	registry *BackgroundTaskRegistry
}

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
// 补全与写出共用 local_metadata 这一个 key：任一在跑就算这项在跑，
// 登记表用计数配对，两条 worker 各自 Begin/End 不会互相抵消。
func (s *LocalMetadataService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	backfill := s.backfillState()
	backfill.mu.Lock()
	backfill.registry = registry
	backfill.mu.Unlock()
	export := s.exportState()
	export.mu.Lock()
	export.registry = registry
	export.mu.Unlock()
}

func (s *LocalMetadataService) PreviewBatch(videoIDs []uint) LocalMetadataBatchPreview {
	if len(videoIDs) > localMetadataBatchMaxItems {
		return LocalMetadataBatchPreview{Requested: len(videoIDs), Diffs: []LocalMetadataDiff{}, Failures: []LocalMetadataFailure{{
			ErrorCode: "batch_limit_exceeded", Message: "单次最多选择 500 个视频",
		}}}
	}
	videoIDs = uniqueSortedIDs(videoIDs)
	preview := LocalMetadataBatchPreview{Requested: len(videoIDs), Diffs: make([]LocalMetadataDiff, 0, len(videoIDs)),
		Failures: []LocalMetadataFailure{}, PeopleDecisions: []LocalMetadataBatchPersonDecision{}}
	decisions := make(map[string]*LocalMetadataBatchPersonDecision)
	for _, videoID := range videoIDs {
		diff, err := s.GetDiff(videoID)
		if err != nil {
			preview.Failures = append(preview.Failures, localMetadataFailure(videoID, err))
			continue
		}
		preview.Diffs = append(preview.Diffs, *diff)
		for _, candidate := range diff.People.Source {
			decision, exists := decisions[candidate.NormalizedName]
			if !exists {
				decision = &LocalMetadataBatchPersonDecision{
					SourceName: candidate.SourceName, NormalizedName: candidate.NormalizedName, Matches: candidate.Matches,
					DefaultMode: candidate.DefaultMode, DefaultEntityID: candidate.DefaultEntityID, VideoIDs: []uint{},
				}
				decisions[candidate.NormalizedName] = decision
			}
			decision.VideoIDs = append(decision.VideoIDs, videoID)
		}
	}
	names := make([]string, 0, len(decisions))
	for name := range decisions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		preview.PeopleDecisions = append(preview.PeopleDecisions, *decisions[name])
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
	batch := &localMetadataBatchContext{createdPeople: make(map[string]uint)}
	batchResolutions := make(map[string]LocalMetadataResolution, len(request.BatchResolutions))
	for name, resolution := range request.BatchResolutions {
		normalized := normalizeLocalMetadataName(name)
		if normalized == "" {
			continue
		}
		resolution.NormalizedName = normalized
		batchResolutions[normalized] = resolution
	}
	for _, item := range request.Requests {
		item.PeopleResolutions = mergeBatchPeopleResolutions(item.PeopleResolutions, batchResolutions)
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
		applied, err := s.apply(item, batch)
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

// mergeBatchPeopleResolutions 把批次级决策补进单个请求：请求自己已有的同名 resolution 优先。
// indexLocalMetadataResolutions 拒绝重复名字，所以这里按规范化名字去重后再补。
func mergeBatchPeopleResolutions(own []LocalMetadataResolution, batch map[string]LocalMetadataResolution) []LocalMetadataResolution {
	if len(batch) == 0 {
		return own
	}
	merged := append([]LocalMetadataResolution(nil), own...)
	present := make(map[string]struct{}, len(own))
	for _, resolution := range own {
		present[normalizeLocalMetadataName(resolution.NormalizedName)] = struct{}{}
	}
	names := make([]string, 0, len(batch))
	for name := range batch {
		if _, exists := present[name]; !exists {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		merged = append(merged, batch[name])
	}
	return merged
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
	registry := state.registry
	state.worker.Add(1)
	state.mu.Unlock()
	registry.Begin(BackgroundTaskLocalMetadata)
	emitLocalMetadataBackfill(emitter, status)
	go s.runBackfill(ctx, videos, registry)
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

func (s *LocalMetadataService) runBackfill(ctx context.Context, videos []models.Video, registry *BackgroundTaskRegistry) {
	defer s.backfillState().worker.Done()
	defer registry.End(BackgroundTaskLocalMetadata)
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
