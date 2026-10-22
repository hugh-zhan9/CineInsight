package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const PlayEventSourceDesktopControlledView = "desktop_controlled_view"

type controlledDispatchBypassKey struct{}

var controlledOwnership struct {
	sync.Mutex
	desktop, queue string
}

func claimControlledDesktop(token string) error {
	controlledOwnership.Lock()
	defer controlledOwnership.Unlock()
	if controlledOwnership.queue != "" {
		return errors.New("请先停止正在播放的队列")
	}
	if controlledOwnership.desktop != "" {
		return errors.New("上次精确同步播放尚未停止，请重试停止播放")
	}
	controlledOwnership.desktop = token
	return nil
}
func releaseControlledDesktop(token string) {
	controlledOwnership.Lock()
	defer controlledOwnership.Unlock()
	if controlledOwnership.desktop == token {
		controlledOwnership.desktop = ""
	}
}
func claimControlledQueue(token string) error {
	controlledOwnership.Lock()
	defer controlledOwnership.Unlock()
	if controlledOwnership.desktop != "" {
		return errors.New("请先停止 IINA 精确同步播放")
	}
	controlledOwnership.queue = token
	return nil
}
func releaseControlledQueue(token string) {
	controlledOwnership.Lock()
	defer controlledOwnership.Unlock()
	if controlledOwnership.queue == token {
		controlledOwnership.queue = ""
	}
}

type ControlledPlaybackState struct {
	Status  string `json:"status"`
	VideoID uint   `json:"video_id"`
	Message string `json:"message"`
}

type controlledPlayback struct {
	reservation string
	state       ControlledPlaybackState
	emit        func(ControlledPlaybackState)
	admission   formalPlaybackAdmission
	commands    contextMutex
	player      queueControlledPlayer
	run         *controlledViewRun
	video       *VideoService
	now         func() time.Time
}
type controlledViewRun struct {
	stopping                                              bool
	token                                                 string
	lease                                                 *queueMediaLease
	seq, cycle                                            uint64
	playing, started, seeking, afterSeek, ended, recorded bool
	position, duration, seekLanding, played               float64
	tracker                                               jellyfinViewTracker
	lastSaved                                             time.Time
}

func (r *controlledViewRun) key() string { return fmt.Sprintf("%s:%d", r.token, r.cycle) }
func (s *VideoService) controlledPlayer() *controlledPlayback {
	s.controlledMu.Lock()
	defer s.controlledMu.Unlock()
	if s.controlled == nil {
		s.controlled = &controlledPlayback{video: s, player: NewControlledIINAPlayer(), now: time.Now}
	}
	return s.controlled
}
func (s *VideoService) startControlledPlayback(parent context.Context, id uint) error {
	owner := s.controlledPlayer()
	ctx, done, err := owner.admission.begin(parent)
	if err != nil {
		return err
	}
	defer done()
	if err = owner.commands.LockContext(ctx); err != nil {
		return err
	}
	defer owner.commands.Unlock()
	if ok, reason := owner.player.Available(); !ok {
		return errors.New(reason)
	}
	token, err := newRandomRerollToken()
	if err != nil {
		return err
	}
	// Retire the previous process while its reservation still excludes queue admission.
	if err := owner.stopLocked(); err != nil {
		return err
	}
	if err = claimControlledDesktop(token); err != nil {
		return err
	}
	owner.reservation = token
	success := false
	defer func() {
		if !success {
			cleanupErr := owner.stopLocked()
			owner.state = ControlledPlaybackState{Status: "failed", VideoID: id, Message: "IINA 精确同步未能启动，请检查原片和播放器"}
			if cleanupErr != nil {
				owner.state.Message = "IINA 启动未完成，专用进程尚未清理，请重试停止播放"
			}
			owner.changed()
		}
	}()
	owner.state = ControlledPlaybackState{Status: "starting", VideoID: id}
	owner.changed()
	lease, err := s.prepareQueueMedia(ctx, id, false)
	if err != nil {
		return err
	}
	run := &controlledViewRun{token: token, lease: lease, cycle: 1, position: lease.start, duration: lease.video.Duration}
	owner.run = run
	if err = owner.player.Load(ctx, lease, token, owner.report); err != nil {
		return err
	}
	if err = owner.player.Control(ctx, "resume"); err != nil {
		return err
	}
	success = true
	return nil
}
func (o *controlledPlayback) changed() {
	if o.emit != nil {
		o.emit(o.state)
	}
}
func (s *VideoService) SetControlledPlaybackListener(emit func(ControlledPlaybackState)) {
	o := s.controlledPlayer()
	o.commands.Lock()
	defer o.commands.Unlock()
	o.emit = emit
}
func (s *VideoService) GetControlledPlaybackState() ControlledPlaybackState {
	o := s.controlledPlayer()
	o.commands.Lock()
	defer o.commands.Unlock()
	return o.state
}
func (o *controlledPlayback) stopLocked() error {
	run := o.run
	var saveErr error
	if run != nil {
		run.stopping = true
		if run.started && !run.ended {
			ctx, cancel := playbackFactContext(context.Background())
			if validateQueueIINASource(ctx, run.lease) == nil {
				_, saveErr = o.video.UpdateVideoWatchProgressContext(ctx, run.lease.video.ID, run.position, run.duration, false, run.lease.origin)
			}
			cancel()
		}
	}
	closeErr := o.player.Close()
	if closeErr == nil {
		o.run = nil
		if run != nil {
			run.lease.Close()
		}
		releaseControlledDesktop(o.reservation)
		o.reservation = ""
	}
	err := errors.Join(saveErr, closeErr)
	o.state.Status = "stopped"
	if err != nil {
		o.state.Status = "failed"
		o.state.Message = "精确同步停止时未能完成进度保存或进程清理，请重试。"
	}
	o.changed()
	return err
}
func (s *VideoService) StopControlledPlayback() error {
	owner := s.controlledPlayer()
	owner.commands.Lock()
	defer owner.commands.Unlock()
	return owner.stopLocked()
}

