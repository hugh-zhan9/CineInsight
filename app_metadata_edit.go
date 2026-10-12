package main

import (
	"context"

	"video-master/services"
)

// 数据维护 · MetadataEdit 的 App 层接线（骨架，实现按详细设计补齐）。

func (a *App) initMetadataEdit(dataDir string) {
	a.dataCare.completeness = services.NewMetadataCompletenessService(dataDir)
	a.dataCare.batchEdit = services.NewMetadataBatchEditService(dataDir)
	a.dataCare.batchEdit.SetBackgroundTaskRegistry(a.backgroundTasks)
}

func (a *App) startMetadataEdit(ctx context.Context, emit func(event string, data any)) {}

func (a *App) stopMetadataEdit() {
	if a.dataCare.batchEdit != nil {
		a.dataCare.batchEdit.StopAndWait()
	}
}

// taskCenterMetadataEdit 是任务中心适配（BackgroundTaskMetadataBatchEdit）；运行与否以登记表为准。
func (a *App) taskCenterMetadataEdit(src *taskCenterSources) taskCenterKeyState {
	return taskCenterKeyState{}
}
