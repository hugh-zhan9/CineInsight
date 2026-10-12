package main

import (
	"context"
	"log"

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

// PreviewMetadataBatchEdit 是批量编辑的预览（D-MC-04）：目标条目数与每个操作实际会改变的条目数。
func (a *App) PreviewMetadataBatchEdit(req services.MetadataBatchEditRequest) (*services.MetadataBatchEditPreview, error) {
	preview, err := a.dataCare.batchEdit.PreviewMetadataBatchEdit(req)
	if err != nil {
		log.Printf("API PreviewMetadataBatchEdit kind=%s err=%v", req.Kind, services.WithoutAbsolutePaths(err))
	}
	return preview, err
}

// ApplyMetadataBatchEdit 执行批量编辑（D-MC-04）。在 metadata_batch_edit 任务键下运行，同一时刻只允许一个；
// 完成后前端按既有的 videos-changed 路径重取列表。
func (a *App) ApplyMetadataBatchEdit(req services.MetadataBatchEditRequest) (*services.MetadataBatchEditResult, error) {
	result, err := a.dataCare.batchEdit.ApplyMetadataBatchEdit(a.backgroundContext(), req)
	if err != nil {
		log.Printf("API ApplyMetadataBatchEdit kind=%s err=%v", req.Kind, services.WithoutAbsolutePaths(err))
	}
	return result, err
}

// ListMetadataBatchEdits 是「最近的批量编辑」（D-MC-05）。
func (a *App) ListMetadataBatchEdits() ([]services.MetadataBatchEditListItem, error) {
	return a.dataCare.batchEdit.ListMetadataBatchEdits()
}

// UndoMetadataBatchEdit 撤销一次批量编辑（D-MC-05）；与执行共用任务键和运行位。
func (a *App) UndoMetadataBatchEdit(batchID uint) (*services.MetadataBatchUndoResult, error) {
	result, err := a.dataCare.batchEdit.UndoMetadataBatchEdit(a.backgroundContext(), batchID)
	if err != nil {
		log.Printf("API UndoMetadataBatchEdit batch=%d err=%v", batchID, services.WithoutAbsolutePaths(err))
	}
	return result, err
}

// taskCenterMetadataEdit 是任务中心适配（BackgroundTaskMetadataBatchEdit）；运行与否以登记表为准。
// 没有零参的启动与取消：批量编辑总要选范围与操作，取消只在进入恢复维护与退出时发生。
func (a *App) taskCenterMetadataEdit(src *taskCenterSources) taskCenterKeyState {
	if a.dataCare.batchEdit == nil {
		return taskCenterKeyState{}
	}
	status := a.dataCare.batchEdit.Status()
	state := taskCenterKeyState{running: status.Running, progress: runningProgress(status.Running, status.Done, status.Total)}
	if !status.Running && status.FinishedAt != nil {
		state.lastRun = singleOutcomeRun(status.FinishedAt, status.LastError)
	}
	return state
}
