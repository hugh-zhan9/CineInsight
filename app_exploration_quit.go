package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

type explorationQuitState struct {
	mu         sync.Mutex
	clientSeen bool
	pending    string
	deadline   time.Time
	timer      *time.Timer
	passOnce   bool
}

// BeforeClose never blocks the UI thread on I/O. The frontend flushes through
// normal guarded bindings before shutdown closes their admission gate.
func (a *App) beforeExplorationClose(ctx context.Context) bool {
	if ctx == nil {
		ctx = a.ctx
	}
	if ctx == nil {
		return false
	}
	q := &a.explorationQuit
	q.mu.Lock()
	if q.passOnce {
		q.passOnce = false
		q.mu.Unlock()
		return false
	}
	if !q.clientSeen {
		q.mu.Unlock()
		return false
	}
	if q.pending != "" {
		q.mu.Unlock()
		return true
	}
	id := uuid.NewString()
	q.pending = id
	q.deadline = time.Now().Add(5 * time.Second)
	q.timer = time.AfterFunc(5*time.Second, func() {
		q.mu.Lock()
		current := q.pending == id
		q.mu.Unlock()
		if current {
			emitRuntimeEvent(ctx, "exploration-close-timeout", map[string]string{"request_id": id})
		}
	})
	q.mu.Unlock()
	emitRuntimeEvent(ctx, "exploration-close-flush", map[string]string{"request_id": id})
	return true
}

// FinishMediaExplorationClose handles only the matching close attempt. Passing
// the flush resumes, rather than bypasses, the existing background-task guard.
func (a *App) FinishMediaExplorationClose(requestID, action string) error {
	q := &a.explorationQuit
	q.mu.Lock()
	if requestID == "" || q.pending != requestID {
		q.mu.Unlock()
		return errors.New("退出请求已变化，请重新关闭窗口")
	}
	if action != "saved" && action != "discard" && action != "cancel" && action != "retry" {
		q.mu.Unlock()
		return errors.New("不支持的退出操作")
	}
	if action == "saved" && time.Now().After(q.deadline) {
		q.mu.Unlock()
		return errors.New("保存等待已超时，请重试或继续运行")
	}
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	q.pending = ""
	q.passOnce = action == "saved" || action == "discard"
	quit := q.passOnce
	q.mu.Unlock()
	if action == "retry" {
		a.beforeExplorationClose(a.ctx)
		return nil
	}
	if quit {
		if a.ctx == nil {
			return errors.New("应用尚未就绪，无法退出")
		}
		go func(ctx context.Context) { time.Sleep(internalQuitDelay); quitRuntime(ctx) }(a.ctx)
	}
	return nil
}

func (a *App) stopExplorationQuit() {
	q := &a.explorationQuit
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	q.pending = ""
}

func (a *App) notifyExplorationLifecycle(event string) {
	a.explorationQuit.mu.Lock()
	seen := a.explorationQuit.clientSeen
	a.explorationQuit.mu.Unlock()
	if seen && a.ctx != nil {
		emitRuntimeEvent(a.ctx, event, nil)
	}
}
