package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrStructureObsolete = errors.New("structure_obsolete: 分析来源或任务已变化")
var ErrStructureRuntimeUnavailable = errors.New("structure_runtime_unavailable: 本机FFmpeg组件不可用")

var ErrStructureUnavailable = errors.New("structure_unavailable: 原片当前不可访问")

type VideoStructureView struct {
	VideoID          uint                        `json:"video_id"`
	Revision         uint64                      `json:"revision"`
	Status           string                      `json:"status"`
	SourceStatus     string                      `json:"source_status"`
	SourceVersion    string                      `json:"source_version"`
	AlgorithmVersion string                      `json:"algorithm_version"`
	Generation       uint64                      `json:"generation"`
	CheckpointMS     int64                       `json:"checkpoint_ms"`
	DurationMS       int64                       `json:"duration_ms"`
	ErrorCode        string                      `json:"error_code"`
	Segments         []VideoStructureSegmentView `json:"segments"`
}
type VideoStructureSegmentView struct {
	Position         int    `json:"position"`
	StartMS          int64  `json:"start_ms"`
	EndMS            int64  `json:"end_ms"`
	RepresentativeMS int64  `json:"representative_ms"`
	Title            string `json:"title"`
	Basis            string `json:"basis"`
}
type VideoStructureStatus struct {
	Running        bool   `json:"running"`
	WaitingIdle    bool   `json:"waiting_idle"`
	Reason         string `json:"reason"`
	CurrentVideoID uint   `json:"current_video_id"`
	Pending        int64  `json:"pending"`
	Ready          int64  `json:"ready"`
	Failed         int64  `json:"failed"`
	Unavailable    int64  `json:"unavailable"`
	Error          string `json:"error"`
}

type VideoStructureService struct {
	sourceCallbacks  sync.WaitGroup
	scanRequested    bool
	activeSource     string
	activeVideo      uint
	playback         map[string]time.Time
	transfers        int
	mediaQuietUntil  time.Time
	probe            *MediaProbeService
	slot             *MediaWorkSlot
	extract          func(context.Context, string, int64, int64) ([]structureFrame, error)
	runtimeAvailable func() bool
	mu               sync.Mutex
	status           VideoStructureStatus
	cancel           context.CancelFunc
	windowCancel     context.CancelFunc
	done             chan struct{}
	wake             chan struct{}
	stopped          bool
	registry         *BackgroundTaskRegistry
	emitter          func(VideoStructureStatus)
	activity         func(context.Context) (bool, string)
	now              func() time.Time
}

func NewVideoStructureService(probe *MediaProbeService, slot *MediaWorkSlot) *VideoStructureService {
	return &VideoStructureService{probe: probe, slot: slot, extract: extractStructureFrames, runtimeAvailable: func() bool { _, a := findThumbnailFFmpeg(); _, b := findFFProbeBinary(); return a == nil && b == nil }, wake: make(chan struct{}, 1), now: time.Now}
}
func structureReadSource(ctx context.Context, id uint) (models.Video, os.FileInfo, error) {
	var video models.Video
	var info os.FileInfo
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var err error
		query, err := visibleVideoQuery(db.Model(&models.Video{}))
		if err != nil {
			return err
		}
		if err = query.First(&video, id).Error; err != nil {
			return ErrStructureUnavailable
		}
		info, err = os.Stat(video.Path)
		if err != nil || !info.Mode().IsRegular() {
			return ErrStructureUnavailable
		}
		return nil
	})
	return video, info, err
}
func structureCAS(db *gorm.DB, state models.VideoStructureState) *gorm.DB {
	return db.Model(&models.VideoStructureState{}).Where("id = ? AND revision = ? AND generation = ? AND run_token = ?", state.ID, state.Revision, state.Generation, state.RunToken)
}
func structureChanged(update *gorm.DB) error {
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return ErrStructureObsolete
	}
	return nil
}
func structureReset(source string, info os.FileInfo) map[string]interface{} {
	return map[string]interface{}{"source_version": source, "source_size": info.Size(), "source_mod_time_ns": info.ModTime().UnixNano(), "algorithm_version": structureAlgorithmVersion, "generation": gorm.Expr("generation + 1"), "revision": gorm.Expr("revision + 1"), "run_token": "", "status": "pending", "phase": "analyze", "duration_ms": 0, "checkpoint_ms": 0, "pending_start_ms": 0, "pending_frame_pts": 0, "previous_sample_pts": 0, "previous_sample": nil, "error_code": "", "pause_reason": ""}
}

