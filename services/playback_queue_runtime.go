package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"video-master/database"
	"video-master/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var queueEventSequence atomic.Uint64

var ErrQueueAtEnd = errors.New("queue_at_end: 已到待播队列末尾")

// Player adapters never own ordering, counters, or database handles. Load must
// return paused after its identity handshake; Resume is an explicit later step.
type queueControlledPlayer interface {
	Available() (bool, string)
	Load(context.Context, *queueMediaLease, string, func(InlineQueueEvent) error) error
	Control(context.Context, string) error
	Stop(context.Context) error
	Close() error
}

type QueueCapability struct {
	Player    string `json:"player"`
	Available bool   `json:"available"`
	Autoplay  bool   `json:"autoplay"`
	Reason    string `json:"reason"`
}
type QueueSession struct {
	Token           string  `json:"token"`
	VideoID         uint    `json:"video_id"`
	SourceVersion   string  `json:"source_version"`
	Locator         string  `json:"locator"`
	MIME            string  `json:"mime"`
	StartSeconds    float64 `json:"start_seconds"`
	Duration        float64 `json:"duration"`
	ViewCycle       uint64  `json:"view_cycle"`
	DesiredPaused   bool    `json:"desired_paused"`
	ControlSequence uint64  `json:"control_sequence"`
}
type QueueSnapshot struct {
	QueuePage
	Sequence     uint64            `json:"sequence"`
	Session      *QueueSession     `json:"session"`
	Capabilities []QueueCapability `json:"capabilities"`
	Warning      string            `json:"warning"`
}
type InlineQueueEvent struct {
	Token         string  `json:"token"`
	VideoID       uint    `json:"video_id"`
	SourceVersion string  `json:"source_version"`
	Seq           uint64  `json:"seq"`
	ViewCycle     uint64  `json:"view_cycle"`
	Kind          string  `json:"kind"`
	Position      float64 `json:"position"`
	Duration      float64 `json:"duration"`
	Message       string  `json:"message"`
}

type playbackQueueRun struct {
	controlSequence                                                 uint64
	token                                                           string
	entryID                                                         uint
	player                                                          string
	cancel                                                          context.CancelFunc
	prepared                                                        chan struct{}
	lease                                                           *queueMediaLease
	seq                                                             uint64
	cycle                                                           uint64
	loaded, started, playing, seeking, ended, desiredPaused, failed bool
	viewRecorded, viewAttempted                                     bool
	// afterSeek: since the last seek, playback has not yet been seen moving
	// past seekLanding while playing; an end in that state was reached by
	// seeking, not by playback, and is never a natural end.
	afterSeek                  bool
	seekLanding                float64
	position, duration, played float64
	lastSaved                  time.Time
	tracker                    jellyfinViewTracker
}

type PlaybackQueueService struct {
	admission     formalPlaybackAdmission
	commands      contextMutex
	video         *VideoService
	player        queueControlledPlayer
	run           *playbackQueueRun // owned by commands
	terminalToken string
	sequence      uint64
	stoppedToken  string
	warning       string
	emit          func(uint64)
	now           func() time.Time
	closeOnce     sync.Once
	closeErr      error
}

