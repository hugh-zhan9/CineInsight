package main

import (
	"fmt"
	"video-master/services"
)

// StopControlledPlayback stops only the dedicated IINA process owned by this application.
func (a *App) StopControlledPlayback() error {
	if a.videoService == nil {
		return nil
	}
	a.playbackLifecycleMu.Lock()
	wasMaintenance := a.playbackMaintenanceRelease != nil || a.playbackControlledRelease != nil
	err := a.videoService.StopControlledPlayback()
	if err == nil {
		a.playbackControlledCleanupError = nil
	}
	a.playbackLifecycleMu.Unlock()
	if err != nil {
		return services.WithoutAbsolutePaths(err)
	}
	a.releasePlaybackMaintenance()
	if wasMaintenance && a.databaseUnavailableReason() == "" {
		return a.startPlaybackQueue()
	}
	return nil
}
func (a *App) GetControlledPlaybackState() (services.ControlledPlaybackState, error) {
	_, done, err := a.beginFunctionalPolish()
	if err != nil {
		return services.ControlledPlaybackState{}, err
	}
	defer done()

	if reason := a.databaseUnavailableReason(); reason != "" {
		return services.ControlledPlaybackState{}, fmt.Errorf("%s", reason)
	}
	if a.videoService == nil {
		return services.ControlledPlaybackState{}, fmt.Errorf("视频服务未初始化")
	}
	return a.videoService.GetControlledPlaybackState(), nil
}
