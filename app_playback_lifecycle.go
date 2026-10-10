package main

import (
	"log"
	"video-master/services"
)

func (a *App) quiescePlaybackForShutdown() {
	a.quiescePlayback(true)
}

func (a *App) quiescePlaybackForMaintenance() {
	a.quiescePlayback(false)
}

func (a *App) quiescePlayback(shuttingDown bool) {
	a.playbackLifecycleMu.Lock()
	defer a.playbackLifecycleMu.Unlock()
	a.playbackShuttingDown = a.playbackShuttingDown || shuttingDown
	// Queue callbacks and preparations must leave before formal playback and
	// database/path fences. The getter never holds this lifecycle lock.
	a.playbackQueueMu.Lock()
	queue := a.playbackQueue
	a.playbackQueue = nil
	a.playbackQueueMu.Unlock()
	if queue != nil {
		if err := queue.Close(); err != nil {
			log.Printf("Playback queue shutdown failed: %v", services.WithoutAbsolutePaths(err))
		}
	}
	if a.playbackMaintenanceRelease == nil {
		a.playbackMaintenanceRelease = services.QuiescePlayback()
	}
}

func (a *App) releasePlaybackMaintenance() {
	a.playbackLifecycleMu.Lock()
	defer a.playbackLifecycleMu.Unlock()
	if a.playbackShuttingDown {
		return
	}
	release := a.playbackMaintenanceRelease
	a.playbackMaintenanceRelease = nil
	if release != nil {
		release()
	}
}
