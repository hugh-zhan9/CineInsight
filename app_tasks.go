package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
	"video-master/models"
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
	log.Printf("API CreateEnhancementTask video=%d profile=%s copy_metadata=%v err=%v", request.VideoID, request.Profile, request.CopyMetadata, err)
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

// DiscardEnhancementProgress 放弃一个超分任务保留的进度（空间不足 / 取消时留下的检查点，I-3）：
// 删掉工作目录，结束码改为 checkpoint_discarded，之后重试从头开始。
func (a *App) DiscardEnhancementProgress(taskID uint) (*services.EnhancementTaskView, error) {
	view, err := a.enhancement.DiscardTaskProgress(taskID)
	log.Printf("API DiscardEnhancementProgress task=%d err=%v", taskID, err)
	return view, err
}

// ===== 任务中心适配表（D-PC18、APP-03）=====
//
// 每个后台任务 key 一个适配函数，把该服务既有的状态接口翻成任务中心的一项。表必须覆盖
// 登记表的全部 key（TestAPP03TaskCenterAdaptersCoverEveryRegistryKey 守住：登记表增减一个
// key 而这里没跟上，测试即失败）。
//
// 运行与否以登记表为准（buildTaskCenterItem 合并），适配函数报告的 running 只补登记表还没
// 反映出来的那一瞬（服务已置位、Begin 尚未调用）。canStart / canCancel 只在对应的零参绑定
// 确实存在时为真：需要参数的启动（超分、人脸、语义索引、播放代理、NFO 写出）与按单个任务取消的
// （超分、下载）不在 key 这一层给出，它们在 Recent 里逐条给。
//
// 批量任务的状态先经 xxxRun 换成 batchRun（纯函数，测试直接喂服务状态），再由适配函数补上
// canStart / canCancel；后两者必须以 `canStart:` / `canCancel:` 的字面量写在适配函数里，
// 前端 taskCommands.test.js 读这里的源码比对零参绑定。

type taskCenterKeyState struct {
	running   bool
	preparing bool
	progress  *TaskProgress
	runCounts *TaskRunCounts
	lastRun   *TaskLastRun
	canStart  bool
	canCancel bool
}

type taskCenterAdapter func(a *App, src *taskCenterSources) taskCenterKeyState

var taskCenterAdapters = map[services.BackgroundTaskKey]taskCenterAdapter{
	services.BackgroundTaskSubtitle:             (*App).taskCenterSubtitle,
	services.BackgroundTaskEnhancement:          (*App).taskCenterEnhancement,
	services.BackgroundTaskProxy:                (*App).taskCenterProxy,
	services.BackgroundTaskFace:                 (*App).taskCenterFace,
	services.BackgroundTaskFrameHash:            (*App).taskCenterFrameHash,
	services.BackgroundTaskPerceptualHash:       (*App).taskCenterPerceptualHash,
	services.BackgroundTaskTechnical:            (*App).taskCenterTechnical,
	services.BackgroundTaskLocalMetadata:        (*App).taskCenterLocalMetadata,
	services.BackgroundTaskLocalMetadataExport:  (*App).taskCenterLocalMetadataExport,
	services.BackgroundTaskSemantic:             (*App).taskCenterSemantic,
	services.BackgroundTaskImageSemantic:        (*App).taskCenterImageSemantic,
	services.BackgroundTaskAITagging:            (*App).taskCenterAITagging,
	services.BackgroundTaskImageAITagging:       (*App).taskCenterImageAITagging,
	services.BackgroundTaskAIReview:             (*App).taskCenterAIReview,
	services.BackgroundTaskImageAIReview:        (*App).taskCenterImageAIReview,
	services.BackgroundTaskEXIF:                 (*App).taskCenterEXIF,
	services.BackgroundTaskImagePerceptualHash:  (*App).taskCenterImagePerceptualHash,
	services.BackgroundTaskCleanup:              (*App).taskCenterCleanup,
	services.BackgroundTaskImageCleanup:         (*App).taskCenterImageCleanup,
	services.BackgroundTaskCleanupConsolidation: (*App).taskCenterCleanupConsolidation,
	services.BackgroundTaskCollectionSuggest:    (*App).taskCenterCollectionSuggest,
	services.BackgroundTaskBackup:               (*App).taskCenterBackup,
	services.BackgroundTaskBrowserDownload:      (*App).taskCenterBrowserDownload,
	services.BackgroundTaskWatchlistEnrich:      (*App).taskCenterWatchlistEnrich,
	services.BackgroundTaskMovieChart:           (*App).taskCenterMovieChart,
	services.BackgroundTaskSceneIndex:           (*App).taskCenterSceneIndex,
	services.BackgroundTaskVideoEdit:            (*App).taskCenterVideoEdit,
	services.BackgroundTaskLostMedia:            (*App).taskCenterLostMedia,
	services.BackgroundTaskMetadataBatchEdit:    (*App).taskCenterMetadataEdit,
	services.BackgroundTaskAutomationRules:      (*App).taskCenterAutomationRules,
	services.BackgroundTaskDataHealth:           (*App).taskCenterDataHealth,
}

