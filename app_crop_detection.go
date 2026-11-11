package main

import (
	"context"
	"errors"
	"sync"
	"time"
	"video-master/services"
)

// cropDetectionLoop owns only automatic wakeups; detector state stays in its service.
type cropDetectionLoop struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
	wake   chan struct{}
}

func (a *App) configureCropDetection(enabled bool) { a.configureCropDetectionMode(enabled, false) }
func (a *App) stopCropDetection()                  { a.configureCropDetectionMode(false, true) }
func (a *App) configureCropDetectionMode(enabled, stopping bool) {
	if a.cropDetection == nil {
		return
	}
	a.cropAuto.mu.Lock()
	defer a.cropAuto.mu.Unlock()
	if a.cropAuto.cancel != nil {
		a.cropAuto.cancel()
		a.cropAuto.cancel = nil
	}
	a.cropAuto.wg.Wait()
	a.cropDetection.StopAndWait()
	if stopping {
		return
	}
	a.cropDetection.Resume()
	if !enabled {
		return
	}
	ctx, cancel := context.WithCancel(a.backgroundContext())
	a.cropAuto.cancel = cancel
	if a.cropAuto.wake == nil {
		a.cropAuto.wake = make(chan struct{}, 1)
	}
	wake := a.cropAuto.wake
	a.cropAuto.wg.Add(1)
	go func() {
		defer a.cropAuto.wg.Done()
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		run := func() {
			if a.idleGate == nil {
				return
			}
			_ = a.idleGate.Run(ctx, string(services.BackgroundTaskCropDetection), func(runCtx context.Context) error {
				_, err := a.cropDetection.StartWithPauseHook(runCtx, a.idleGate.PauseHook(string(services.BackgroundTaskCropDetection)))
				return err
			})
		}
		run()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			case <-wake:
				run()
			}
		}
	}()
}
func (a *App) wakeCropDetection() {
	if !a.cropAuto.mu.TryLock() {
		return
	}
	defer a.cropAuto.mu.Unlock()
	if a.cropAuto.cancel != nil {
		select {
		case a.cropAuto.wake <- struct{}{}:
		default:
		}
	}
}
func (a *App) StartCropDetection() (services.CropDetectionStatus, error) {
	if a.cropDetection == nil {
		return services.CropDetectionStatus{}, errors.New("crop_detection_unavailable: 黑边识别尚未初始化")
	}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return services.CropDetectionStatus{}, errors.New("crop_detection_unavailable: " + reason)
	}
	return a.cropDetection.Start(a.backgroundContext())
}
func (a *App) GetCropDetectionStatus() services.CropDetectionStatus {
	if a.cropDetection == nil {
		return services.CropDetectionStatus{}
	}
	return a.cropDetection.Status()
}
func (a *App) CancelCropDetection() error {
	if a.cropDetection == nil {
		return nil
	}
	return a.cropDetection.Cancel()
}
func (a *App) taskCenterCropDetection(src *taskCenterSources) taskCenterKeyState {
	if a.cropDetection == nil {
		return taskCenterKeyState{}
	}
	status := a.cropDetection.Status()
	run := frameHashRun(status)
	if status.Gate.WaitingIdle {
		src.waiting[string(services.BackgroundTaskCropDetection)] = status.Gate.Reason
	}
	return taskCenterKeyState{running: run.running, preparing: run.preparing, canStart: true, canCancel: run.running, progress: run.progress(), lastRun: run.lastRun()}
}