func NewPlaybackQueueService(video *VideoService, player queueControlledPlayer, emit func(uint64)) *PlaybackQueueService {
	return &PlaybackQueueService{video: video, player: player, emit: emit, now: time.Now}
}
func (s *PlaybackQueueService) command(parent context.Context, fn func(context.Context) error) error {
	ctx, done, err := s.admission.begin(parent)
	if err != nil {
		return err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.commands.LockContext(ctx); err != nil {
		return err
	}
	defer s.commands.Unlock()
	return fn(ctx)
}
func (s *PlaybackQueueService) Initialize(ctx context.Context) error {
	return s.command(ctx, func(ctx context.Context) error {
		if err := initializePlaybackQueue(ctx); err != nil {
			return err
		}
		s.changed()
		return nil
	})
}
func (s *PlaybackQueueService) changed() {
	s.sequence = queueEventSequence.Add(1)
	if s.emit != nil {
		s.emit(s.sequence)
	}
}
func (s *PlaybackQueueService) capabilities() []QueueCapability {
	available, reason := false, "仅 macOS 安装 IINA 后可用"
	if s.player != nil {
		available, reason = s.player.Available()
	}
	return []QueueCapability{{Player: "system", Available: true}, {Player: "inline", Available: true, Autoplay: true}, {Player: "iina", Available: available, Autoplay: true, Reason: reason}}
}
func (s *PlaybackQueueService) Snapshot(ctx context.Context, query QueueQuery) (*QueueSnapshot, error) {
	var result *QueueSnapshot
	err := s.command(ctx, func(ctx context.Context) error {
		page, err := loadQueuePage(ctx, query)
		if err != nil {
			return err
		}
		result = &QueueSnapshot{QueuePage: *page, Sequence: s.sequence, Capabilities: s.capabilities(), Warning: s.warning}
		if r := s.run; r != nil && r.failed && r.token == page.State.ActiveToken {
			result.State.Status = "failed"
		}
		if s.terminalToken != "" && page.State.ActiveToken == s.terminalToken {
			result.State.Status = "stopped"
			result.State.ActiveToken = ""
		}
		if r := s.run; r != nil && !r.failed && r.lease != nil && r.player == "inline" && page.State.ActiveToken == r.token {
			m := r.lease
			result.Session = &QueueSession{Token: r.token, VideoID: m.video.ID, SourceVersion: m.sourceVersion(), Locator: "/preview/queue/" + r.token, MIME: m.mime, StartSeconds: m.start, Duration: m.video.Duration, ViewCycle: r.cycle, DesiredPaused: r.desiredPaused, ControlSequence: r.controlSequence}
		}
		return nil
	})
	return result, err
}
func readQueueState(ctx context.Context) (models.PlaybackQueueState, error) {
	var state models.PlaybackQueueState
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error { var err error; state, err = queueStateFrom(db); return err })
	return state, err
}
func transitionQueue(ctx context.Context, expected uint64, updates map[string]any) error {
	return database.TransactionWithContext(ctx, func(db *gorm.DB) error {
		if err := claimQueueRevision(db, expected); err != nil {
			return err
		}
		return db.Model(&models.PlaybackQueueState{}).Where("id = 1").Updates(updates).Error
	})
}