// Observe is a bounded discovery operation. A changed source never authorizes an old run.
func (s *VideoStructureService) Observe(ctx context.Context, id uint) error {
	video, info, sourceErr := structureReadSource(ctx, id)
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var state models.VideoStructureState
			err := tx.Where("video_id = ?", id).First(&state).Error
			if sourceErr != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					if video.ID == 0 {
						return nil
					}
					return tx.Create(&models.VideoStructureState{VideoID: id, Revision: 1, Generation: 1, AlgorithmVersion: structureAlgorithmVersion, Status: "unavailable", ErrorCode: "source_unavailable", Phase: "analyze"}).Error
				}
				if err != nil {
					return err
				}
				if state.Status == "unavailable" {
					return nil
				}
				return structureChanged(structureCAS(tx, state).Updates(map[string]interface{}{"status": "unavailable", "run_token": "", "revision": gorm.Expr("revision + 1"), "error_code": "source_unavailable"}))
			}
			source := videoSourceVersion(id, info.Size(), info.ModTime().UnixNano())
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.VideoStructureState{VideoID: id, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(), SourceVersion: source, Revision: 1, Generation: 1, AlgorithmVersion: structureAlgorithmVersion, Status: "pending", Phase: "analyze"}).Error
			}
			if err != nil {
				return err
			}
			if state.SourceVersion != source || state.AlgorithmVersion != structureAlgorithmVersion || state.Status == "source_changed" {
				if err := structureChanged(structureCAS(tx, state).Updates(structureReset(source, info))); err != nil {
					return err
				}
				query := tx.Where("state_id = ?", state.ID)
				if state.PublishedGeneration != nil {
					query = query.Where("generation <> ?", *state.PublishedGeneration)
				}
				return query.Delete(&models.VideoStructureSegment{}).Error
			}
			if state.Status == "unavailable" && state.ErrorCode == "structure_runtime_unavailable" && !s.runtimeAvailable() {
				return nil
			}
			if state.Status == "unavailable" || state.Status == "source_changed" {
				return structureChanged(structureCAS(tx, state).Updates(map[string]interface{}{"status": "pending", "run_token": "", "revision": gorm.Expr("revision + 1"), "error_code": ""}))
			}
			return nil
		})
	})
}
func (s *VideoStructureService) Recover(ctx context.Context) error {
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var cursor uint
		for {
			var changed []models.VideoStructureState
			if err := db.Select("id", "revision", "generation", "run_token", "published_generation").Where("status = ? AND id > ?", "source_changed", cursor).Order("id").Limit(200).Find(&changed).Error; err != nil {
				return err
			}
			if len(changed) == 0 {
				break
			}
			if err := db.Transaction(func(tx *gorm.DB) error {
				for _, state := range changed {
					update := structureCAS(tx, state).Where("status = ?", "source_changed").Updates(map[string]interface{}{"status": "pending", "generation": gorm.Expr("generation + 1"), "revision": gorm.Expr("revision + 1"), "run_token": "", "checkpoint_ms": 0, "pending_start_ms": 0, "pending_frame_pts": 0, "previous_sample_pts": 0, "previous_sample": nil, "phase": "analyze", "duration_ms": 0})
					if update.Error != nil {
						return update.Error
					}
					if update.RowsAffected == 0 {
						continue
					}
					query := tx.Where("state_id = ?", state.ID)
					if state.PublishedGeneration != nil {
						query = query.Where("generation <> ?", *state.PublishedGeneration)
					}
					if err := query.Delete(&models.VideoStructureSegment{}).Error; err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
			cursor = changed[len(changed)-1].ID
		}
		return db.Model(&models.VideoStructureState{}).Where("status IN ?", []string{"running", "waiting_idle", "pending"}).Updates(map[string]interface{}{"status": "pending", "run_token": "", "revision": gorm.Expr("revision + 1")}).Error
	})
}
func (s *VideoStructureService) Discover(ctx context.Context) error {
	var cursor uint
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var ids []uint
		if err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
			query, err := visibleVideoQuery(db.Model(&models.Video{}))
			if err != nil {
				return err
			}
			return query.Where("videos.id > ?", cursor).Order("videos.id").Limit(200).Pluck("videos.id", &ids).Error
		}); err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			if err := s.Observe(ctx, id); err != nil && !errors.Is(err, ErrStructureObsolete) {
				return err
			}
			cursor = id
		}
	}
	// Scope removal is a cancellation boundary even while the physical file still exists.
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		visible, err := visibleVideoQuery(db.Model(&models.Video{}).Select("videos.id"))
		if err != nil {
			return err
		}
		return db.Model(&models.VideoStructureState{}).Where("video_id NOT IN (?) AND status <> ?", visible, "unavailable").Updates(map[string]interface{}{"status": "unavailable", "run_token": "", "revision": gorm.Expr("revision + 1"), "error_code": "source_unavailable"}).Error
	})
}
func (s *VideoStructureService) claim(ctx context.Context, id uint) (models.VideoStructureState, error) {
	var state models.VideoStructureState
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.First(&state, id).Error; err != nil {
				return err
			}
			if state.Status != "pending" && state.Status != "waiting_idle" {
				return ErrStructureObsolete
			}
			token := uuid.NewString()
			if err := structureChanged(structureCAS(tx, state).Where("status IN ?", []string{"pending", "waiting_idle"}).Updates(map[string]interface{}{"status": "running", "run_token": token, "revision": gorm.Expr("revision + 1"), "pause_reason": "", "error_code": ""})); err != nil {
				return err
			}
			state.Status = "running"
			state.RunToken = token
			state.Revision++
			return nil
		})
	})
	return state, err
}
func (s *VideoStructureService) readState(ctx context.Context, id uint) (models.VideoStructureState, error) {
	var state models.VideoStructureState
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error { return db.First(&state, id).Error })
	return state, err
}
func (s *VideoStructureService) verifyRunSource(ctx context.Context, state models.VideoStructureState) error {
	_, info, err := structureReadSource(ctx, state.VideoID)
	if err != nil {
		return err
	}
	if videoSourceVersion(state.VideoID, info.Size(), info.ModTime().UnixNano()) != state.SourceVersion {
		return ErrStructureObsolete
	}
	return nil
}

