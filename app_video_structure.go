package main

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func (a *App) startVideoStructure(ctx context.Context) {
	if a.videoStructure == nil || database.DB == nil || a.databaseUnavailableReason() != "" {
		return
	}
	a.videoStructure.SetActivityCheck(func(ctx context.Context) (bool, string) {
		var settings models.Settings
		if err := database.WithOperationContext(ctx, func(db *gorm.DB) error { return db.First(&settings).Error }); err != nil {
			return false, "probe_failed"
		}
		if !settings.AutoStructureEnabled {
			return false, "disabled"
		}
		if a.videoStructure.ForegroundBusy() {
			return false, "active_playback"
		}
		if a.idleGate == nil {
			return false, "probe_unavailable"
		}
		return a.idleGate.FreshStructureDecision(ctx, services.IdleSchedulerSettings{Enabled: true, ThresholdMinutes: settings.IdleThresholdMinutes, RequireACPower: settings.IdleRequireACPower, WindowStart: settings.IdleWindowStart, WindowEnd: settings.IdleWindowEnd})
	})
	_ = a.videoStructure.Start(ctx)
}
func (a *App) stopVideoStructure(ctx context.Context) error {
	if a.videoStructure == nil {
		return nil
	}
	return a.videoStructure.StopAndWait(ctx)
}
func (a *App) GetVideoStructureStatus() services.VideoStructureStatus {
	if a.videoStructure == nil {
		return services.VideoStructureStatus{Error: "自动分段尚未初始化"}
	}
	return a.videoStructure.Status()
}
func (a *App) GetVideoStructure(videoID uint) (*services.VideoStructureView, error) {
	if a.videoStructure == nil {
		return nil, errors.New("自动分段尚未初始化")
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 5*time.Second)
	defer cancel()
	return a.videoStructure.Get(ctx, videoID)
}
func (a *App) RetryVideoStructure(videoID uint, revision uint64) error {
	if a.videoStructure == nil {
		return errors.New("自动分段尚未初始化")
	}
	err := a.videoStructure.Retry(a.backgroundContext(), videoID, revision)
	if err == nil {
		a.videoStructure.Wake()
	}
	return err
}
func (a *App) ResolveVideoStructure(videoID uint, generation uint64, position int, sourceVersion string) (*services.ChapterResolution, error) {
	view, err := a.GetVideoStructure(videoID)
	if err != nil {
		return nil, err
	}
	if view.SourceStatus != "ready" || view.Generation != generation || view.SourceVersion != sourceVersion {
		return nil, services.ErrStructureObsolete
	}
	if position < 0 || position >= len(view.Segments) {
		return nil, errors.New("structure_invalid_position")
	}
	return &services.ChapterResolution{VideoID: videoID, SourceVersion: sourceVersion, StartMS: view.Segments[position].StartMS}, nil
}
func (a *App) NotifyPlaybackActivity(owner string, playing bool) {
	if a.videoStructure != nil {
		a.videoStructure.SetPlaybackActivity(owner, playing)
	}
}
func (a *App) taskCenterVideoStructure(src *taskCenterSources) taskCenterKeyState {
	if a.videoStructure == nil {
		return taskCenterKeyState{}
	}
	status := a.videoStructure.Status()
	if status.WaitingIdle {
		src.waiting[string(services.BackgroundTaskVideoStructure)] = status.Reason
	}
	if status.Error != "" {
		src.warn(status.Error)
	}
	return taskCenterKeyState{strictIdle: true, running: status.Running, progress: runningProgress(status.Running, int(status.Ready), int(status.Pending+status.Ready+status.Failed+status.Unavailable)), lastRun: &TaskLastRun{Succeeded: int(status.Ready), Failed: int(status.Failed), Remaining: int(status.Pending), Skipped: int(status.Unavailable), Failures: []string{}}}
}
func (a *App) wireVideoStructure() {
	a.videoStructure.SetBackgroundTaskRegistry(a.backgroundTasks)

	a.mediaProbeService.SetSourceObserver(a.videoStructure.SourceObserved)
}