func (s *PlaybackQueueService) Play(ctx context.Context, entryID uint, revision uint64) error {
	return s.command(ctx, func(ctx context.Context) error { return s.playLocked(ctx, entryID, revision) })
}
func (s *PlaybackQueueService) playLocked(ctx context.Context, entryID uint, revision uint64) error {
	if entryID == 0 {
		return ErrQueueInvalid
	}
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	var state models.PlaybackQueueState
	var entry models.PlaybackQueueEntry
	err := database.TransactionWithContext(ctx, func(db *gorm.DB) error {
		if err := claimQueueRevision(db, revision); err != nil {
			return err
		}
		var err error
		state, err = queueStateFrom(db)
		if err != nil {
			return err
		}
		if state.Player == "iina" {
			if s.player == nil {
				return errors.New("queue_player_unavailable: IINA 不可用")
			}
			ok, reason := s.player.Available()
			if !ok {
				return fmt.Errorf("queue_player_unavailable: %s", reason)
			}
		}
		if err := db.First(&entry, entryID).Error; err != nil {
			return err
		}
		return db.Model(&models.PlaybackQueueState{}).Where("id = 1").Updates(map[string]any{"current_entry_id": entry.ID, "active_token": token, "status": "starting", "last_error_code": "", "last_error_message": ""}).Error
	})
	if err != nil {
		return err
	}
	s.stopRuntimeLocked(true)
	s.warning = ""
	prepCtx, prepDone, err := s.admission.begin(context.Background())
	if err != nil {
		return err
	}
	prepCtx, cancel := context.WithTimeout(prepCtx, 30*time.Second)
	r := &playbackQueueRun{token: token, entryID: entryID, player: state.Player, cancel: cancel, prepared: make(chan struct{}), cycle: 1}
	s.run = r
	s.terminalToken = ""
	s.stoppedToken = ""
	s.changed()
	go func() {
		defer prepDone()
		defer cancel()
		var lease *queueMediaLease
		var attempt *PlaybackAttemptResult
		var prepareErr error
		if r.player == "system" {
			attempt, prepareErr = s.video.PlayVideoContext(prepCtx, entry.VideoID)
		} else {
			lease, prepareErr = s.video.prepareQueueMedia(prepCtx, entry.VideoID, r.player == "inline")
			if prepareErr == nil && r.player == "iina" {
				prepareErr = s.player.Load(prepCtx, lease, r.token, func(event InlineQueueEvent) error { return s.reportPlayerEvent(event) })
			}
		}
		close(r.prepared)
		installed := false
		applyErr := s.command(context.Background(), func(ctx context.Context) error {
			if s.run != r {
				return ErrQueueChanged
			}
			if prepareErr != nil {
				return s.failLocked(ctx, "queue_prepare_failed", queueSafeError(prepareErr))
			}
			if r.player == "system" {
				if attempt == nil || !attempt.DispatchSucceeded {
					return s.failLocked(ctx, "queue_dispatch_failed", "系统播放器未能启动，请检查原片和播放器")
				}
				s.warning = attempt.StatsWarning
				return s.statusLocked(ctx, "dispatched", false)
			}
			r.lease = lease
			r.position = lease.start
			r.duration = lease.video.Duration
			installed = true
			if r.player == "iina" {
				r.loaded = true
				if err := s.player.Control(ctx, "resume"); err != nil {
					return s.failLocked(ctx, "queue_player_failed", queueSafeError(err))
				}
			}
			s.changed()
			return nil
		})
		s.runtimeFailure(r.token, applyErr)
		if lease != nil && !installed {
			lease.Close()
		}
	}()
	return nil
}

func (s *PlaybackQueueService) statusLocked(ctx context.Context, status string, bump bool) error {
	r := s.run
	if r == nil {
		return ErrQueueChanged
	}
	err := database.TransactionWithContext(ctx, func(db *gorm.DB) error {
		updates := map[string]any{"status": status}
		if bump {
			updates["revision"] = gorm.Expr("revision + 1")
		}
		result := db.Model(&models.PlaybackQueueState{}).Where("id = 1 AND active_token = ?", r.token).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrQueueChanged
		}
		return nil
	})
	if err == nil {
		s.changed()
	}
	return err
}
func (s *PlaybackQueueService) failLocked(ctx context.Context, code, message string) error {
	r := s.run
	if r == nil {
		return ErrQueueChanged
	}
	r.cancel()
	if r.lease != nil {
		r.lease.Close()
	}
	r.playing = false
	r.failed = true
	if r.player == "iina" && s.player != nil {
		if err := s.player.Close(); err != nil {
			s.warning = "播放器关闭失败：" + queueSafeError(err)
		}
	}
	state, err := readQueueState(ctx)
	if err != nil {
		return err
	}
	if state.ActiveToken != r.token {
		return ErrQueueChanged
	}
	message = queueSafeMessage(message)
	if len([]rune(message)) > 500 {
		message = string([]rune(message)[:500])
	}
	err = transitionQueue(ctx, state.Revision, map[string]any{"status": "failed", "last_error_code": code, "last_error_message": message})
	if err == nil {
		s.changed()
	}
	return err
}

