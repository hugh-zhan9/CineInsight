package services

import (
	"context"
	"sync"
)

// contextMutex preserves each owning service's existing serialization and lock
// order, but allows cancellable callers to stop waiting without a parked goroutine.
type contextMutex struct {
	once  sync.Once
	token chan struct{}
}

func (g *contextMutex) init() {
	g.once.Do(func() { g.token = make(chan struct{}, 1); g.token <- struct{}{} })
}
func (g *contextMutex) LockContext(ctx context.Context) error {
	g.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
		if err := ctx.Err(); err != nil {
			g.Unlock()
			return err
		}
		return nil
	}
}
func (g *contextMutex) Lock() { _ = g.LockContext(context.Background()) }
func (g *contextMutex) Unlock() {
	g.init()
	select {
	case g.token <- struct{}{}:
	default:
		panic("unlock of unlocked context mutex")
	}
}