// ProcessWindow performs at most one analysis window, compaction or representative step.
func (s *VideoStructureService) ProcessWindow(ctx context.Context, id uint) error {
	initial, err := s.readState(ctx, id)
	if err != nil {
		return err
	}
	if err = s.Observe(ctx, initial.VideoID); err != nil {
		return err
	}
	state, err := s.claim(ctx, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.activeSource = state.SourceVersion
	s.activeVideo = state.VideoID
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.activeSource = ""; s.activeVideo = 0; s.mu.Unlock() }()
	if s.slot != nil {
		if err = s.slot.Acquire(ctx); err != nil {
			return s.failWindow(state, err)
		}
		defer s.slot.Release()
	}
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return s.failWindow(state, err)
	}
	defer unlock()
	video, info, err := structureReadSource(ctx, state.VideoID)
	if err != nil {
		return s.failWindow(state, err)
	}
	if videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano()) != state.SourceVersion {
		return s.failWindow(state, ErrStructureObsolete)
	}
	if !s.runtimeAvailable() {
		return s.failWindow(state, ErrStructureRuntimeUnavailable)
	}
	if state.DurationMS == 0 {
		if s.probe != nil {
			if err = s.probe.RefreshForSource(ctx, video.ID, state.SourceVersion); err != nil {
				return s.failWindow(state, err)
			}
			video, _, err = structureReadSource(ctx, video.ID)
			if err != nil {
				return s.failWindow(state, err)
			}
		}
		if video.Duration <= 0 || math.IsNaN(video.Duration) || math.IsInf(video.Duration, 0) {
			return s.failWindow(state, errors.New("structure_unknown_duration"))
		}
		state.DurationMS = int64(math.Round(video.Duration * 1000))
	}
	if state.Phase == "representatives" {
		err = s.processRepresentative(ctx, &state, video)
	} else if state.CheckpointMS >= state.DurationMS {
		err = s.compact(ctx, &state)
	} else {
		end := state.CheckpointMS + structureWindowMS
		if end > state.DurationMS {
			end = state.DurationMS
		}
		var frames []structureFrame
		frames, err = s.extract(ctx, video.Path, state.CheckpointMS, end)
		if err == nil && len(frames) == 0 && len(state.PreviousSample) == 0 {
			err = errors.New("structure_no_frames")
		}
		if err == nil {
			var result structureWindow
			boundaries, boundaryErr := structureEmbeddedBoundaries(ctx, state)
			if boundaryErr != nil {
				return s.failWindow(state, boundaryErr)
			}
			result, err = analyzeStructureWindow(state, frames, end, boundaries)
			if err == nil {
				err = s.checkpoint(ctx, &state, result, end)
			}
		}
	}
	if err != nil {
		return s.failWindow(state, err)
	}
	return nil
}
func (s *VideoStructureService) checkpoint(ctx context.Context, state *models.VideoStructureState, result structureWindow, end int64) error {
	if err := s.verifyRunSource(ctx, *state); err != nil {
		return err
	}
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var position int64
			if err := tx.Model(&models.VideoStructureSegment{}).Where("state_id = ? AND generation = ?", state.ID, state.Generation).Count(&position).Error; err != nil {
				return err
			}
			if len(result.Segments) == 1 && end == state.DurationMS && result.Segments[0].EndMS-result.Segments[0].StartMS < 5000 && position > 0 {
				var previous models.VideoStructureSegment
				if err := tx.Where("state_id = ? AND generation = ?", state.ID, state.Generation).Order("position DESC").First(&previous).Error; err != nil {
					return err
				}
				if err := tx.Model(&previous).Updates(map[string]interface{}{"end_ms": end, "score": 0}).Error; err != nil {
					return err
				}
				result.Segments = nil
			}
			for i := range result.Segments {
				result.Segments[i].StateID = state.ID
				result.Segments[i].Generation = state.Generation
				result.Segments[i].Position = int(position) + i
			}
			if len(result.Segments) > 0 {
				if err := tx.Create(&result.Segments).Error; err != nil {
					return err
				}
			}
			return structureChanged(structureCAS(tx, *state).Where("status = ?", "running").Updates(map[string]interface{}{"duration_ms": state.DurationMS, "checkpoint_ms": end, "pending_start_ms": result.PendingStartMS, "pending_frame_pts": result.PendingFramePTS, "previous_sample_pts": result.PreviousSamplePTS, "previous_sample": result.PreviousSample, "revision": gorm.Expr("revision + 1"), "run_token": "", "status": "pending"}))
		})
	})
}
func (s *VideoStructureService) compact(ctx context.Context, state *models.VideoStructureState) error {
	if err := s.verifyRunSource(ctx, *state); err != nil {
		return err
	}
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var boundaries []models.VideoStructureSegment
			if err := tx.Where("state_id = ? AND generation = ? AND end_ms < ?", state.ID, state.Generation, state.DurationMS).Order("score DESC, end_ms ASC").Limit(structureMaxSegments - 1).Find(&boundaries).Error; err != nil {
				return err
			}
			rows := compactStructureSegments(boundaries, state.DurationMS)
			for i := range rows {
				rows[i].StateID = state.ID
				rows[i].Generation = state.Generation
			}
			seeds, err := tx.Model(&models.VideoStructureSegment{}).Select("representative_ms").Where("state_id = ? AND generation = ?", state.ID, state.Generation).Order("position").Rows()
			if err != nil {
				return err
			}
			next := 0
			for seeds.Next() {
				var pts int64
				if err := seeds.Scan(&pts); err != nil {
					seeds.Close()
					return err
				}
				for next < len(rows) && pts >= rows[next].EndMS {
					next++
				}
				if next < len(rows) && pts >= rows[next].StartMS && !rows[next].RepresentativeReady {
					rows[next].RepresentativeMS = pts
					rows[next].RepresentativeReady = true
				}
			}
			seedError := seeds.Err()
			seeds.Close()
			if seedError != nil {
				return seedError
			}
			for i := range rows {
				if !rows[i].RepresentativeReady {
					return errors.New("structure_no_seed_frame")
				}
				rows[i].RepresentativeReady = false
			}
			if err := tx.Where("state_id = ? AND generation = ?", state.ID, state.Generation).Delete(&models.VideoStructureSegment{}).Error; err != nil {
				return err
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
			return structureChanged(structureCAS(tx, *state).Where("status = ?", "running").Updates(map[string]interface{}{"phase": "representatives", "revision": gorm.Expr("revision + 1"), "run_token": "", "status": "pending"}))
		})
	})
}
func (s *VideoStructureService) processRepresentative(ctx context.Context, state *models.VideoStructureState, video models.Video) error {
	var segment models.VideoStructureSegment
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Where("state_id = ? AND generation = ? AND representative_ready = ?", state.ID, state.Generation, false).Order("position").First(&segment).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.publish(ctx, state)
	}
	if err != nil {
		return err
	}
	bestPTS := int64(-1)
	bestScore := float64(-1)
	fallback := segment.RepresentativeMS
	for _, fraction := range []int64{20, 50, 80} {
		point := segment.StartMS + (segment.EndMS-segment.StartMS)*fraction/100
		end := point + 1000
		if end > segment.EndMS {
			end = segment.EndMS
		}
		frames, err := s.extract(ctx, video.Path, point, end)
		if err != nil {
			return err
		}
		if len(frames) == 0 {
			continue
		}
		frame := frames[0]
		if fallback < 0 || fraction == 50 {
			fallback = frame.PTS
		}
		score, black := structureFrameQuality(frame.Gray)
		if !black && score > bestScore {
			bestScore = score
			bestPTS = frame.PTS
		}
	}
	if bestPTS < 0 {
		bestPTS = fallback
	}
	if bestPTS < 0 {
		return errors.New("structure_no_representative")
	}
	if err := s.verifyRunSource(ctx, *state); err != nil {
		return err
	}
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.VideoStructureSegment{}).Where("id = ? AND generation = ?", segment.ID, state.Generation).Updates(map[string]interface{}{"representative_ms": bestPTS, "representative_ready": true}).Error; err != nil {
				return err
			}
			return structureChanged(structureCAS(tx, *state).Where("status = ?", "running").Updates(map[string]interface{}{"status": "pending", "run_token": "", "revision": gorm.Expr("revision + 1")}))
		})
	})
}
func (s *VideoStructureService) publish(ctx context.Context, state *models.VideoStructureState) error {
	if err := s.verifyRunSource(ctx, *state); err != nil {
		return err
	}
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var rows []models.VideoStructureSegment
			if err := tx.Where("state_id = ? AND generation = ?", state.ID, state.Generation).Order("position").Limit(structureMaxSegments + 1).Find(&rows).Error; err != nil {
				return err
			}
			if !validStructureSegments(rows, state.DurationMS) {
				return errors.New("structure_invalid_result")
			}
			if err := structureChanged(structureCAS(tx, *state).Where("status = ?", "running").Updates(map[string]interface{}{"published_generation": state.Generation, "published_source_version": state.SourceVersion, "published_algorithm_version": state.AlgorithmVersion, "status": "ready", "run_token": "", "previous_sample": nil, "revision": gorm.Expr("revision + 1"), "error_code": "", "pause_reason": ""})); err != nil {
				return err
			}
			return tx.Where("state_id = ? AND generation <> ?", state.ID, state.Generation).Delete(&models.VideoStructureSegment{}).Error
		})
	})
}
func (s *VideoStructureService) failWindow(state models.VideoStructureState, cause error) error {
	status, code := "failed", "structure_analysis_failed"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		status, code = "waiting_idle", ""
	} else if errors.Is(cause, ErrStructureRuntimeUnavailable) {
		status, code = "unavailable", "structure_runtime_unavailable"
	} else if errors.Is(cause, ErrStructureUnavailable) {
		status, code = "unavailable", "source_unavailable"
	} else if errors.Is(cause, ErrStructureObsolete) || errors.Is(cause, ErrMediaProbeSourceChanged) {
		status, code = "source_changed", "source_changed"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return structureChanged(structureCAS(db, state).Where("status = ?", "running").Updates(map[string]interface{}{"status": status, "error_code": code, "run_token": "", "revision": gorm.Expr("revision + 1")}))
	})
	if err != nil && !errors.Is(err, ErrStructureObsolete) {
		return errors.Join(cause, err)
	}
	return cause
}
func (s *VideoStructureService) Get(ctx context.Context, id uint) (*VideoStructureView, error) {
	if id == 0 || id > experienceMaxSafeInteger {
		return nil, errors.New("structure_invalid_video")
	}
	video, info, sourceErr := structureReadSource(ctx, id)
	if video.ID == 0 {
		return nil, ErrStructureUnavailable
	}
	var result *VideoStructureView
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var err error
			result, err = readVideoStructureView(tx, id, info, sourceErr)
			return err
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	return result, err
}
func (s *VideoStructureService) Retry(ctx context.Context, id uint, revision uint64) error {
	if _, _, err := structureReadSource(ctx, id); err != nil {
		return err
	}
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return structureChanged(db.Model(&models.VideoStructureState{}).Where("video_id = ? AND revision = ? AND status IN ?", id, revision, []string{"failed", "unavailable", "waiting_idle"}).Updates(map[string]interface{}{"status": "pending", "run_token": "", "revision": gorm.Expr("revision + 1"), "error_code": "", "pause_reason": ""}))
	})
}

