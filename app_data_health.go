package main

import (
	"context"

	"video-master/services"
)

// 数据维护 · DataHealth 的 App 层接线（骨架，实现按详细设计补齐）。

func (a *App) initDataHealth(dataDir string) {
	a.dataCare.health = services.NewDataHealthService(dataDir)
	a.dataCare.health.SetBackgroundTaskRegistry(a.backgroundTasks)
}

func (a *App) startDataHealth(ctx context.Context, emit func(event string, data any)) {}

func (a *App) stopDataHealth() {
	if a.dataCare.health != nil {
		a.dataCare.health.StopAndWait()
	}
}

// taskCenterDataHealth 是任务中心适配（BackgroundTaskDataHealth）；运行与否以登记表为准。
func (a *App) taskCenterDataHealth(src *taskCenterSources) taskCenterKeyState {
	return taskCenterKeyState{}
}
