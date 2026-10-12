package main

import (
	"context"

	"video-master/services"
)

// 数据维护 · AutomationRules 的 App 层接线（骨架，实现按详细设计补齐）。

func (a *App) initAutomationRules(dataDir string) {
	a.dataCare.rules = services.NewAutomationRuleService(dataDir)
	a.dataCare.rules.SetBackgroundTaskRegistry(a.backgroundTasks)
}

func (a *App) startAutomationRules(ctx context.Context, emit func(event string, data any)) {}

func (a *App) stopAutomationRules() {
	if a.dataCare.rules != nil {
		a.dataCare.rules.StopAndWait()
	}
}

func (a *App) resumeAutomationRules(ctx context.Context) {}

// taskCenterAutomationRules 是任务中心适配（BackgroundTaskAutomationRules）；运行与否以登记表为准。
func (a *App) taskCenterAutomationRules(src *taskCenterSources) taskCenterKeyState {
	return taskCenterKeyState{}
}
