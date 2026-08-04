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

const technicalBackfillFailureLimit = 50

var ErrTechnicalBackfillNotRunning = errors.New("technical backfill is not running")

type TechnicalBackfillFailure struct {
	VideoID uint   `json:"video_id"`
	Name    string `json:"name"`
	Error   string `json:"error"`
}

type TechnicalBackfillStatus struct {
	Running          bool                       `json:"running"`
	Preparing        bool                       `json:"preparing"`
	Cancelled        bool                       `json:"cancelled"`
	Completed        bool                       `json:"completed"`
	Total            int                        `json:"total"`
	Processed        int                        `json:"processed"`
	Succeeded        int                        `json:"succeeded"`
	Skipped          int                        `json:"skipped"`
	Failed           int                        `json:"failed"`
	CurrentVideoID   uint                       `json:"current_video_id"`
	CurrentVideoName string                     `json:"current_video_name"`
	StartedAt        *time.Time                 `json:"started_at" ts_type:"string"`
	UpdatedAt        *time.Time                 `json:"updated_at" ts_type:"string"`
	Failures         []TechnicalBackfillFailure `json:"failures"`
}

type technicalBackfillCandidate struct {
	ID   uint
	Name string
}

type TechnicalBackfillService struct {
	probe          *MediaProbeService
	loadCandidates func(context.Context) ([]technicalBackfillCandidate, error)
	syncVideoTag   func(uint) error
	mu             sync.Mutex
	status         TechnicalBackfillStatus
	cancel         context.CancelFunc
	emitter        func(TechnicalBackfillStatus)
	worker         sync.WaitGroup
}

func NewTechnicalBackfillService(probe *MediaProbeService) *TechnicalBackfillService {
	return &TechnicalBackfillService{
		probe:          probe,
		loadCandidates: loadTechnicalBackfillCandidates,
		syncVideoTag: func(videoID uint) error {
			return database.Transaction(func(tx *gorm.DB) error { return syncShortVideoTagForVideo(tx, videoID) })
		},
	}
}

func (s *TechnicalBackfillService) SetEventEmitter(emitter func(TechnicalBackfillStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitter = emitter
}

func (s *TechnicalBackfillService) Start(parent context.Context) (TechnicalBackfillStatus, error) {
	if s == nil || s.probe == nil {
		return TechnicalBackfillStatus{}, errors.New("technical backfill service is not initialized")
	}
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	if s.status.Running {
		status := s.snapshotLocked()
		s.mu.Unlock()
		return status, nil
	}
	now := time.Now()
	s.status = TechnicalBackfillStatus{
		Running:   true,
		Preparing: true,
		StartedAt: &now,
		UpdatedAt: &now,
		Failures:  []TechnicalBackfillFailure{},
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	status := s.snapshotLocked()
	emitter := s.emitter
	s.worker.Add(1)
	s.mu.Unlock()
	emitTechnicalBackfillStatus(emitter, status)
	go func() {
		defer s.worker.Done()
		s.prepareAndRun(ctx)
	}()
	return status, nil
}

func loadTechnicalBackfillCandidates(ctx context.Context) ([]technicalBackfillCandidate, error) {
	var videos []models.Video
	if err := database.DB.WithContext(ctx).Select("id", "name", "path").Order("id ASC").Find(&videos).Error; err != nil {
		return nil, fmt.Errorf("load technical backfill videos: %w", err)
	}
	metadataByVideoID := make(map[uint]models.VideoTechnicalMetadata, len(videos))
	if len(videos) > 0 {
		var metadataRows []models.VideoTechnicalMetadata
		if err := database.DB.WithContext(ctx).
			Select("video_id", "successful_source_size", "successful_source_mod_time_ns", "probed_at", "last_error").
			Find(&metadataRows).Error; err != nil {
			return nil, fmt.Errorf("load technical backfill metadata: %w", err)
		}
		for _, metadata := range metadataRows {
			metadataByVideoID[metadata.VideoID] = metadata
		}
	}
	candidates := make([]technicalBackfillCandidate, 0)
	for _, video := range videos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		metadata, exists := metadataByVideoID[video.ID]
		fresh := exists && videoTechnicalSnapshotMatchesFile(video, &metadata)
		if !fresh {
			candidates = append(candidates, technicalBackfillCandidate{ID: video.ID, Name: video.Name})
		}
	}
	return candidates, nil
}

func videoTechnicalSnapshotIsFresh(ctx context.Context, video models.Video) (bool, error) {
	var metadata models.VideoTechnicalMetadata
	err := database.DB.WithContext(ctx).Select("video_id", "successful_source_size", "successful_source_mod_time_ns", "probed_at", "last_error").
		First(&metadata, "video_id = ?", video.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return videoTechnicalSnapshotMatchesFile(video, &metadata), nil
}

func videoTechnicalSnapshotMatchesFile(video models.Video, metadata *models.VideoTechnicalMetadata) bool {
	if metadata == nil || metadata.LastError != "" || metadata.ProbedAt == nil || metadata.SuccessfulSourceSize == nil || metadata.SuccessfulSourceModTimeNS == nil {
		return false
	}
	fingerprint, err := mediaProbeStat(video.Path)
	if err != nil {
		return false
	}
	return fingerprint.size == *metadata.SuccessfulSourceSize && fingerprint.modTimeNS == *metadata.SuccessfulSourceModTimeNS
}

func (s *TechnicalBackfillService) prepareAndRun(ctx context.Context) {
	candidates, err := s.loadCandidates(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			s.finish(true)
			return
		}
		s.failPreparation(err)
		return
	}
	if ctx.Err() != nil {
		s.finish(true)
		return
	}
	s.mu.Lock()
	s.status.Preparing = false
	s.status.Total = len(candidates)
	now := time.Now()
	s.status.UpdatedAt = &now
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	emitTechnicalBackfillStatus(emitter, status)
	if len(candidates) == 0 {
		s.finish(false)
		return
	}
	s.run(ctx, candidates)
}

func (s *TechnicalBackfillService) failPreparation(err error) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Preparing = false
	s.status.Completed = true
	s.status.Failed = 1
	s.status.Failures = []TechnicalBackfillFailure{{Name: "准备补全任务", Error: truncateMediaProbeError(err.Error())}}
	now := time.Now()
	s.status.UpdatedAt = &now
	cancel := s.cancel
	s.cancel = nil
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	emitTechnicalBackfillStatus(emitter, status)
}

