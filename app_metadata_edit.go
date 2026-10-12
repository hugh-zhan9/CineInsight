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

// GetMetadataCompleteness 是数据维护「完整度」页签的统计（D-MC-02）。
func (a *App) GetMetadataCompleteness() (*services.MetadataCompleteness, error) {
	return a.dataCare.completeness.GetMetadataCompleteness()
}

// GetMetadataCompletenessByDirectory 是某一项「缺得最多的目录」（kind 为 video / image，limit ≤0 时取 10）。
func (a *App) GetMetadataCompletenessByDirectory(kind, field string, limit int) ([]services.MetadataCompletenessDirectory, error) {
	return a.dataCare.completeness.GetMetadataCompletenessByDirectory(kind, field, limit)
}

// taskCenterMetadataEdit 是任务中心适配（BackgroundTaskMetadataBatchEdit）；运行与否以登记表为准。
func (a *App) taskCenterMetadataEdit(src *taskCenterSources) taskCenterKeyState {
	return taskCenterKeyState{}
}
