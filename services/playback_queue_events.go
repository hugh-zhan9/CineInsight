package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

const playEventSourceQueueView = "queue_view"

// ErrQueueEndIgnored tells the reporting player that its "ended" fact was not
// consumed as a natural end (a seek was still in flight or playback never
// started). It is not a failure; the player keeps its current view cycle.
var ErrQueueEndIgnored = errors.New("queue_end_ignored: 本次结束未被确认为自然结束")

// ErrQueueStaleEvent rejects a seq at or below the last accepted one for the
// current token, e.g. a remounted inline player restarting its count. The
// session is unchanged; the player must stop reporting and ask for a new play.
var ErrQueueStaleEvent = errors.New("queue_stale_event: 播放器已重新载入，请重新播放此项")

func (s *PlaybackQueueService) ReportInline(ctx context.Context, event InlineQueueEvent) error {
	return s.report(ctx, event, "inline")
}

// The adapter learns whether a natural end was consumed; other outcomes are
// already reflected in the runtime state.
func (s *PlaybackQueueService) reportPlayerEvent(event InlineQueueEvent) error {
	return s.report(context.Background(), event, "iina")
}
func (s *PlaybackQueueService) report(ctx context.Context, event InlineQueueEvent, player string) error {
	if event.Seq == 0 || event.Seq > 9007199254740991 || event.ViewCycle == 0 || event.ViewCycle > 9007199254740991 || event.Position < 0 || event.Duration < 0 || math.IsNaN(event.Position) || math.IsInf(event.Position, 0) || math.IsNaN(event.Duration) || math.IsInf(event.Duration, 0) {
		return ErrQueueInvalid
	}
	err := s.command(ctx, func(ctx context.Context) error {
		r := s.run
		if r == nil || r.failed || r.player != player || r.token != event.Token || r.lease == nil || r.lease.video.ID != event.VideoID || r.lease.sourceVersion() != event.SourceVersion {
			return ErrQueueChanged
		}
		if event.Seq <= r.seq {
			return ErrQueueStaleEvent
		}
		if event.ViewCycle != r.cycle {
			if !r.ended || event.Kind != "playing" || event.ViewCycle != r.cycle+1 {
				return ErrQueueChanged
			}
			r.cycle++
			r.ended = false
			r.played = 0
			r.viewAttempted = false
			r.viewRecorded = false
			r.lastSaved = time.Time{}
		}
		switch event.Kind {
		case "loaded", "playing", "progress", "paused", "seeking", "seeked", "ended", "error":
		default:
			return ErrQueueInvalid
		}
		r.seq = event.Seq
		now := s.now()
		if event.Duration > 0 {
			r.duration = event.Duration
		}
		if event.Kind == "error" {
			return s.failLocked(ctx, "queue_media_failed", event.Message)
		}
		if event.Kind == "loaded" {
			r.loaded = true
			return nil
		}
		if !r.loaded {
			return ErrQueueInvalid
		}
		if event.Kind == "seeking" {
			s.sampleQueueView(r, r.position, now, true)
			r.seeking = true
			r.afterSeek = true
			r.playing = false
			r.position = event.Position
			return nil
		}
		if event.Kind == "seeked" {
			// A completed seek ends the seek guard even when the element stays
			// paused (no "playing" follows); accumulation resumes only on playing.
			r.seeking = false
			r.afterSeek = true
			r.seekLanding = event.Position
			r.position = event.Position
			return nil
		}
		if event.Kind == "playing" {
			if r.ended {
				return ErrQueueChanged
			}
			r.seeking = false
			r.playing = true
			r.position = event.Position
			r.tracker.advance(r.viewKey(), r.position, now, false)
			if !r.started {
				r.started = true
				factCtx, cancel := playbackFactContext(ctx)
				err := recordFormalPlaybackStatsContext(factCtx, &r.lease.video, map[string]interface{}{"play_count": gorm.Expr("play_count + 1"), "last_played_at": now, "is_stale": false, "stale_reason": ""}, now, models.PlayEventSourceDesktopPlay)
				cancel()
				if err != nil {
					s.warning = "播放已开始，但启动统计未记录：" + queueSafeError(err)
				}
			}
			return s.statusLocked(ctx, "playing", false)
		}
		// Only playback observed moving past the landing point clears a seek;
		// a "playing" at the landing itself is not playback into the end.
		if r.afterSeek && r.playing && event.Position > r.seekLanding {
			r.afterSeek = false
		}
		if event.Kind == "ended" && (r.seeking || r.afterSeek || !r.started) && !r.ended {
			return ErrQueueEndIgnored
		}
		if event.Kind == "ended" && r.ended {
			return nil
		}
		if r.ended {
			return nil
		}
		ended := event.Kind == "ended"
		played := s.sampleQueueView(r, event.Position, now, event.Kind == "paused" || ended)
		r.position = event.Position
		if !r.viewAttempted && (played >= viewThreshold(r.duration) || ended) {
			r.viewAttempted = true
			factCtx, cancel := playbackFactContext(ctx)
			_, err := viewEvents.recordContext(factCtx, r.lease.video.ID, playEventSourceQueueView, r.viewKey(), now)
			cancel()
			r.viewRecorded = err == nil
			if err != nil {
				s.warning = "有效观看未记录：" + queueSafeError(err)
				s.changed()
			}
		}
		force := ended || event.Kind == "paused"
		if r.started && (force || r.lastSaved.IsZero() || now.Sub(r.lastSaved) >= 10*time.Second) {
			if err := s.saveProgress(ctx, r, ended); err != nil {
				s.warning = "观看进度未保存：" + queueSafeError(err)
				s.changed()
			}
		}
		if event.Kind == "paused" {
			r.playing = false
			return s.statusLocked(ctx, "paused", false)
		}
		if ended {
			r.ended = true
			r.playing = false
			if err := s.statusLocked(ctx, "ended", true); err != nil {
				return err
			}
			state, err := readQueueState(ctx)
			if err != nil {
				return err
			}
			if state.Autoplay && state.Player != "system" {
				err = s.nextLocked(ctx, state)
				if err != nil && !errors.Is(err, ErrQueueAtEnd) {
					return err
				}
			}
		}
		return nil
	})
	s.runtimeFailure(event.Token, err)
	return err
}
func (r *playbackQueueRun) viewKey() string { return fmt.Sprintf("%s:%d", r.token, r.cycle) }
func (s *PlaybackQueueService) sampleQueueView(r *playbackQueueRun, position float64, now time.Time, finish bool) float64 {
	if !r.playing {
		return r.played
	}
	segment := r.tracker.advance(r.viewKey(), position, now, finish)
	if finish {
		r.played += segment
		return r.played
	}
	return r.played + segment
}
func (s *PlaybackQueueService) saveProgress(ctx context.Context, r *playbackQueueRun, completed bool) error {
	r.lastSaved = s.now()
	_, err := s.video.UpdateVideoWatchProgressContext(ctx, r.lease.video.ID, r.position, r.duration, completed, r.lease.origin)
	return err
}