func (s *PlaybackQueueService) Next(ctx context.Context, token string, revision uint64) error {
	return s.command(ctx, func(ctx context.Context) error {
		if s.run == nil || s.run.token != token {
			return ErrQueueChanged
		}
		state, err := readQueueState(ctx)
		if err != nil {
			return err
		}
		if token == "" || state.ActiveToken != token || state.Revision != revision {
			return ErrQueueChanged
		}
		return s.nextLocked(ctx, state)
	})
}
func (s *PlaybackQueueService) nextLocked(ctx context.Context, state models.PlaybackQueueState) error {
	if state.CurrentEntryID == nil {
		return ErrQueueChanged
	}
	var next models.PlaybackQueueEntry
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var current models.PlaybackQueueEntry
		if err := db.First(&current, *state.CurrentEntryID).Error; err != nil {
			return err
		}
		return db.Where("position > ? OR (position = ? AND id > ?)", current.Position, current.Position, current.ID).Order("position ASC").Order("id ASC").First(&next).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrQueueAtEnd
	}
	if err != nil {
		return err
	}
	return s.playLocked(ctx, next.ID, state.Revision)
}

func (s *PlaybackQueueService) Stop(ctx context.Context, token string) error {
	return s.command(ctx, func(ctx context.Context) error {
		if token != "" && s.stoppedToken == token && s.run == nil {
			return nil
		}
		if token == "" || s.run == nil || s.run.token != token {
			return ErrQueueChanged
		}
		s.stoppedToken = token
		s.terminalToken = token
		s.stopRuntimeLocked(false)
		s.changed()
		state, err := readQueueState(ctx)
		if err == nil {
			if state.ActiveToken != token {
				return ErrQueueChanged
			}
			err = transitionQueue(ctx, state.Revision, map[string]any{"active_token": "", "status": "stopped"})
		}
		if err != nil {
			s.warning = "播放已停止，但队列停止状态未保存：" + queueSafeError(err)
			s.changed()
		}
		return err
	})
}

func (s *PlaybackQueueService) stopRuntimeLocked(reusePlayer bool) {
	r := s.run
	s.run = nil
	if r == nil {
		return
	}
	r.cancel()
	if r.lease != nil {
		r.lease.Close()
	}
	<-r.prepared
	ctx, cancel := playbackFactContext(context.Background())
	defer cancel()
	if r.player == "iina" && s.player != nil {
		if reusePlayer {
			// mpv stop enters IINA's idle/window-close path. Replacement must
			// leave the window paused and loadfile replace its single item.
			if r.lease != nil && !r.failed {
				if err := s.player.Control(ctx, "pause"); err != nil {
					s.warning = "播放器暂停失败：" + queueSafeError(err)
				}
			}
		} else {
			if err := s.player.Stop(ctx); err != nil {
				s.warning = "播放器停止失败：" + queueSafeError(err)
			}
			if err := s.player.Close(); err != nil {
				s.warning = "播放器关闭失败：" + queueSafeError(err)
			}
		}
	}
	if r.started && !r.failed && r.lease != nil {
		if err := s.saveProgress(ctx, r, false); err != nil {
			s.warning = "最后观看进度未保存：" + queueSafeError(err)
		}
	}
}
func (s *PlaybackQueueService) Edit(ctx context.Context, in QueueEdit) error {
	return s.command(ctx, func(ctx context.Context) error {
		plan, err := preparePlaybackQueueEdit(ctx, in)
		if err != nil {
			return err
		}
		state := plan.state
		stop := in.Action == "clear" || in.Action == "replace_collection" || in.Action == "configure" && state.Player != in.Player || in.Action == "remove" && state.CurrentEntryID != nil && *state.CurrentEntryID == in.EntryID
		if stop && s.run != nil {
			s.stoppedToken = s.run.token
			s.terminalToken = s.run.token
			s.stopRuntimeLocked(false)
		}
		_, err = applyPreparedQueueEdit(ctx, plan)
		if err != nil && stop {
			s.warning = "播放已停止，但队列修改未保存：" + queueSafeError(err)
		}
		s.changed()
		return err
	})
}