func (s *TechnicalBackfillService) run(ctx context.Context, candidates []technicalBackfillCandidate) {
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		s.setCurrent(candidate)
		var video models.Video
		if err := database.DB.Select("id", "name", "path").First(&video, candidate.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				s.recordSkipped()
				continue
			}
			s.recordFailure(candidate, fmt.Errorf("reload video: %w", err))
			continue
		}
		fresh, err := videoTechnicalSnapshotIsFresh(ctx, video)
		if err != nil {
			s.recordFailure(candidate, err)
			continue
		}
		if fresh {
			s.recordSkipped()
			continue
		}
		if err := s.probe.Refresh(ctx, candidate.ID); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				break
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				s.recordSkipped()
				continue
			}
			s.recordFailure(candidate, err)
			continue
		}
		if err := s.syncVideoTag(candidate.ID); err != nil {
			syncErr := fmt.Errorf("sync short video tag: %w", err)
			var fingerprint *mediaProbeFingerprint
			if current, statErr := mediaProbeStat(video.Path); statErr == nil {
				fingerprint = &current
			} else {
				syncErr = errors.Join(syncErr, statErr)
			}
			s.recordFailure(candidate, s.probe.recordFailureOrJoin(candidate.ID, fingerprint, time.Now(), syncErr))
			continue
		}
		s.recordSuccess()
	}
	s.finish(ctx.Err() != nil)
}

func (s *TechnicalBackfillService) setCurrent(candidate technicalBackfillCandidate) {
	s.mu.Lock()
	s.status.CurrentVideoID = candidate.ID
	s.status.CurrentVideoName = candidate.Name
	now := time.Now()
	s.status.UpdatedAt = &now
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	emitTechnicalBackfillStatus(emitter, status)
}

func (s *TechnicalBackfillService) recordSkipped() {
	s.updateItem(func(status *TechnicalBackfillStatus) {
		status.Processed++
		status.Skipped++
	})
}

func (s *TechnicalBackfillService) recordSuccess() {
	s.updateItem(func(status *TechnicalBackfillStatus) {
		status.Processed++
		status.Succeeded++
	})
}

func (s *TechnicalBackfillService) recordFailure(candidate technicalBackfillCandidate, err error) {
	s.updateItem(func(status *TechnicalBackfillStatus) {
		status.Processed++
		status.Failed++
		if len(status.Failures) < technicalBackfillFailureLimit {
			status.Failures = append(status.Failures, TechnicalBackfillFailure{
				VideoID: candidate.ID, Name: candidate.Name, Error: truncateMediaProbeError(err.Error()),
			})
		}
	})
}

func (s *TechnicalBackfillService) updateItem(update func(*TechnicalBackfillStatus)) {
	s.mu.Lock()
	update(&s.status)
	now := time.Now()
	s.status.UpdatedAt = &now
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	emitTechnicalBackfillStatus(emitter, status)
}

func (s *TechnicalBackfillService) finish(cancelled bool) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Preparing = false
	s.status.Cancelled = cancelled
	s.status.Completed = !cancelled
	s.status.CurrentVideoID = 0
	s.status.CurrentVideoName = ""
	now := time.Now()
	s.status.UpdatedAt = &now
	cancel := s.cancel
	s.cancel = nil
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	emitTechnicalBackfillStatus(emitter, status)
}

func (s *TechnicalBackfillService) Cancel() error {
	s.mu.Lock()
	if !s.status.Running || s.cancel == nil {
		s.mu.Unlock()
		return ErrTechnicalBackfillNotRunning
	}
	cancel := s.cancel
	s.status.Cancelled = true
	now := time.Now()
	s.status.UpdatedAt = &now
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	cancel()
	emitTechnicalBackfillStatus(emitter, status)
	return nil
}

func (s *TechnicalBackfillService) StopAndWait() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.worker.Wait()
}

func (s *TechnicalBackfillService) Status() TechnicalBackfillStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *TechnicalBackfillService) snapshotLocked() TechnicalBackfillStatus {
	status := s.status
	status.Failures = append([]TechnicalBackfillFailure(nil), s.status.Failures...)
	return status
}

func emitTechnicalBackfillStatus(emitter func(TechnicalBackfillStatus), status TechnicalBackfillStatus) {
	if emitter != nil {
		emitter(status)
	}
}
