package main

import (
	"context"

	"video-master/services"
)

// 数据维护四项（docs/loopx/design/2026-10-12-data-care）在 App 层的公共接线。
// 每一项的初始化、启动、停止与任务中心适配各在自己的文件里（app_lost_media.go、
// app_metadata_edit.go、app_automation_rules.go、app_data_health.go），这里只按固定顺序调用。

type dataCareServices struct {
	lost         *services.LostMediaService
	completeness *services.MetadataCompletenessService
	batchEdit    *services.MetadataBatchEditService
	rules        *services.AutomationRuleService
	health       *services.DataHealthService
}

// initDataCare 在 NewApp 末尾构造四项服务并接上登记表等对象间引用（不依赖启动顺序）。
func (a *App) initDataCare(dataDir string) {
	a.initLostMedia(dataDir)
	a.initMetadataEdit(dataDir)
	a.initAutomationRules(dataDir)
	a.initDataHealth(dataDir)
}

// startDataCare 在 startup 里接上依赖运行时上下文的部分（事件推送、常驻队列）。
func (a *App) startDataCare(ctx context.Context, emit func(event string, data any)) {
	a.startLostMedia(ctx, emit)
	a.startMetadataEdit(ctx, emit)
	a.startAutomationRules(ctx, emit)
	a.startDataHealth(ctx, emit)
}

// stopDataCare 停止并等待四项的后台工作：进入数据库恢复维护与退出时调用。
func (a *App) stopDataCare() {
	a.stopLostMedia()
	a.stopMetadataEdit()
	a.stopAutomationRules()
	a.stopDataHealth()
}

// resumeDataCare 在数据库恢复失败续跑时恢复常驻部分（例如新入库的规则队列）。
func (a *App) resumeDataCare() {
	if a.ctx == nil {
		return
	}
	a.resumeAutomationRules(a.ctx)
}

// dataCareQuitTasks 报告会被退出打断的写任务。数据体检只读，不阻止退出。
func (a *App) dataCareQuitTasks(running map[string]bool) []QuitBlockingTask {
	tasks := []QuitBlockingTask{}
	for _, key := range []services.BackgroundTaskKey{
		services.BackgroundTaskLostMedia,
		services.BackgroundTaskMetadataBatchEdit,
		services.BackgroundTaskAutomationRules,
	} {
		if running[string(key)] {
			tasks = append(tasks, QuitBlockingTask{Key: string(key), Running: 1, Names: []string{}})
		}
	}
	return tasks
}