// runningProgress 给运行中的批量任务报进度；总数还不知道（准备阶段）时为 nil。
func runningProgress(running bool, done, total int) *TaskProgress {
	if !running || total <= 0 {
		return nil
	}
	return &TaskProgress{Done: done, Total: total}
}

// batchRun 是批量任务一轮的状态，各 xxxRun 从自己服务的状态里原样搬过来（D-P3-05：不改服务的
// 统计口径）。finished 由各服务自己的「完成 / 取消（/ 中断）」标志决定；没有「跳过」「复用」
// 口径的服务对应字段留 0。
type batchRun struct {
	running    bool
	preparing  bool
	finished   bool
	cancelled  bool
	at         *time.Time
	total      int
	processed  int
	succeeded  int
	skipped    int
	failed     int
	reused     int
	outOfScope int
	failures   []string
}

// progress 是运行中的进度；准备阶段（总数还不知道）为 nil。
func (run batchRun) progress() *TaskProgress {
	return runningProgress(run.running, run.processed, run.total)
}

// runCounts 是运行中这一轮到目前为止的计数；不在跑时为 nil（这一轮的数在 lastRun 里）。
func (run batchRun) runCounts() *TaskRunCounts {
	if !run.running {
		return nil
	}
	return &TaskRunCounts{Succeeded: run.succeeded, Reused: run.reused, Failed: run.failed, OutOfScope: run.outOfScope}
}

// lastRun 是上一轮的结果；还在跑、或从没跑过时为 nil。总数为 0、没被取消也没有失败的一轮是
// 「已是最新」（NothingToDo）。
func (run batchRun) lastRun() *TaskLastRun {
	if !run.finished {
		return nil
	}
	failures := boundTaskFailures(run.failures)
	return &TaskLastRun{
		FinishedAt:  run.at,
		Succeeded:   run.succeeded,
		Skipped:     run.skipped,
		Failed:      run.failed,
		Failures:    failures,
		Cancelled:   run.cancelled,
		Reused:      run.reused,
		OutOfScope:  run.outOfScope,
		NothingToDo: !run.cancelled && run.total == 0 && run.failed == 0 && len(failures) == 0,
	}
}

// singleOutcomeRun 是「一轮就是一次分析」的任务（清理、建议作品集、备份）的上一轮结果。
func singleOutcomeRun(at *time.Time, failure string) *TaskLastRun {
	run := &TaskLastRun{FinishedAt: at, Failures: []string{}}
	if failure != "" {
		run.Failed = 1
		run.Failures = boundTaskFailures([]string{failure})
	} else {
		run.Succeeded = 1
	}
	return run
}

// cancelledOutcomeRun 是这类「一次分析」被用户取消的那一轮：没有结果，也不算失败。
func cancelledOutcomeRun(at *time.Time) *TaskLastRun {
	return &TaskLastRun{FinishedAt: at, Cancelled: true, Failures: []string{}}
}

