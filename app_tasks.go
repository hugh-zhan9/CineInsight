package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"video-master/services"
)

func (a *App) StartLocalMetadataBackfill() (services.LocalMetadataBackfillStatus, error) {
	settings, err := a.settingsService.GetSettings()
	if err != nil {
		return services.LocalMetadataBackfillStatus{}, err
	}
	if !settings.LocalMetadataEnabled {
		return services.LocalMetadataBackfillStatus{}, fmt.Errorf("本地元数据自动补全已关闭")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.localMetadata.StartBackfill(ctx)
}

func (a *App) GetLocalMetadataBackfillStatus() services.LocalMetadataBackfillStatus {
	return a.localMetadata.BackfillStatus()
}

func (a *App) CancelLocalMetadataBackfill() error {
	return a.localMetadata.CancelBackfill()
}

func (a *App) StartLocalMetadataExport(request services.LocalMetadataExportRequest) (services.LocalMetadataExportStatus, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.localMetadata.StartExport(ctx, request)
}

func (a *App) GetLocalMetadataExportStatus() services.LocalMetadataExportStatus {
	return a.localMetadata.ExportStatus()
}

func (a *App) CancelLocalMetadataExport() error {
	return a.localMetadata.CancelExport()
}

// ===== 后台任务登记表与空闲调度（D-014、D-030..D-032）=====

// GetBackgroundTasks 返回当前运行中的后台任务 key（顺序固定）。
// 事件 background-tasks 推同一份数据。
func (a *App) GetBackgroundTasks() []string {
	return a.backgroundTasks.Snapshot()
}

// GetIdleSchedulerStatus 返回空闲调度总览：开关、阈值、当前空闲秒数、供电、等待清单。
func (a *App) GetIdleSchedulerStatus() services.IdleSchedulerStatus {
	return a.idleGate.GetIdleSchedulerStatus()
}

// RunGatedTaskNow 让一个被空闲门挡住的任务立即继续（"忽略空闲立即运行"）。
func (a *App) RunGatedTaskNow(taskKey string) error {
	err := a.idleGate.RunGatedTaskNow(taskKey)
	log.Printf("API RunGatedTaskNow task=%s err=%v", taskKey, err)
	return err
}

// runGatedAutoTask 是自动触发路径的统一入口（D-030）：先过空闲门，放行后把这一轮的
// 项间检查点交给服务，由服务在自己的锁里决定装不装。用户显式启动的路径永远不调用它。
//
// 装钩子与翻 Running 必须在服务的同一把锁里完成：在 app 层"先查是否在跑、再装钩子"
// 中间隔着一个窗口，用户正好在这个窗口里点了显式启动的话，他那一轮会继承这个门。
func (a *App) runGatedAutoTask(taskKey string, start func(context.Context, services.TaskPauseHook) error) error {
	err := a.idleGate.Run(a.backgroundContext(), taskKey, func(ctx context.Context) error {
		return start(ctx, a.idleGate.PauseHook(taskKey))
	})
	if errors.Is(err, services.ErrIdleGateTaskAlreadyWaiting) {
		// 同一任务已有一个自动唤醒在门口排队，本次合并进去，不是失败。
		return nil
	}
	return err
}

// triggerAITaggingAuto 是 AI 打标的自动唤醒路径：先过空闲门再叫醒 worker。
// worker 循环同时服务显式的 TriggerAITagging / RetryVideo，因此这里只挡"唤醒"，
// 不给循环装项间检查点（装了会连显式任务一起挡住，D-030 明令禁止）。
func (a *App) triggerAITaggingAuto(reason string) {
	err := a.runGatedAutoTask(string(services.BackgroundTaskAITagging), func(context.Context, services.TaskPauseHook) error {
		// worker 循环不装项间检查点，这里只管"叫醒"这一下。
		a.aiTaggingService.Trigger()
		return nil
	})
	if err != nil {
		log.Printf("AI 打标自动唤醒被中止 reason=%s err=%v", reason, err)
	}
}

func (a *App) StartTechnicalBackfill() (services.TechnicalBackfillStatus, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// Start 会在服务锁内摘掉当前这一轮的项间检查点，用户点的任务永不被空闲门挡住（D-030）。
	return a.technicalBackfill.Start(ctx)
}

func (a *App) GetTechnicalBackfillStatus() services.TechnicalBackfillStatus {
	return a.technicalBackfill.Status()
}

func (a *App) CancelTechnicalBackfill() error {
	return a.technicalBackfill.Cancel()
}

func (a *App) StartPerceptualHashBackfill() (services.PerceptualHashStatus, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// Start 会在服务锁内摘掉当前这一轮的项间检查点（D-030）。
	return a.perceptualHash.Start(ctx)
}

func (a *App) GetPerceptualHashBackfillStatus() services.PerceptualHashStatus {
	return a.perceptualHash.Status()
}

func (a *App) CancelPerceptualHashBackfill() error {
	return a.perceptualHash.Cancel()
}

// ===== Video Enhancement (P-013) =====

func (a *App) GetEnhancementCapability() services.EnhancementRuntimeCapability {
	return a.enhancement.Capability()
}

// GetEnhancementModelStatus 返回模型下载状态。
func (a *App) GetEnhancementModelStatus() services.EnhancementModelStatus {
	return a.enhancementModels.Status()
}

// StartEnhancementModelDownload 按需下载超分模型（用户在设置里主动发起）。
func (a *App) StartEnhancementModelDownload() (services.EnhancementModelStatus, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	required, err := a.enhancement.RequiredModelFiles()
	if err != nil {
		log.Printf("API StartEnhancementModelDownload err=%v", err)
		return services.EnhancementModelStatus{}, err
	}
	status, err := a.enhancementModels.Start(ctx, required)
	log.Printf("API StartEnhancementModelDownload running=%v err=%v", status.Running, err)
	return status, err
}

// CancelEnhancementModelDownload 取消正在进行的模型下载。
func (a *App) CancelEnhancementModelDownload() services.EnhancementModelStatus {
	a.enhancementModels.Cancel()
	return a.enhancementModels.Status()
}

func (a *App) CreateEnhancementTask(request services.EnhancementCreateRequest) (*services.EnhancementTaskView, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	view, err := a.enhancement.CreateTask(ctx, request)
	log.Printf("API CreateEnhancementTask video=%d profile=%s err=%v", request.VideoID, request.Profile, err)
	return view, err
}

func (a *App) GetEnhancementVideoPreflight(videoID uint) (*services.EnhancementVideoPreflight, error) {
	return a.enhancement.PreflightVideo(videoID)
}

func (a *App) ListEnhancementTasks(limit int) ([]services.EnhancementTaskView, error) {
	return a.enhancement.ListTasks(limit)
}

func (a *App) CancelEnhancementTask(taskID uint) error {
	err := a.enhancement.CancelTask(taskID)
	log.Printf("API CancelEnhancementTask task=%d err=%v", taskID, err)
	return err
}

func (a *App) RetryEnhancementTask(taskID uint) (*services.EnhancementTaskView, error) {
	view, err := a.enhancement.RetryTask(taskID)
	log.Printf("API RetryEnhancementTask task=%d err=%v", taskID, err)
	return view, err
}
