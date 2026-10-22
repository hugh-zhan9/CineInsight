package main

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"strings"
	"video-master/database"
	"video-master/services"
)

func (a *App) quiescePlaybackForShutdown() error    { return a.quiescePlayback(true) }
func (a *App) quiescePlaybackForMaintenance() error { return a.quiescePlayback(false) }
func (a *App) closeQueue(queue *services.PlaybackQueueService) error {
	if a.playbackQueueClose != nil {
		return a.playbackQueueClose(queue)
	}
	return queue.Close()
}
func (a *App) quiescePlayback(shutdown bool) error {
	a.playbackLifecycleMu.Lock()
	defer a.playbackLifecycleMu.Unlock()
	a.playbackShuttingDown = a.playbackShuttingDown || shutdown
	a.stopFunctionalPolish(shutdown)
	if a.playbackMaintenanceRelease == nil {
		a.playbackMaintenanceRelease = services.QuiescePlayback()
	}
	if a.videoService != nil && a.playbackControlledRelease == nil {
		a.playbackControlledRelease, a.playbackControlledCleanupError = a.videoService.QuiesceControlledPlayback()
	}
	a.playbackQueueMu.Lock()
	queue := a.playbackQueue
	if queue != nil {
		a.playbackQueueQuiesced = true
		if a.playbackQueueCleanupToken == "" {
			a.playbackQueueCleanupToken = strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	a.playbackQueueMu.Unlock()
	var queueErr error
	if queue != nil {
		queueErr = a.closeQueue(queue)
		if queueErr == nil {
			a.playbackQueueMu.Lock()
			a.playbackQueue = nil
			a.playbackQueueQuiesced = false
			a.playbackQueueCleanupToken = ""
			a.playbackQueueMu.Unlock()
		}
	}
	if err := errors.Join(a.playbackControlledCleanupError, queueErr); err != nil {
		return fmt.Errorf("播放器清理未完成，数据库维护未开始，请重试停止播放：%w", services.WithoutAbsolutePaths(err))
	}
	return nil
}
func (a *App) releasePlaybackMaintenance() {
	a.playbackLifecycleMu.Lock()
	defer a.playbackLifecycleMu.Unlock()
	if a.playbackShuttingDown || database.MaintenanceActive() || a.databaseUnavailableReason() != "" {
		return
	}
	a.playbackQueueMu.RLock()
	queuePending := a.playbackQueue != nil && a.playbackQueueQuiesced
	a.playbackQueueMu.RUnlock()
	if queuePending || a.playbackControlledCleanupError != nil {
		return
	}
	if release := a.playbackControlledRelease; release != nil {
		a.playbackControlledRelease = nil
		release()
	}
	if release := a.playbackMaintenanceRelease; release != nil {
		a.playbackMaintenanceRelease = nil
		release()
	}
}