// QuiesceControlledPlayback closes admission, cancels handshakes and drains all database callbacks.
// The returned release is only called after a cancelled maintenance restores database access.
func (s *VideoService) QuiesceControlledPlayback() (func(), error) {
	owner := s.controlledPlayer()
	wait, release := owner.admission.quiesce()
	wait()
	owner.commands.Lock()
	err := owner.stopLocked()
	owner.commands.Unlock()
	return release, err
}
func (o *controlledPlayback) report(event InlineQueueEvent) error {
	ctx, done, err := o.admission.begin(context.Background())
	if err != nil {
		return err
	}
	defer done()
	if err = o.commands.LockContext(ctx); err != nil {
		return err
	}
	defer o.commands.Unlock()
	r := o.run
	if r == nil || r.stopping || r.token != event.Token || r.lease.video.ID != event.VideoID || r.lease.sourceVersion() != event.SourceVersion {
		return ErrQueueChanged
	}
	if event.Seq <= r.seq {
		return ErrQueueStaleEvent
	}
	if event.Seq == 0 || event.ViewCycle == 0 || event.Position < 0 || event.Duration < 0 || math.IsNaN(event.Position) || math.IsInf(event.Position, 0) || math.IsNaN(event.Duration) || math.IsInf(event.Duration, 0) {
		return ErrQueueInvalid
	}
	if event.ViewCycle != r.cycle {
		if !r.ended || event.Kind != "playing" || event.ViewCycle != r.cycle+1 {
			return ErrQueueChanged
		}
		r.cycle++
		r.ended = false
		r.recorded = false
		r.played = 0
		r.tracker = jellyfinViewTracker{}
		r.lastSaved = time.Time{}
	}
	r.seq = event.Seq
	now := o.now()
	if event.Duration > 0 {
		r.duration = event.Duration
	}
	if event.Kind == "error" {
		o.stopLocked()
		o.state.Status = "failed"
		o.state.Message = "IINA 精确同步已停止：" + queueSafeMessage(event.Message)
		o.changed()
		return nil
	}
	if err := validateQueueIINASource(ctx, r.lease); err != nil {
		o.stopLocked()
		o.state.Status = "failed"
		o.state.Message = "播放源已改变，精确同步已停止"
		o.changed()
		return err
	}
	switch event.Kind {
	case "loaded":
		return nil
	case "seeking":
		o.sample(r, r.position, now, true)
		r.seeking = true
		r.afterSeek = true
		r.playing = false
		r.position = event.Position
		return nil
	case "seeked":
		r.seeking = false
		r.afterSeek = true
		r.seekLanding = event.Position
		r.position = event.Position
		return nil
	case "playing":
		if r.ended {
			return ErrQueueChanged
		}
		r.started = true
		r.playing = true
		r.seeking = false
		r.position = event.Position
		r.tracker.advance(r.key(), r.position, now, false)
		o.state.Status = "playing"
		o.changed()
		return nil
	case "progress", "paused", "ended":
	default:
		return ErrQueueInvalid
	}
	if r.afterSeek && r.playing && event.Position > r.seekLanding {
		r.afterSeek = false
	}
	ended := event.Kind == "ended"
	if ended && (r.seeking || r.afterSeek || !r.started) {
		return ErrQueueEndIgnored
	}
	if r.ended {
		return nil
	}
	played := o.sample(r, event.Position, now, ended || event.Kind == "paused")
	r.position = event.Position
	if !r.recorded && (played >= viewThreshold(r.duration) || ended) {
		factCtx, cancel := playbackFactContext(ctx)
		_, err := viewEvents.recordContext(factCtx, r.lease.video.ID, PlayEventSourceDesktopControlledView, r.key(), now)
		cancel()
		if err != nil {
			return err
		}
		r.recorded = true
	}
	if r.started && (ended || event.Kind == "paused" || r.lastSaved.IsZero() || now.Sub(r.lastSaved) >= 10*time.Second) {
		if _, err := o.video.UpdateVideoWatchProgressContext(ctx, r.lease.video.ID, r.position, r.duration, ended, r.lease.origin); err != nil {
			return err
		}
		r.lastSaved = now
	}
	if ended {
		r.ended = true
		r.playing = false
		o.state.Status = "ended"
		o.changed()
	}
	if event.Kind == "paused" {
		r.playing = false
		o.state.Status = "paused"
		o.changed()
	}
	return nil
}
func (o *controlledPlayback) sample(r *controlledViewRun, position float64, now time.Time, finish bool) float64 {
	if !r.playing {
		return r.played
	}
	segment := r.tracker.advance(r.key(), position, now, finish)
	if finish {
		r.played += segment
		return r.played
	}
	return r.played + segment
}