// boundTaskFailures 按 LastRun 的约定截断：最多 taskCenterFailureLimit 条，每条最多
// taskCenterFailureMaxRunes 个字符。
func boundTaskFailures(failures []string) []string {
	bounded := make([]string, 0, min(len(failures), taskCenterFailureLimit))
	for _, failure := range failures {
		if len(bounded) == taskCenterFailureLimit {
			break
		}
		if runes := []rune(failure); len(runes) > taskCenterFailureMaxRunes {
			failure = string(runes[:taskCenterFailureMaxRunes])
		}
		bounded = append(bounded, failure)
	}
	return bounded
}

// taskFailure 把一条失败写成「名称：原因」；没有名称时只留原因。
func taskFailure(name, message string) string {
	if name == "" {
		return message
	}
	return name + "：" + message
}

func (a *App) taskCenterSubtitle(src *taskCenterSources) taskCenterKeyState {
	// 队列里有任务（含排队中、worker 还没取走的）就算在跑；CancelSubtitle 取消的是当前这一个。
	state := taskCenterKeyState{running: src.subtitleQueue.Total > 0, canCancel: src.subtitleQueue.ActiveTask != nil}
	for _, job := range src.subtitleJobs {
		switch job.Status {
		case services.SubtitleQueueTaskStatusQueued, services.SubtitleQueueTaskStatusRunning:
			continue
		}
		run := &TaskLastRun{FinishedAt: job.FinishedAt, Failures: []string{}}
		switch job.Status {
		case services.SubtitleQueueTaskStatusSucceeded:
			run.Succeeded = 1
		case services.SubtitleQueueTaskStatusFailed:
			run.Failed = 1
			run.Failures = boundTaskFailures([]string{taskFailure(job.VideoName, job.Message)})
		case services.SubtitleQueueTaskStatusCancelled:
			run.Cancelled = true
		}
		state.lastRun = run
		break
	}
	return state
}

func (a *App) taskCenterEnhancement(src *taskCenterSources) taskCenterKeyState {
	var state taskCenterKeyState
	for _, task := range src.enhancement {
		switch task.Status {
		case models.EnhancementStatusQueued, models.EnhancementStatusCancelRequested:
			continue
		case models.EnhancementStatusRunning:
			if state.progress == nil && task.TotalFrames > 0 {
				state.progress = &TaskProgress{Done: int(task.CommittedFrames), Total: int(task.TotalFrames)}
			}
			continue
		}
		if state.lastRun != nil {
			continue
		}
		// ListTasks 先列进行中的，再按更新时间倒序列历史：第一条历史就是最近结束的那个。
		run := &TaskLastRun{FinishedAt: task.FinishedAt, Failures: []string{}}
		switch task.Status {
		case models.EnhancementStatusCompleted:
			run.Succeeded = 1
		case models.EnhancementStatusFailed:
			run.Failed = 1
			reason := task.ErrorSummary
			if reason == "" {
				reason = task.ErrorCode
			}
			run.Failures = boundTaskFailures([]string{taskFailure(task.VideoName, reason)})
		case models.EnhancementStatusCancelled:
			run.Cancelled = true
		}
		state.lastRun = run
	}
	return state
}

