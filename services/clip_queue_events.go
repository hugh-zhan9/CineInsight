package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"math"
	"video-master/database"
	"video-master/models"
)

func (s *PlaybackQueueService) failClipLocked(ctx context.Context, message string) error {
	if err := s.failLocked(ctx, "queue_source_changed", message); err != nil {
		return err
	}
	return ErrQueueChanged
}

// reportClipLocked deliberately never enters the formal-view tracker or writers.
func (s *PlaybackQueueService) reportClipLocked(ctx context.Context, r *playbackQueueRun, event InlineQueueEvent) error {
	if event.ViewCycle != r.cycle {
		return ErrQueueChanged
	}
	switch event.Kind {
	case "loaded", "playing", "progress", "paused", "seeking", "seeked", "ended", "error":
	default:
		return ErrQueueInvalid
	}
	if err := validateClipLease(ctx, r.lease, r.entry); err != nil {
		return s.failClipLocked(ctx, "原片或预览范围已不可用，请重新核对")
	}
	if err := validateQueueIINASource(ctx, r.lease); err != nil {
		return s.failClipLocked(ctx, "原片已变化，请重新核对")
	}
	r.seq = event.Seq
	start, end := float64(*r.entry.ClipStartMS)/1000, float64(*r.entry.ClipEndMS)/1000
	if event.Kind == "error" {
		return s.failClipLocked(ctx, "当前预览区间无法播放，请检查原片")
	}
	if event.Duration > 0 && event.Duration < end {
		return s.failClipLocked(ctx, "原片提前结束或区间越界，请重新核对")
	}
	if event.Kind == "loaded" {
		if event.Duration <= 0 {
			return s.failClipLocked(ctx, "无法确定当前片长，请重新核对")
		}
		r.loaded = true
		r.duration = event.Duration
		r.position = start
		return nil
	}
	if !r.loaded {
		return ErrQueueInvalid
	}
	r.position = math.Max(start, math.Min(end, event.Position))
	if event.Kind == "ended" && event.Position < end {
		return s.failClipLocked(ctx, "原片在预览终点前结束，请重新核对")
	}
	if r.position >= end {
		return s.finishClipLocked(ctx, r)
	}
	switch event.Kind {
	case "playing":
		r.started = true
		r.playing = true
		r.seeking = false
		return s.statusLocked(ctx, "playing", false)
	case "paused":
		r.playing = false
		return s.statusLocked(ctx, "paused", false)
	case "seeking":
		r.seeking = true
		r.playing = false
	case "seeked":
		r.seeking = false
	}
	return nil
}
func (s *PlaybackQueueService) finishClipLocked(ctx context.Context, r *playbackQueueRun) error {
	if s.run != r || r.entry.EntryKind != "clip" {
		return ErrQueueChanged
	}
	r.ended = true
	r.playing = false
	state, err := readQueueState(ctx)
	if err != nil {
		return err
	}
	if state.ActiveToken != r.token {
		return ErrQueueChanged
	}
	if *r.entry.PreviewPosition < *r.entry.PreviewCount {
		var next models.PlaybackQueueEntry
		err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
			return db.Where("preview_batch_id = ? AND preview_position = ?", *r.entry.PreviewBatchID, *r.entry.PreviewPosition+1).First(&next).Error
		})
		if err == nil && validQueueEntryShape(next) && next.EntryKind == "clip" && *next.PreviewCount == *r.entry.PreviewCount {
			return s.playLocked(ctx, next.ID, state.Revision)
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return s.stopClipBatchLocked(ctx, r, state, "clip_batch_changed", "预览列表已变化，请重新选择")
	}
	return s.stopClipBatchLocked(ctx, r, state, "", "")
}
func (s *PlaybackQueueService) stopClipBatchLocked(ctx context.Context, r *playbackQueueRun, state models.PlaybackQueueState, code, message string) error {
	s.stoppedToken = r.token
	s.terminalToken = r.token
	if err := s.stopRuntimeLocked(false); err != nil {
		return err
	}
	err := transitionQueue(ctx, state.Revision, map[string]any{"active_token": "", "status": "ended", "last_error_code": code, "last_error_message": message})
	s.changed()
	return err
}
