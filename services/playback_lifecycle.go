package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"sync"
	"time"
	"video-master/database"
)

var ErrPlaybackQuiesced = errors.New("playback_quiesced: 播放正在停止或数据库维护中")

// Owns only in-flight formal dispatches, not the lifetime of a system player
// already launched. Admission is closed before either class of worker is waited.
type formalPlaybackAdmission struct {
	mu         sync.Mutex
	paused     int
	active     int
	empty      chan struct{}
	generation context.Context
	cancel     context.CancelFunc
}

var formalPlaybacks formalPlaybackAdmission

func (g *formalPlaybackAdmission) begin(parent context.Context) (context.Context, func(), error) {
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	g.mu.Lock()
	if g.paused > 0 {
		g.mu.Unlock()
		return nil, nil, ErrPlaybackQuiesced
	}
	if g.generation == nil {
		g.generation, g.cancel = context.WithCancel(context.Background())
	}
	generation := g.generation
	if g.active == 0 {
		g.empty = make(chan struct{})
	}
	g.active++
	g.mu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(generation, cancel)
	if generation.Err() != nil {
		cancel()
	}
	var once sync.Once
	done := func() {
		once.Do(func() {
			stop()
			cancel()
			g.mu.Lock()
			g.active--
			if g.active == 0 {
				close(g.empty)
			}
			g.mu.Unlock()
		})
	}
	return ctx, done, nil
}

func (g *formalPlaybackAdmission) quiesce() (wait func(), release func()) {
	g.mu.Lock()
	g.paused++
	if g.cancel != nil {
		g.cancel()
	}
	empty := g.empty
	g.mu.Unlock()
	var once sync.Once
	return func() {
			if empty != nil {
				<-empty
			}
		}, func() {
			once.Do(func() {
				g.mu.Lock()
				defer g.mu.Unlock()
				g.paused--
				if g.paused == 0 {
					g.generation = nil
					g.cancel = nil
				}
			})
		}
}

// QuiescePlayback stays closed after returning. Maintenance owns the release
// until it has restored database/path access; shutdown deliberately never calls
// it. The old StopPlaybackRelocation remains a temporary stop for its callers.
func QuiescePlayback() func() {
	waitFormal, releaseFormal := formalPlaybacks.quiesce()
	waitRelocation, releaseRelocation := quiescePlaybackRelocation()
	waitFormal()
	waitRelocation()
	var once sync.Once
	return func() { once.Do(func() { releaseRelocation(); releaseFormal() }) }
}

func withFormalPlayback(parent context.Context, operation func(context.Context, *gorm.DB) (*PlaybackAttemptResult, error)) (*PlaybackAttemptResult, error) {
	ctx, done, err := formalPlaybacks.begin(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	var result *PlaybackAttemptResult
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var err error
		result, err = operation(ctx, db)
		return err
	})
	return result, err
}

// An already dispatched playback is a fact. Cancellation stops preparation, not
// the bounded final attempt to account for that dispatch before maintenance.
func playbackFactContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
}
