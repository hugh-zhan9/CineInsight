package main

import (
	"context"
	"log"

	"video-master/services"
)

// 数据维护 · 数据体检（D-DH-04 / D-DH-05）的 App 层接线与绑定。

// dataHealthProgressEvent 推送体检与修复的进度（services.DataHealthProgress）。
const dataHealthProgressEvent = "data-health-progress"

func (a *App) initDataHealth(dataDir string) {
	a.dataCare.health = services.NewDataHealthService(dataDir)
	a.dataCare.health.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.dataCare.health.SetBackupService(a.backupService)
}

func (a *App) startDataHealth(ctx context.Context, emit func(event string, data any)) {
	a.dataCare.health.SetProgressEmitter(func(progress services.DataHealthProgress) {
		emit(dataHealthProgressEvent, progress)
	})
}

func (a *App) stopDataHealth() {
	if a.dataCare.health != nil {
		a.dataCare.health.StopAndWait()
	}
}

// taskCenterDataHealth 是任务中心适配（BackgroundTaskDataHealth）；运行与否以登记表为准，运行中可取消。
func (a *App) taskCenterDataHealth(src *taskCenterSources) taskCenterKeyState {
	running := src.running[string(services.BackgroundTaskDataHealth)]
	return taskCenterKeyState{running: running, canCancel: running}
}

func (a *App) dataHealthContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// RunDataHealthCheck 跑一次完整体检（只读，可取消），返回并保存报告。进度经 data-health-progress 推送。
func (a *App) RunDataHealthCheck() (*services.DataHealthReportView, error) {
	report, err := a.dataCare.health.Run(a.dataHealthContext())
	log.Printf("API RunDataHealthCheck err=%v", err)
	return report, services.WithoutAbsolutePaths(err)
}

// CancelDataHealthCheck 取消进行中的体检或修复。
func (a *App) CancelDataHealthCheck() {
	a.dataCare.health.Cancel()
}

// GetLatestDataHealthReport 返回运行状态与最近一份体检报告。
func (a *App) GetLatestDataHealthReport() (services.DataHealthState, error) {
	state, err := a.dataCare.health.Latest(a.dataHealthContext())
	return state, services.WithoutAbsolutePaths(err)
}

// RepairDataHealth 执行一项检查的修复（dangling_refs、derived_counts、managed_paths），修完重跑该项。
func (a *App) RepairDataHealth(check string) (services.DataHealthRepairResult, error) {
	result, err := a.dataCare.health.Repair(a.dataHealthContext(), check)
	log.Printf("API RepairDataHealth check=%s repaired=%d cancelled=%v err=%v", check, result.Repaired, result.Cancelled, err)
	return result, services.WithoutAbsolutePaths(err)
}
