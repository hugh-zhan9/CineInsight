package main

import (
	"context"
	"log"

	"video-master/services"
)

// 数据维护 · 失联数据治理（D-LD-*）的 App 层接线：报告、找回、清除与孤儿文件回收。

func (a *App) initLostMedia(dataDir string) {
	a.dataCare.lost = services.NewLostMediaService(dataDir)
	a.dataCare.lost.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.dataCare.lost.SetDependencies(a.videoService, a.imageService, a.playbackProxies)
}

// startLostMedia 接上播放队列变更通知：清除删掉了待播队列里的条目时，队列面板按同一个事件重读。
func (a *App) startLostMedia(ctx context.Context, emit func(event string, data any)) {
	if a.dataCare.lost != nil {
		a.dataCare.lost.SetPlaybackQueueEmitter(func(sequence uint64) { emit("playback-queue-state", sequence) })
	}
}

func (a *App) stopLostMedia() {
	if a.dataCare.lost != nil {
		a.dataCare.lost.StopAndWait()
	}
}

// taskCenterLostMedia 是任务中心适配（BackgroundTaskLostMedia）；运行与否以登记表为准。
// 取消在失联页里给（CancelLostMediaTask），任务中心这一层只显示进度。
func (a *App) taskCenterLostMedia(src *taskCenterSources) taskCenterKeyState {
	if a.dataCare.lost == nil {
		return taskCenterKeyState{}
	}
	status := a.dataCare.lost.Status()
	return taskCenterKeyState{running: status.Running, canCancel: status.Running, progress: runningProgress(status.Running, status.Processed, status.Total)}
}

// GetLostMediaReport 返回失联报告（kind 为 video / image）。
func (a *App) GetLostMediaReport(kind string) (*services.LostMediaReport, error) {
	report, err := a.dataCare.lost.GetLostMediaReport(a.backgroundContext(), kind)
	if err != nil {
		log.Printf("API GetLostMediaReport kind=%s err=%v", kind, err)
	}
	return report, err
}

// ListLostMediaItems 返回一个目录分组里的失联条目（按 id 键集分页）。
func (a *App) ListLostMediaItems(query services.LostMediaItemQuery) (*services.LostMediaItemPage, error) {
	return a.dataCare.lost.ListLostMediaItems(a.backgroundContext(), query)
}

// PreviewLostRelinkMove 是「目录搬家」的配对预览（只读）。
func (a *App) PreviewLostRelinkMove(request services.LostRelinkMoveRequest) (*services.LostRelinkPreview, error) {
	preview, err := a.dataCare.lost.PreviewLostRelinkMove(a.backgroundContext(), request)
	if err != nil {
		log.Printf("API PreviewLostRelinkMove kind=%s err=%v", request.Kind, err)
	}
	return preview, err
}

// PreviewLostRelinkSearch 是「在文件夹里找」的配对预览（只读）。
func (a *App) PreviewLostRelinkSearch(request services.LostRelinkSearchRequest) (*services.LostRelinkPreview, error) {
	preview, err := a.dataCare.lost.PreviewLostRelinkSearch(a.backgroundContext(), request)
	if err != nil {
		log.Printf("API PreviewLostRelinkSearch kind=%s err=%v", request.Kind, err)
	}
	return preview, err
}

// ApplyLostRelink 执行找回；有条目改了路径时让清理分析结果失效。
func (a *App) ApplyLostRelink(request services.LostRelinkApplyRequest) (*services.LostMediaBatchResult, error) {
	result, err := a.dataCare.lost.ApplyLostRelink(a.backgroundContext(), request)
	a.afterLostMediaChange(request.Kind, result)
	logLostMediaResult("ApplyLostRelink", request.Kind, result, err)
	return result, err
}

// PreviewLostPurge 统计清除的连带影响（只读）。
func (a *App) PreviewLostPurge(request services.LostPurgeRequest) (*services.LostPurgePreview, error) {
	preview, err := a.dataCare.lost.PreviewLostPurge(a.backgroundContext(), request)
	if err != nil {
		log.Printf("API PreviewLostPurge kind=%s err=%v", request.Kind, err)
	}
	return preview, err
}

// ApplyLostPurge 彻底清除（Confirm 必须是「清除记录」）；有条目被清除时让清理分析结果失效。
func (a *App) ApplyLostPurge(request services.LostPurgeRequest) (*services.LostMediaBatchResult, error) {
	result, err := a.dataCare.lost.ApplyLostPurge(a.backgroundContext(), request)
	a.afterLostMediaChange(request.Kind, result)
	logLostMediaResult("ApplyLostPurge", request.Kind, result, err)
	return result, err
}

// GetOrphanFileReport 按位置统计数据目录里的孤儿文件（只读）。
func (a *App) GetOrphanFileReport() (*services.OrphanFileReport, error) {
	return a.dataCare.lost.GetOrphanFileReport(a.backgroundContext())
}

// ReclaimOrphanFiles 回收所选位置的孤儿文件。
func (a *App) ReclaimOrphanFiles(locations []string) (*services.OrphanReclaimResult, error) {
	result, err := a.dataCare.lost.ReclaimOrphanFiles(a.backgroundContext(), locations)
	log.Printf("API ReclaimOrphanFiles locations=%v result=%+v err=%v", locations, result, err)
	return result, err
}

// CancelLostMediaTask 取消进行中的找回 / 清除 / 回收：已处理的保留。
func (a *App) CancelLostMediaTask() {
	a.dataCare.lost.Cancel()
}

func (a *App) afterLostMediaChange(kind string, result *services.LostMediaBatchResult) {
	if result == nil || result.Done == 0 {
		return
	}
	if kind == services.LostMediaKindVideo && a.cleanupService != nil {
		a.cleanupService.InvalidateAnalysis()
	}
	if kind == services.LostMediaKindImage && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
}

func logLostMediaResult(method, kind string, result *services.LostMediaBatchResult, err error) {
	if result == nil {
		log.Printf("API %s kind=%s err=%v", method, kind, err)
		return
	}
	log.Printf("API %s kind=%s done=%d skipped=%d failed=%d cancelled=%t", method, kind, result.Done, result.Skipped, result.Failed, result.Cancelled)
}