func (a *App) taskCenterProxy(src *taskCenterSources) taskCenterKeyState {
	run := playbackProxyRun(src.proxy)
	return taskCenterKeyState{
		running:   run.running,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

// playbackProxyRun：跳过数是服务按 already_exists / in_progress / cancelled 计的 Skipped，
// 失败明细是逐项结果里的失败项。
func playbackProxyRun(status services.PlaybackProxyStatus) batchRun {
	failures := make([]string, 0)
	for _, result := range status.Results {
		if playbackProxyResultFailed(result.Code) {
			failures = append(failures, taskFailure(result.Name, result.Message))
		}
	}
	return batchRun{
		running: status.Running, finished: !status.Running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed, failures: failures,
	}
}

func (a *App) taskCenterFace(src *taskCenterSources) taskCenterKeyState {
	if a.faceAnalysis == nil {
		return taskCenterKeyState{}
	}
	run := faceAnalysisRun(a.faceAnalysis.Status())
	return taskCenterKeyState{
		running:   run.running,
		preparing: run.preparing,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

// faceAnalysisRun：人脸分析没有跳过口径；中途 sidecar 没了（Interrupted）也算这一轮结束，
// 整轮的错误（LastError）并进失败明细。
func faceAnalysisRun(status services.FaceAnalysisStatus) batchRun {
	running := status.Running || status.Preparing
	failures := make([]string, 0, len(status.Failures)+1)
	for _, failure := range status.Failures {
		failures = append(failures, taskFailure(failure.Name, failure.Error))
	}
	if status.LastError != "" {
		failures = append(failures, status.LastError)
	}
	return batchRun{
		running: running, preparing: status.Preparing,
		finished:  !running && (status.Completed || status.Cancelled || status.Interrupted),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, failed: status.Failed, failures: failures,
	}
}

func (a *App) taskCenterFrameHash(src *taskCenterSources) taskCenterKeyState {
	if a.frameHash == nil {
		return taskCenterKeyState{}
	}
	run := frameHashRun(a.frameHash.Status())
	return taskCenterKeyState{
		running:   run.running,
		preparing: run.preparing,
		canStart:  true,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

func frameHashRun(status services.FrameHashStatus) batchRun {
	running := status.Running || status.Preparing
	failures := make([]string, 0, len(status.Failures))
	for _, failure := range status.Failures {
		failures = append(failures, taskFailure(failure.Name, failure.Error))
	}
	return batchRun{
		running: running, preparing: status.Preparing, finished: !running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed, failures: failures,
	}
}

// taskCenterSceneIndex：画面索引的启动要选范围（全部 / 当前筛选），不给零参启动；在跑时可取消。
func (a *App) taskCenterSceneIndex(src *taskCenterSources) taskCenterKeyState {
	if a.sceneIndex == nil {
		return taskCenterKeyState{}
	}
	run := sceneIndexRun(a.sceneIndex.Status())
	return taskCenterKeyState{
		running:   run.running,
		preparing: run.preparing,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

func sceneIndexRun(status services.SceneIndexStatus) batchRun {
	running := status.Running || status.Preparing
	failures := make([]string, 0, len(status.Failures))
	for _, failure := range status.Failures {
		failures = append(failures, taskFailure(failure.Name, failure.Error))
	}
	return batchRun{
		running: running, preparing: status.Preparing, finished: !running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed, failures: failures,
	}
}

func (a *App) taskCenterPerceptualHash(src *taskCenterSources) taskCenterKeyState {
	if a.perceptualHash == nil {
		return taskCenterKeyState{}
	}
	run := perceptualHashRun(a.perceptualHash.Status())
	return taskCenterKeyState{
		running:   run.running,
		canStart:  true,
		canCancel: run.running,
		progress:  run.progress(),
		runCounts: run.runCounts(),
		lastRun:   run.lastRun(),
	}
}

// perceptualHashRun：近重复指纹另有「已有指纹复用」与「已移出处理范围」两项（片库页状态条原有的口径）。
// 服务逐项累计这几个数，在跑时也经 runCounts 给出（状态条原来在跑时就列）。
func perceptualHashRun(status services.PerceptualHashStatus) batchRun {
	failures := make([]string, 0, len(status.Failures))
	for _, failure := range status.Failures {
		failures = append(failures, taskFailure(failure.Name, failure.Error))
	}
	return batchRun{
		running: status.Running, finished: !status.Running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed,
		reused: status.Reused, outOfScope: status.OutOfScope, failures: failures,
	}
}

func (a *App) taskCenterTechnical(src *taskCenterSources) taskCenterKeyState {
	if a.technicalBackfill == nil {
		return taskCenterKeyState{}
	}
	run := technicalBackfillRun(a.technicalBackfill.Status())
	return taskCenterKeyState{
		running:   run.running,
		preparing: run.preparing,
		canStart:  true,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

func technicalBackfillRun(status services.TechnicalBackfillStatus) batchRun {
	running := status.Running || status.Preparing
	failures := make([]string, 0, len(status.Failures))
	for _, failure := range status.Failures {
		failures = append(failures, taskFailure(failure.Name, failure.Error))
	}
	return batchRun{
		running: running, preparing: status.Preparing, finished: !running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed, failures: failures,
	}
}

// taskCenterLocalMetadata：本地资料补全。NFO 写出 D-P3-05 起单独登记为 local_metadata_export，
// 两者各报各的运行、进度与上一轮；这里的启动与取消只作用于补全（与命令面板同一口径）。
func (a *App) taskCenterLocalMetadata(src *taskCenterSources) taskCenterKeyState {
	if a.localMetadata == nil {
		return taskCenterKeyState{}
	}
	run := localMetadataBackfillRun(a.localMetadata.BackfillStatus())
	return taskCenterKeyState{
		running:   run.running,
		canStart:  true,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

// taskCenterLocalMetadataExport：NFO 写出要选筛选范围，只从片库页发起，不给零参启动；在跑时可取消
// （CancelLocalMetadataExport）。
func (a *App) taskCenterLocalMetadataExport(src *taskCenterSources) taskCenterKeyState {
	if a.localMetadata == nil {
		return taskCenterKeyState{}
	}
	run := localMetadataExportRun(a.localMetadata.ExportStatus())
	return taskCenterKeyState{
		running:   run.running,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

func localMetadataFailureMessages(failures []services.LocalMetadataFailure) []string {
	messages := make([]string, 0, len(failures))
	for _, failure := range failures {
		messages = append(messages, failure.Message)
	}
	return messages
}

func localMetadataBackfillRun(status services.LocalMetadataBackfillStatus) batchRun {
	return batchRun{
		running: status.Running, finished: !status.Running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed,
		failures: localMetadataFailureMessages(status.Failures),
	}
}

// localMetadataExportRun：写出没有跳过口径。
func localMetadataExportRun(status services.LocalMetadataExportStatus) batchRun {
	return batchRun{
		running: status.Running, finished: !status.Running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, failed: status.Failed,
		failures: localMetadataFailureMessages(status.Failures),
	}
}

func (a *App) taskCenterSemantic(src *taskCenterSources) taskCenterKeyState {
	svc := a.semanticIndexService()
	if svc == nil {
		return taskCenterKeyState{}
	}
	run := semanticIndexRun(svc.Status())
	return taskCenterKeyState{
		running:   run.running,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

func semanticIndexRun(status services.SemanticIndexStatus) batchRun {
	failures := make([]string, 0, len(status.Failures))
	for _, failure := range status.Failures {
		failures = append(failures, taskFailure(failure.Name, failure.Error))
	}
	return batchRun{
		running: status.Running, finished: !status.Running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed, failures: failures,
	}
}

func (a *App) taskCenterImageSemantic(src *taskCenterSources) taskCenterKeyState {
	svc := a.imageSemanticIndexService()
	if svc == nil {
		return taskCenterKeyState{}
	}
	status := svc.Status()
	run := imageSemanticIndexRun(status)
	return taskCenterKeyState{
		running:   run.running,
		canStart:  status.Available,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

func imageSemanticIndexRun(status services.ImageSemanticIndexStatus) batchRun {
	failures := make([]string, 0, len(status.Failures))
	for _, failure := range status.Failures {
		failures = append(failures, taskFailure(failure.Name, failure.Error))
	}
	return batchRun{
		running: status.Running, finished: !status.Running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed, failures: failures,
	}
}

// taskCenterAITagging：视频 AI 打标没有内存里的「本轮」状态，运行与否只看登记表；上一轮给的是
// 按状态累计的完成数与失败数（StatusSummary，与待处理汇总同一口径），数据库不可读时不给。
func (a *App) taskCenterAITagging(src *taskCenterSources) taskCenterKeyState {
	state := taskCenterKeyState{canStart: a.aiTaggingService != nil}
	if a.aiTaggingService == nil || src.dbUnavailable != "" {
		return state
	}
	summary, err := a.aiTaggingService.StatusSummary()
	if err != nil {
		log.Printf("API GetTaskCenterSnapshot ai tagging summary err=%v", err)
		src.warn("视频 AI 打标状态读取失败")
		return state
	}
	if summary.Completed+summary.Failed > 0 {
		state.lastRun = &TaskLastRun{Succeeded: int(summary.Completed), Failed: int(summary.Failed), Failures: []string{}}
	}
	return state
}

func (a *App) taskCenterImageAITagging(src *taskCenterSources) taskCenterKeyState {
	svc := a.imageAITaggingService()
	if svc == nil {
		return taskCenterKeyState{}
	}
	run := imageAITaggingRun(svc.GetImageAITaggingStatus())
	return taskCenterKeyState{
		running:   run.running,
		canStart:  true,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

func imageAITaggingRun(status services.ImageAITaggingStatus) batchRun {
	failures := make([]string, 0, len(status.Failures))
	for _, failure := range status.Failures {
		failures = append(failures, taskFailure(failure.Name, failure.Error))
	}
	return batchRun{
		running: status.Running, finished: !status.Running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed, failures: failures,
	}
}

func (a *App) taskCenterEXIF(src *taskCenterSources) taskCenterKeyState {
	if a.imageEXIFBackfill == nil {
		return taskCenterKeyState{}
	}
	run := imageEXIFRun(a.imageEXIFBackfill.GetImageEXIFBackfillStatus())
	return taskCenterKeyState{
		running:   run.running,
		canStart:  true,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

func imageEXIFRun(status services.ImageEXIFBackfillStatus) batchRun {
	failures := make([]string, 0, len(status.Failures))
	for _, failure := range status.Failures {
		failures = append(failures, taskFailure(failure.Name, failure.Error))
	}
	return batchRun{
		running: status.Running, finished: !status.Running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed, failures: failures,
	}
}

func (a *App) taskCenterImagePerceptualHash(src *taskCenterSources) taskCenterKeyState {
	if a.imagePHashBackfill == nil {
		return taskCenterKeyState{}
	}
	run := imagePerceptualHashRun(a.imagePHashBackfill.GetImagePerceptualHashBackfillStatus())
	return taskCenterKeyState{
		running:   run.running,
		canStart:  true,
		canCancel: run.running,
		progress:  run.progress(),
		lastRun:   run.lastRun(),
	}
}

func imagePerceptualHashRun(status services.ImagePerceptualHashBackfillStatus) batchRun {
	failures := make([]string, 0, len(status.Failures))
	for _, failure := range status.Failures {
		failures = append(failures, taskFailure(failure.Name, failure.Error))
	}
	return batchRun{
		running: status.Running, finished: !status.Running && (status.Completed || status.Cancelled),
		cancelled: status.Cancelled, at: status.UpdatedAt, total: status.Total, processed: status.Processed,
		succeeded: status.Succeeded, skipped: status.Skipped, failed: status.Failed, failures: failures,
	}
}

// taskCenterCleanup：CleanupService.Status 回读时要按审阅决定过滤缓存结果（读库），数据库
// 不可读时它的 Error 会是底层的英文错误，因此这时只报运行与进度、不报上一轮。
func (a *App) taskCenterCleanup(src *taskCenterSources) taskCenterKeyState {
	if a.cleanupService == nil {
		return taskCenterKeyState{}
	}
	status := a.cleanupService.Status()
	state := taskCenterKeyState{
		running:   status.Running,
		canStart:  true,
		canCancel: status.Running,
		progress:  runningProgress(status.Running, status.Progress.Current, status.Progress.Total),
	}
	if status.Running || src.dbUnavailable != "" {
		return state
	}
	state.lastRun = cleanupLastRun(status.Completed, status.Cancelled, status.Error, status.UpdatedAt)
	return state
}

// cleanupLastRun 是视频 / 图片清理分析的上一轮：完成、取消、出错三选一，都没有就是没跑过。
func cleanupLastRun(completed, cancelled bool, failure string, at *time.Time) *TaskLastRun {
	switch {
	case completed:
		return singleOutcomeRun(at, "")
	case cancelled:
		return cancelledOutcomeRun(at)
	case failure != "":
		return singleOutcomeRun(at, failure)
	}
	return nil
}

func (a *App) taskCenterImageCleanup(src *taskCenterSources) taskCenterKeyState {
	if a.imageCleanupService == nil {
		return taskCenterKeyState{}
	}
	status := a.imageCleanupService.GetImageCleanupStatus()
	state := taskCenterKeyState{
		running:   status.Running,
		canStart:  true,
		canCancel: status.Running,
		progress:  runningProgress(status.Running, status.Progress.Current, status.Progress.Total),
	}
	if status.Running {
		return state
	}
	state.lastRun = cleanupLastRun(status.Completed, status.Cancelled, status.Error, status.UpdatedAt)
	return state
}

func (a *App) taskCenterCollectionSuggest(src *taskCenterSources) taskCenterKeyState {
	if a.collectionSuggestions == nil {
		return taskCenterKeyState{}
	}
	status := a.collectionSuggestions.Status()
	state := taskCenterKeyState{
		running:   status.Running,
		canStart:  true,
		canCancel: status.Running,
		progress:  runningProgress(status.Running, status.Scanned, status.Total),
	}
	if status.Running {
		return state
	}
	state.lastRun = collectionSuggestLastRun(status)
	return state
}

// collectionSuggestLastRun：出错优先于完成（服务出错时也可能置了 Completed），再看取消。
func collectionSuggestLastRun(status services.CollectionSuggestionStatus) *TaskLastRun {
	switch {
	case status.LastError != "":
		return singleOutcomeRun(status.UpdatedAt, status.LastError)
	case status.Completed:
		return singleOutcomeRun(status.UpdatedAt, "")
	case status.Cancelled:
		return cancelledOutcomeRun(status.UpdatedAt)
	}
	return nil
}

// taskCenterBackup：备份状态要读设置（最近一次尝试与错误都记在设置行里），数据库不可读时
// 运行与否只看登记表，也不给「启动」——维护期间手动备份本来就会被拒绝。
func (a *App) taskCenterBackup(src *taskCenterSources) taskCenterKeyState {
	if a.backupService == nil || src.dbUnavailable != "" {
		return taskCenterKeyState{}
	}
	status := a.backupService.GetStatus()
	state := taskCenterKeyState{running: status.Running, canStart: !status.Running && status.BackupAvailable}
	if status.LastAttemptAt != nil {
		state.lastRun = singleOutcomeRun(status.LastAttemptAt, status.LastError)
	}
	return state
}

// taskCenterBrowserDownload：运行与否看登记表（每个在下的任务各登记一次）；取消与重试按单个
// 任务在 Recent 里给。上一轮是最近结束（完成或失败）的那一个下载。
func (a *App) taskCenterBrowserDownload(src *taskCenterSources) taskCenterKeyState {
	var state taskCenterKeyState
	var latest *services.BrowserDownloadTask
	for index := range src.downloads {
		task := &src.downloads[index]
		if task.State != browserDownloadDone && task.State != browserDownloadFailed {
			continue
		}
		if latest == nil || task.FinishedAt > latest.FinishedAt {
			latest = task
		}
	}
	if latest != nil {
		failure := ""
		if latest.State == browserDownloadFailed {
			title := latest.Title
			if title == "" {
				title = latest.Filename
			}
			failure = taskFailure(title, latest.Error)
		}
		state.lastRun = singleOutcomeRun(unixMilliTime(latest.FinishedAt), failure)
	}
	return state
}

// taskCenterWatchlistEnrich：片单补全只有逐条目的状态（在片单页），没有本轮进度，也没有零参的
// 启动 / 取消绑定；任务中心只报它在不在跑。
func (a *App) taskCenterWatchlistEnrich(src *taskCenterSources) taskCenterKeyState {
	return taskCenterKeyState{}
}

func (a *App) taskCenterMovieChart(src *taskCenterSources) taskCenterKeyState {
	svc := a.movieChartService()
	if svc == nil {
		return taskCenterKeyState{}
	}
	_, running := svc.RefreshStatus()
	return taskCenterKeyState{running: running, canCancel: running}
}

// Consolidation has no zero-argument start: a reviewed preview is always required.
func (a *App) taskCenterCleanupConsolidation(src *taskCenterSources) taskCenterKeyState {
	local := src.running[string(services.BackgroundTaskCleanupConsolidation)]
	state := taskCenterKeyState{canCancel: local}
	for _, task := range src.consolidations {
		if task.Status == "running" {
			state.running = true
			state.progress = runningProgress(true, task.Completed, task.Total)
		} else if state.lastRun == nil {
			failed := 0
			if task.Status == "failed" {
				failed = 1
			}
			failures := []string{}
			if task.Error != "" {
				failures = append(failures, task.Error)
			}
			state.lastRun = &TaskLastRun{
				FinishedAt: task.FinishedAt, Succeeded: task.Completed, Failed: failed,
				Failures: boundTaskFailures(failures), Cancelled: task.Status == "cancelled",
			}
		}
	}
	return state
}

func (a *App) taskCenterAIReview(_ *taskCenterSources) taskCenterKeyState {
	return a.taskCenterReviewApproval("video")
}
func (a *App) taskCenterImageAIReview(_ *taskCenterSources) taskCenterKeyState {
	return a.taskCenterReviewApproval("image")
}
func (a *App) taskCenterReviewApproval(kind string) taskCenterKeyState {
	workflow, err := a.aiReviewWorkflow(kind)
	if err != nil {
		return taskCenterKeyState{}
	}
	state, err := workflow.State("", 0, 0)
	if err != nil {
		return taskCenterKeyState{}
	}
	running := state.State == "running"
	return taskCenterKeyState{running: running, canCancel: running, progress: runningProgress(running, state.Processed, state.Total), lastRun: reviewApprovalLastRun(state)}
}

// reviewApprovalLastRun 是批量批准的上一批；中止（failed）带一条说明，取消（cancelled）单独标出。
func reviewApprovalLastRun(state services.ReviewApprovalState) *TaskLastRun {
	if state.FinishedAt == nil {
		return nil
	}
	run := &TaskLastRun{FinishedAt: state.FinishedAt, Succeeded: state.Succeeded, Skipped: state.Skipped, Failed: state.Failed, Remaining: state.Remaining, Failures: []string{}, Cancelled: state.State == "cancelled"}
	if state.State == "failed" {
		run.Failures = []string{"批量批准中止；请返回审阅面板查看结果，未处理项需要重新预览。"}
	}
	return run
}

// taskCenterVideoEdit：视频工作台按项目汇总（运行中项目的已完成/总项数为进度，最近结束的项目为上一轮）。
// 取消与「继续」按项目给出（Recent 里的 video_edit 条目），key 这一层没有零参动作。
func (a *App) taskCenterVideoEdit(src *taskCenterSources) taskCenterKeyState {
	var state taskCenterKeyState
	for _, project := range src.videoEdits {
		switch project.Status {
		case "queued", "running":
			if project.Status == "running" {
				state.running = true
			}
			if state.progress == nil && project.ItemCount > 0 {
				state.progress = &TaskProgress{Done: project.CompletedCount, Total: project.ItemCount}
			}
			continue
		case "completed", "partial", "failed", "cancelled", "interrupted":
		default:
			continue
		}
		if state.lastRun != nil {
			continue
		}
		run := &TaskLastRun{FinishedAt: project.FinishedAt, Failures: []string{}, Succeeded: project.CompletedCount, Cancelled: project.Status == "cancelled"}
		if failed := project.ItemCount - project.CompletedCount; failed > 0 && project.Status != "cancelled" {
			run.Failed = failed
			reason := project.ErrorMessage
			if reason == "" {
				reason = project.ErrorCode
			}
			run.Failures = boundTaskFailures([]string{taskFailure(project.Title, reason)})
		}
		state.lastRun = run
	}
	return state
}
