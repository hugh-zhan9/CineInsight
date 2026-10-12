package main

import (
	"context"
	"log"

	"video-master/services"
)

// 数据维护 · 本地规则（D-RA-01..05，需求设计文档 §3）的 App 层接线。

// automationRulesRunEvent 推送手动运行的进度（规则页订阅）。
const automationRulesRunEvent = "automation-rules-run"

func (a *App) initAutomationRules(dataDir string) {
	rules := services.NewAutomationRuleService(dataDir)
	a.dataCare.rules = rules
	rules.SetBackgroundTaskRegistry(a.backgroundTasks)
	// 入队方（D-RA-05）：新建的视频、新建的图片、补探测成功的视频。观察者只记 id，评估在规则队列里做。
	if a.videoService != nil {
		a.videoService.SetNewVideoObserver(rules.EnqueueVideo)
	}
	if a.imageService != nil {
		a.imageService.SetNewImageObserver(rules.EnqueueImage)
	}
	if a.technicalBackfill != nil {
		a.technicalBackfill.SetProbedObserver(rules.EnqueueVideo)
	}
}

func (a *App) startAutomationRules(ctx context.Context, emit func(event string, data any)) {
	if a.dataCare.rules == nil {
		return
	}
	a.dataCare.rules.Start(ctx)
	a.dataCare.rules.SetEventEmitter(func(status services.AutomationRuleRunStatus) { emit(automationRulesRunEvent, status) })
}

func (a *App) stopAutomationRules() {
	if a.dataCare.rules != nil {
		a.dataCare.rules.StopAndWait()
	}
}

func (a *App) resumeAutomationRules(ctx context.Context) {
	if a.dataCare.rules != nil {
		a.dataCare.rules.Resume()
	}
}

// taskCenterAutomationRules 是任务中心适配（BackgroundTaskAutomationRules）；运行与否以登记表为准。
// 只反映手动运行（新入库队列是轻量写，不进任务中心）；取消用零参的 CancelAutomationRuleRun。
func (a *App) taskCenterAutomationRules(src *taskCenterSources) taskCenterKeyState {
	if a.dataCare.rules == nil {
		return taskCenterKeyState{}
	}
	status := a.dataCare.rules.GetAutomationRuleRunStatus()
	state := taskCenterKeyState{
		running:   status.Running,
		canCancel: status.Running,
		progress:  runningProgress(status.Running, status.Processed, status.Total),
	}
	if status.Running || !status.Completed {
		return state
	}
	failures := []string{}
	if status.Error != "" {
		failures = append(failures, status.Error)
	}
	state.lastRun = &TaskLastRun{FinishedAt: status.FinishedAt, Succeeded: status.Matched, Failed: len(failures), Failures: boundTaskFailures(failures)}
	return state
}

// ===== App 接口（需求设计文档 §3.5）=====

func (a *App) ListAutomationRules() ([]services.AutomationRuleView, error) {
	return a.dataCare.rules.ListAutomationRules()
}

func (a *App) SaveAutomationRule(rule services.AutomationRuleInput) (services.AutomationRuleView, error) {
	view, err := a.dataCare.rules.SaveAutomationRule(rule)
	log.Printf("API SaveAutomationRule id=%d kind=%s conditions=%d actions=%d err=%v", rule.ID, rule.MediaKind, len(rule.Conditions), len(rule.Actions), err)
	return view, err
}

func (a *App) DeleteAutomationRule(id uint) error {
	err := a.dataCare.rules.DeleteAutomationRule(id)
	log.Printf("API DeleteAutomationRule id=%d err=%v", id, err)
	return err
}

func (a *App) ReorderAutomationRules(ids []uint) error {
	return a.dataCare.rules.ReorderAutomationRules(ids)
}

func (a *App) SetAutomationRuleEnabled(id uint, enabled bool) (services.AutomationRuleView, error) {
	view, err := a.dataCare.rules.SetAutomationRuleEnabled(id, enabled)
	log.Printf("API SetAutomationRuleEnabled id=%d enabled=%v err=%v", id, enabled, err)
	return view, err
}

func (a *App) PreviewAutomationRule(id uint) (services.AutomationRulePreview, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.dataCare.rules.PreviewAutomationRule(ctx, id)
}

func (a *App) RunAutomationRules(ids []uint) (services.AutomationRuleRunStatus, error) {
	status, err := a.dataCare.rules.RunAutomationRules(ids)
	log.Printf("API RunAutomationRules ids=%v err=%v", ids, err)
	return status, err
}

func (a *App) CancelAutomationRuleRun() services.AutomationRuleRunStatus {
	return a.dataCare.rules.CancelAutomationRuleRun()
}

// GetAutomationRuleRunStatus 返回手动运行的当前进度（规则页打开时取一次，之后跟 automation-rules-run 事件）。
func (a *App) GetAutomationRuleRunStatus() services.AutomationRuleRunStatus {
	return a.dataCare.rules.GetAutomationRuleRunStatus()
}
