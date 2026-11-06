package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
)

func (s *VideoStructureService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}
func (s *VideoStructureService) SetEventEmitter(emitter func(VideoStructureStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}
func (s *VideoStructureService) SetActivityCheck(check func(context.Context) (bool, string)) {
	s.mu.Lock()
	s.activity = check
	s.mu.Unlock()
}
func (s *VideoStructureService) Status() VideoStructureStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}
func (s *VideoStructureService) emitStatus() {
	s.mu.Lock()
	status, emit := s.status, s.emitter
	s.mu.Unlock()
	if emit != nil {
		emit(status)
	}
}
func (s *VideoStructureService) refreshCounts(ctx context.Context) {
	var counts []struct {
		Status string
		Count  int64
	}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		visible, err := visibleVideoQuery(db.Model(&models.Video{}).Select("videos.id"))
		if err != nil {
			return err
		}
		return db.Model(&models.VideoStructureState{}).Where("video_id IN (?)", visible).Select("status, count(*) AS count").Group("status").Scan(&counts).Error
	})
	if err != nil {
		return
	}
	s.mu.Lock()
	s.status.Pending = 0
	s.status.Ready = 0
	s.status.Failed = 0
	s.status.Unavailable = 0
	for _, count := range counts {
		switch count.Status {
		case "ready":
			s.status.Ready += count.Count
		case "failed":
			s.status.Failed += count.Count
		case "unavailable":
			s.status.Unavailable += count.Count
		default:
			s.status.Pending += count.Count
		}
	}
	s.mu.Unlock()
	s.emitStatus()
}
func (s *VideoStructureService) RequestDiscovery() {
	s.mu.Lock()
	s.scanRequested = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *VideoStructureService) Wake() {
	s.mu.Lock()
	cancel := s.windowCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.RequestDiscovery()
}
func (s *VideoStructureService) Start(parent context.Context) error {
	s.mu.Lock()
	if s.done != nil {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.stopped = false
	done := s.done
	s.mu.Unlock()
	go func() { defer close(done); s.loop(ctx) }()
	return nil
}
func (s *VideoStructureService) StopAndWait(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	drained := make(chan struct{})
	go func() {
		if done != nil {
			<-done
		}
		s.sourceCallbacks.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		s.mu.Lock()
		if s.done == done {
			s.done = nil
			s.cancel = nil
		}
		s.mu.Unlock()
		return nil
	case <-ctx.Done():
		return fmt.Errorf("structure_stop_pending: 自动分段尚未停止: %w", ctx.Err())
	}

}
func (s *VideoStructureService) allowed(ctx context.Context) (bool, string) {
	s.mu.Lock()
	check, stopped := s.activity, s.stopped
	s.mu.Unlock()
	if stopped {
		return false, "disabled"
	}
	if check == nil {
		return false, "probe_unavailable"
	}
	checkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return check(checkCtx)
}
func (s *VideoStructureService) wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-timer.C:
		return true
	}
}
func (s *VideoStructureService) next(ctx context.Context, newStreak int) (models.VideoStructureState, bool, error) {
	var state models.VideoStructureState
	var found bool
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var settings models.Settings
		if err := db.Select("auto_structure_initial_cutoff_id").First(&settings).Error; err != nil {
			return err
		}
		cutoff := uint(0)
		if settings.AutoStructureInitialCutoffID != nil {
			cutoff = *settings.AutoStructureInitialCutoffID
		}
		choose := func(old bool) error {
			query := db.Where("status IN ?", []string{"pending", "waiting_idle", "source_changed"})
			if old {
				query = query.Where("video_id <= ?", cutoff)
			} else {
				query = query.Where("video_id > ?", cutoff)
			}
			return query.Order("updated_at ASC, id ASC").First(&state).Error
		}
		old := newStreak >= 3
		err := choose(old)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = choose(!old)
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return state, found, err
}
func (s *VideoStructureService) idleOperation(ctx context.Context, operation func(context.Context) error) error {
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.windowCancel = cancel
	s.mu.Unlock()
	monitorDone := make(chan struct{})
	var reasonMu sync.Mutex
	reason := ""
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				allowed, why := s.allowed(runCtx)
				if !allowed {
					reasonMu.Lock()
					reason = why
					reasonMu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	err := operation(runCtx)
	cancel()
	<-monitorDone
	s.mu.Lock()
	s.windowCancel = nil
	s.mu.Unlock()
	reasonMu.Lock()
	why := reason
	reasonMu.Unlock()
	if err != nil && ctx.Err() == nil && why == "" {
		if allowed, latest := s.allowed(ctx); !allowed {
			why = latest
		}
	}
	if why != "" {
		s.mu.Lock()
		videoID := s.status.CurrentVideoID
		s.mu.Unlock()
		if videoID != 0 {
			_ = database.WithOperationContext(ctx, func(db *gorm.DB) error {
				return db.Model(&models.VideoStructureState{}).Where("video_id = ? AND status = ? AND run_token = ?", videoID, "waiting_idle", "").Updates(map[string]interface{}{"pause_reason": why, "revision": gorm.Expr("revision + 1")}).Error
			})
		}
	}
	if why != "" {
		s.mu.Lock()
		s.status.WaitingIdle = true
		s.status.Reason = why
		s.mu.Unlock()
	}
	return err
}
func (s *VideoStructureService) loop(ctx context.Context) {
	if err := s.Recover(ctx); err != nil {
		s.mu.Lock()
		s.status.Error = "自动分段恢复失败"
		s.mu.Unlock()
		s.emitStatus()
		return
	}
	discover := true
	lastDiscovery := time.Time{}
	newStreak := 3
	for ctx.Err() == nil {
		s.mu.Lock()
		requested := s.scanRequested
		s.scanRequested = false
		s.mu.Unlock()
		if requested {
			discover = true
		}
		allowed, reason := s.allowed(ctx)
		if !allowed {
			s.mu.Lock()
			s.status.Running = false
			s.status.WaitingIdle = true
			s.status.Reason = reason
			s.mu.Unlock()
			s.refreshCounts(ctx)
			if !s.wait(ctx, 5*time.Second) {
				return
			}
			continue
		}
		s.mu.Lock()
		s.status.WaitingIdle = false
		s.status.Reason = ""
		s.status.Error = ""
		s.mu.Unlock()
		if discover || s.now().Sub(lastDiscovery) > 5*time.Minute {
			err := s.idleOperation(ctx, s.Discover)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if !errors.Is(err, context.Canceled) {
					s.mu.Lock()
					s.status.Error = "自动分段发现未完成"
					s.mu.Unlock()
				}
				s.refreshCounts(ctx)
				if !s.wait(ctx, 5*time.Second) {
					return
				}
				continue
			}
			discover = false
			lastDiscovery = s.now()
			s.refreshCounts(ctx)
		}
		state, found, err := s.next(ctx, newStreak)
		if err != nil {
			s.mu.Lock()
			s.status.Error = "自动分段队列读取失败"
			s.mu.Unlock()
			if !s.wait(ctx, 5*time.Second) {
				return
			}
			continue
		}
		if !found {
			if !s.wait(ctx, 10*time.Second) {
				return
			}
			continue
		}
		s.mu.Lock()
		s.status.Running = true
		s.status.CurrentVideoID = state.VideoID
		registry := s.registry
		s.mu.Unlock()
		s.emitStatus()
		if registry != nil {
			registry.Begin(BackgroundTaskVideoStructure)
		}
		err = s.idleOperation(ctx, func(run context.Context) error { return s.ProcessWindow(run, state.ID) })
		if registry != nil {
			registry.End(BackgroundTaskVideoStructure)
		}
		s.mu.Lock()
		s.status.Running = false
		s.status.CurrentVideoID = 0
		s.mu.Unlock()
		s.refreshCounts(ctx)
		if err != nil && ctx.Err() != nil {
			return
		}
		var cutoff uint
		_ = database.WithOperationContext(ctx, func(db *gorm.DB) error {
			var settings models.Settings
			if err := db.Select("auto_structure_initial_cutoff_id").First(&settings).Error; err != nil {
				return err
			}
			if settings.AutoStructureInitialCutoffID != nil {
				cutoff = *settings.AutoStructureInitialCutoffID
			}
			return nil
		})
		if state.VideoID <= cutoff {
			newStreak = 0
		} else {
			newStreak++
		}
		select {
		case <-s.wake:
			discover = true
		default:
		}
	}
}
