package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"video-master/database"
	"video-master/services"
)

func (a *App) startPlaybackQueue() error {
	a.playbackLifecycleMu.Lock()
	defer a.playbackLifecycleMu.Unlock()
	if a.playbackShuttingDown {
		return services.ErrPlaybackQuiesced
	}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	a.playbackQueueMu.RLock()
	existing := a.playbackQueue
	a.playbackQueueMu.RUnlock()
	if existing != nil {
		return nil
	}
	emit := a.playbackQueueEmit
	var published atomic.Bool
	queue := services.NewPlaybackQueueService(a.videoService, services.NewQueueIINAPlayer(), func(sequence uint64) {
		if published.Load() && emit != nil {
			emit(sequence)
		}
	})
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 30*time.Second)
	defer cancel()
	if err := queue.Initialize(ctx); err != nil {
		return err
	}
	initial, err := queue.Snapshot(ctx, services.QueueQuery{Limit: 1})
	if err != nil {
		return err
	}
	a.playbackQueueMu.Lock()
	a.playbackQueue = queue
	a.playbackQueueMu.Unlock()
	published.Store(true)
	if emit != nil {
		emit(initial.Sequence)
	}
	return nil
}
func (a *App) queueService() (*services.PlaybackQueueService, error) {
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, fmt.Errorf("queue_unavailable: %s", reason)
	}
	a.playbackQueueMu.RLock()
	queue := a.playbackQueue
	a.playbackQueueMu.RUnlock()
	if queue == nil {
		return nil, errors.New("queue_unavailable: 待播队列尚未就绪或正在维护")
	}
	return queue, nil
}
func (a *App) queueRPC(operation func(*services.PlaybackQueueService) error) error {
	queue, err := a.queueService()
	if err == nil {
		err = operation(queue)
	}
	return services.WithoutAbsolutePaths(err)
}
func (a *App) GetPlaybackQueue(query services.QueueQuery) (*services.QueueSnapshot, error) {
	var result *services.QueueSnapshot
	err := a.queueRPC(func(q *services.PlaybackQueueService) error {
		var err error
		result, err = q.Snapshot(a.backgroundContext(), query)
		return err
	})
	return result, err
}
func (a *App) EditPlaybackQueue(edit services.QueueEdit) error {
	return a.queueRPC(func(q *services.PlaybackQueueService) error { return q.Edit(a.backgroundContext(), edit) })
}
func (a *App) PlayPlaybackQueue(entryID uint, revision uint64) error {
	return a.queueRPC(func(q *services.PlaybackQueueService) error { return q.Play(a.backgroundContext(), entryID, revision) })
}
func (a *App) NextPlaybackQueue(token string, revision uint64) error {
	return a.queueRPC(func(q *services.PlaybackQueueService) error { return q.Next(a.backgroundContext(), token, revision) })
}
func (a *App) StopPlaybackQueue(token string) error {
	return a.queueRPC(func(q *services.PlaybackQueueService) error { return q.Stop(a.backgroundContext(), token) })
}
func (a *App) ControlPlaybackQueue(token, action string) error {
	return a.queueRPC(func(q *services.PlaybackQueueService) error { return q.Control(a.backgroundContext(), token, action) })
}
func (a *App) ReportInlineQueuePlayback(event services.InlineQueueEvent) error {
	return a.queueRPC(func(q *services.PlaybackQueueService) error { return q.ReportInline(a.backgroundContext(), event) })
}

func (a *App) serveQueueMedia(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/preview/queue/")
	if len(token) != 32 || strings.Trim(token, "0123456789abcdef") != "" {
		http.Error(w, "playback expired", http.StatusGone)
		return
	}
	a.playbackQueueMu.RLock()
	queue := a.playbackQueue
	a.playbackQueueMu.RUnlock()
	if queue == nil {
		http.Error(w, "playback expired", http.StatusGone)
		return
	}
	reader, mime, mtime, err := queue.Media(r.Context(), token)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, services.ErrQueueLeaseExpired), errors.Is(err, services.ErrPlaybackQuiesced):
			status = http.StatusGone
		case errors.Is(err, services.ErrQueueSourceChanged), errors.Is(err, services.ErrQueueMediaUnavailable):
			status = http.StatusConflict
		case errors.Is(err, database.ErrMaintenance):
			status = http.StatusServiceUnavailable
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", mime)
	http.ServeContent(w, r, "queue-media", mtime, reader)
}