func readVideoStructureView(tx *gorm.DB, id uint, info os.FileInfo, sourceErr error) (*VideoStructureView, error) {
	result := &VideoStructureView{VideoID: id, Status: "pending", SourceStatus: "unread", Segments: []VideoStructureSegmentView{}}
	var state models.VideoStructureState
	err := tx.Where("video_id = ?", id).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	result.Status = state.Status
	result.Revision = state.Revision
	result.CheckpointMS = state.CheckpointMS
	result.DurationMS = state.DurationMS
	result.ErrorCode = state.ErrorCode
	if state.PublishedGeneration == nil {
		return result, nil
	}
	result.Generation = *state.PublishedGeneration
	result.SourceVersion = state.PublishedSourceVersion
	result.AlgorithmVersion = state.PublishedAlgorithmVersion
	result.SourceStatus = "ready"
	if sourceErr != nil || info == nil {
		result.SourceStatus = "unavailable"
	} else if videoSourceVersion(id, info.Size(), info.ModTime().UnixNano()) != result.SourceVersion {
		result.SourceStatus = "changed"
	}
	var rows []models.VideoStructureSegment
	if err := tx.Where("state_id = ? AND generation = ?", state.ID, *state.PublishedGeneration).Order("position").Limit(structureMaxSegments).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result.Segments = append(result.Segments, VideoStructureSegmentView{Position: row.Position, StartMS: row.StartMS, EndMS: row.EndMS, RepresentativeMS: row.RepresentativeMS, Basis: row.Basis, Title: fmt.Sprintf("片段%02d", row.Position+1)})
	}
	return result, nil
}

func structureEmbeddedBoundaries(ctx context.Context, state models.VideoStructureState) ([]int64, error) {
	var metadata models.VideoTechnicalMetadata
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error { return db.First(&metadata, state.VideoID).Error })
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if metadata.ChaptersJSON == nil || metadata.ChaptersError != "" || metadata.SuccessfulSourceSize == nil || metadata.SuccessfulSourceModTimeNS == nil || videoSourceVersion(state.VideoID, *metadata.SuccessfulSourceSize, *metadata.SuccessfulSourceModTimeNS) != state.SourceVersion {
		return nil, nil
	}
	var chapters []ChapterInput
	if json.Unmarshal([]byte(*metadata.ChaptersJSON), &chapters) != nil {
		return nil, nil
	}
	var result []int64
	for _, chapter := range chapters {
		if chapter.StartMS > 0 && chapter.StartMS < state.DurationMS {
			result = append(result, chapter.StartMS)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}
