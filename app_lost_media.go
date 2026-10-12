package main

import (
	"context"

	"video-master/services"
)

// 数据维护 · LostMedia 的 App 层接线（骨架，实现按详细设计补齐）。

func (a *App) initLostMedia(dataDir string) {
	a.dataCare.lost = services.NewLostMediaService(dataDir)
	a.dataCare.lost.SetBackgroundTaskRegistry(a.backgroundTasks)
}

func (a *App) startLostMedia(ctx context.Context, emit func(event string, data any)) {}

func (a *App) stopLostMedia() {
	if a.dataCare.lost != nil {
		a.dataCare.lost.StopAndWait()
	}
}

// taskCenterLostMedia 是任务中心适配（BackgroundTaskLostMedia）；运行与否以登记表为准。
func (a *App) taskCenterLostMedia(src *taskCenterSources) taskCenterKeyState {
	return taskCenterKeyState{}
}