func (s *PlaybackQueueService) Control(ctx context.Context, token, action string) error {
	return s.command(ctx, func(ctx context.Context) error {
		r := s.run
		if r == nil || r.failed || r.token != token {
			return ErrQueueChanged
		}
		if action != "pause" && action != "resume" {
			return ErrQueueInvalid
		}
		if r.player == "system" {
			return errors.New("queue_control_unsupported: 请在系统播放器中操作")
		}
		if r.lease == nil {
			return errors.New("queue_still_preparing: 播放器正在准备")
		}
		if r.player == "iina" {
			if err := s.player.Control(ctx, action); err != nil {
				return errors.Join(err, s.failLocked(ctx, "queue_control_failed", queueSafeError(err)))
			}
		}
		r.desiredPaused = action == "pause"
		r.controlSequence++
		s.changed()
		return nil
	})
}
func (s *PlaybackQueueService) Close() error {
	s.closeOnce.Do(func() {
		wait, _ := s.admission.quiesce()
		wait()
		s.commands.Lock()
		defer s.commands.Unlock()
		s.stopRuntimeLocked(false)
		ctx, cancel := playbackFactContext(context.Background())
		defer cancel()
		state, err := readQueueState(ctx)
		if err == nil && state.ActiveToken != "" {
			err = transitionQueue(ctx, state.Revision, map[string]any{"active_token": "", "status": "interrupted"})
		}
		if s.player != nil {
			err = errors.Join(err, s.player.Close())
		}
		s.closeErr = err
		s.changed()
	})
	return s.closeErr
}

// The HTTP route is token-only; callers never supply a file path. The second
// admission check rejects a Stop/Next that occurred during metadata validation.
func (s *PlaybackQueueService) Media(ctx context.Context, token string) (*io.SectionReader, string, time.Time, error) {
	ctx, done, err := s.admission.begin(ctx)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var lease *queueMediaLease
	err = s.command(ctx, func(context.Context) error {
		if s.run == nil || s.run.player != "inline" || s.run.token != token || s.run.lease == nil {
			return ErrQueueLeaseExpired
		}
		lease = s.run.lease
		return nil
	})
	if err != nil {
		return nil, "", time.Time{}, err
	}
	reader, mime, mtime, err := lease.reader(ctx)
	if err != nil {
		if errors.Is(err, ErrQueueSourceChanged) || errors.Is(err, ErrQueueMediaUnavailable) {
			_ = s.command(ctx, func(ctx context.Context) error {
				if s.run == nil || s.run.token != token {
					return ErrQueueLeaseExpired
				}
				return s.failLocked(ctx, "queue_source_changed", "原片已改变或不可用，请重新播放")
			})
		}
		return nil, "", time.Time{}, err
	}
	err = s.command(ctx, func(context.Context) error {
		if s.run == nil || s.run.token != token || s.run.lease != lease {
			return ErrQueueLeaseExpired
		}
		return nil
	})
	return reader, mime, mtime, err
}

// A failed state write cannot be repaired by pretending playback continued or
// by an automatic write retry. Expose the live failure and stop the owned player;
// the persisted unfinished token will become interrupted on restart.
func (s *PlaybackQueueService) runtimeFailure(token string, err error) {
	if err == nil || errors.Is(err, ErrQueueChanged) || errors.Is(err, ErrQueueInvalid) || errors.Is(err, ErrQueueEndIgnored) || errors.Is(err, ErrQueueStaleEvent) || errors.Is(err, ErrPlaybackQuiesced) || errors.Is(err, context.Canceled) {
		return
	}
	_ = s.command(context.Background(), func(context.Context) error {
		r := s.run
		if r == nil || r.token != token {
			return nil
		}
		r.failed = true
		r.playing = false
		r.cancel()
		if r.lease != nil {
			r.lease.Close()
		}
		if r.player == "iina" && s.player != nil {
			_ = s.player.Close()
		}
		s.warning = "播放已停止，队列状态未保存：" + queueSafeError(err)
		s.changed()
		return nil
	})
}

func queueSafeError(err error) string {
	if err == nil {
		return ""
	}
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return queueSafeMessage(pathError.Op + " <path>: " + pathError.Err.Error())
	}
	return queueSafeMessage(err.Error())
}
func queueSafeMessage(message string) string {
	message = scrubPlaybackProxyPaths(message)
	if len([]rune(message)) > 500 {
		return string([]rune(message)[:500])
	}
	return message
}
