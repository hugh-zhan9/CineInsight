package main

import (
	"log"
	"strconv"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// 任务中心（D-PC18、APP-03）。
//
// 这里只做读侧聚合（概要设计图 3.3）：每个后台任务 key 的状态取自登记表、空闲门等待清单
// 与该服务既有的状态接口，App 层不新建状态源，写操作仍走各服务既有的绑定。key → 适配函数
// 的表在 app_tasks.go（taskCenterAdapters），必须覆盖登记表的全部 key，由测试守住。

// TaskCenterItem.State 的三种取值。running 优先：任务跑到一半停在项间检查点上等空闲时，
// State 仍是 running，GateReason 给出它在等什么。
const (
	TaskCenterStateRunning     = "running"
	TaskCenterStateWaitingIdle = "waiting_idle"
	TaskCenterStateIdle        = "idle"
)

// TaskCenterItem.Actions 的取值，每个都对应一个已有的零参绑定（由前端映射）：
//   - start：该 key 的显式启动绑定（例如 StartPerceptualHashBackfill、TriggerAITagging）；
//   - cancel：该 key 的取消绑定（例如 CancelPlaybackProxyTask、CancelSubtitle）；
//   - run_now：RunGatedTaskNow(key)。
//
// 设计里的 retry_failed 目前没有任何 key 有零参的「重试失败项」绑定，因此不会出现。
const (
	TaskCenterActionStart  = "start"
	TaskCenterActionCancel = "cancel"
	TaskCenterActionRunNow = "run_now"
)

// TaskRecentJob.Kind 的取值（D-PC18：字幕、超分、下载、播放代理另附最近的任务）。
const (
	TaskRecentKindSubtitle    = "subtitle"
	TaskRecentKindEnhancement = "enhancement"
	TaskRecentKindDownload    = "download"
	TaskRecentKindProxy       = "proxy"
)

const (
	// taskCenterChangedEvent 在登记表、空闲门或任一服务状态事件之后合并 taskCenterChangeDelay 再发，
	// 载荷是当时的 TaskCenterSnapshot。
	taskCenterChangedEvent = "task-center-changed"
	// taskCenterFailureLimit / taskCenterFailureMaxRunes 是 LastRun.Failures 的条数与单条长度上限。
	taskCenterFailureLimit    = 50
	taskCenterFailureMaxRunes = 500
	// taskCenterRecentPerKind 是 Recent 里每一类最多列出的已结束任务数（进行中的另计）。
	taskCenterRecentPerKind = 20
)

// TaskProgress 是运行中任务的进度；没有进度语义（或总数还不知道）时整个字段为 nil。
type TaskProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// TaskLastRun 是最近一轮的结果。批量任务是上一轮的成功 / 失败计数与失败明细；以单个任务为
// 单位的 key（字幕、超分、下载）是最近结束的那一个任务；视频 AI 打标没有「轮」的概念，
// 给的是按状态累计的完成数与失败数（FinishedAt 为空）。
type TaskLastRun struct {
	FinishedAt *time.Time `json:"finished_at" ts_type:"string"`
	Succeeded  int        `json:"succeeded"`
	Failed     int        `json:"failed"`
	Failures   []string   `json:"failures"`
}

// TaskCenterItem 是任务中心里一个后台任务 key 的状态。
type TaskCenterItem struct {
	Key        string        `json:"key"`
	State      string        `json:"state"`
	GateReason string        `json:"gate_reason"`
	Progress   *TaskProgress `json:"progress"`
	LastRun    *TaskLastRun  `json:"last_run"`
	Actions    []string      `json:"actions"`
}

// TaskRecentJob 是字幕、超分、下载、播放代理四类任务的逐条记录（进行中与最近结束的）。
//
// ID 统一为字符串：下载任务是 task_uid，其余三类是十进制的数字 ID（播放代理是视频 ID），
// 调用 CancelSubtitleTask 等数字参数的绑定前由前端转换。Status 是各类任务自己的状态码，
// 文案由前端翻译；Actions 同样按各类任务已有的绑定给出：
//   - subtitle：cancel / force / discard / retry（与 ListSubtitleJobs 同一份）；
//   - enhancement：cancel、retry、reveal_output（OpenDirectory(output_video_id)）、open_output_in_library；
//   - download：cancel、retry、reveal、add_directory_to_scan、reimport；
//   - proxy：retry（CreatePlaybackProxy(video_id)）。
type TaskRecentJob struct {
	Kind          string     `json:"kind"`
	ID            string     `json:"id"`
	VideoID       uint       `json:"video_id"`
	OutputVideoID uint       `json:"output_video_id,omitempty"`
	Title         string     `json:"title"`
	Status        string     `json:"status"`
	ErrorCode     string     `json:"error_code,omitempty"`
	Message       string     `json:"message"`
	FinishedAt    *time.Time `json:"finished_at" ts_type:"string"`
	Actions       []string   `json:"actions"`
}

// TaskCenterSnapshot 是 GetTaskCenterSnapshot 的结果。Items 恒为登记表的全部 key、按面板顺序；
// Warnings 是读不到的那部分的中文说明（数据库维护或待重启、某类任务记录读取失败），不含原始错误。
type TaskCenterSnapshot struct {
	Items    []TaskCenterItem `json:"items"`
	Recent   []TaskRecentJob  `json:"recent"`
	Warnings []string         `json:"warnings"`
}

// taskCenterSources 是一次快照共用的输入：登记表、空闲门与需要读库的几份任务列表只各读一次，
// Items 与 Recent 看到的是同一份数据。
type taskCenterSources struct {
	// dbUnavailable 非空时数据库此刻不可读（维护围栏、待重启、未初始化），读库的部分一律跳过。
	dbUnavailable string
	warnings      []string
	running       map[string]bool
	waiting       map[string]string

	subtitleQueue services.SubtitleQueueSnapshot
	subtitleJobs  []services.SubtitleJobItem
	enhancement   []services.EnhancementTaskView
	downloads     []services.BrowserDownloadTask
	proxy         services.PlaybackProxyStatus
}

func (src *taskCenterSources) warn(message string) {
	for _, existing := range src.warnings {
		if existing == message {
			return
		}
	}
	src.warnings = append(src.warnings, message)
}

// GetTaskCenterSnapshot 返回任务中心快照（D-PC18）。
func (a *App) GetTaskCenterSnapshot() TaskCenterSnapshot {
	src := a.loadTaskCenterSources()
	snapshot := TaskCenterSnapshot{
		Items:  make([]TaskCenterItem, 0, len(services.BackgroundTaskKeys())),
		Recent: a.taskCenterRecent(src),
	}
	for _, key := range services.BackgroundTaskKeys() {
		var state taskCenterKeyState
		if adapter, ok := taskCenterAdapters[services.BackgroundTaskKey(key)]; ok {
			state = adapter(a, src)
		}
		snapshot.Items = append(snapshot.Items, buildTaskCenterItem(key, state, src))
	}
	snapshot.Warnings = append([]string{}, src.warnings...)
	return snapshot
}

func (a *App) loadTaskCenterSources() *taskCenterSources {
	src := &taskCenterSources{
		dbUnavailable: a.databaseUnavailableReason(),
		warnings:      []string{},
		running:       map[string]bool{},
		waiting:       map[string]string{},
	}
	for _, key := range a.backgroundTasks.Snapshot() {
		src.running[key] = true
	}
	if a.idleGate != nil {
		for _, waiting := range a.idleGate.GetIdleSchedulerStatus().Waiting {
			src.waiting[waiting.TaskKey] = waiting.Reason
		}
	}
	if a.subtitleService != nil {
		src.subtitleQueue = a.subtitleService.GetSubtitleQueueState()
	}
	src.proxy = a.playbackProxies.Status()
	if src.dbUnavailable != "" {
		src.warn(src.dbUnavailable)
		if a.browserDownloads != nil {
			// 历史行在库里：维护期间只列本次会话内存里的任务。
			src.downloads = a.browserDownloads.ListTasks()
		}
		return src
	}
	if a.subtitleService != nil {
		jobs, err := a.subtitleService.ListSubtitleJobs(taskCenterRecentPerKind)
		if err != nil {
			log.Printf("API GetTaskCenterSnapshot list subtitle jobs err=%v", err)
			src.warn("字幕任务记录读取失败")
		}
		src.subtitleJobs = jobs
	}
	if a.enhancement != nil {
		tasks, err := a.enhancement.ListTasks(taskCenterRecentPerKind)
		if err != nil {
			log.Printf("API GetTaskCenterSnapshot list enhancement tasks err=%v", err)
			src.warn("超分任务记录读取失败")
		}
		src.enhancement = tasks
	}
	if a.browserDownloads != nil {
		src.downloads = a.browserDownloads.ListDownloadTasks()
	}
	return src
}

// databaseUnavailableReason 报告数据库此刻是否可读；可读时返回空串。不可读的状态：切换后端
// 成功或只改配置成功后的「待重启」终态、恢复成功后的终态（围栏一直保持到退出）、恢复或切换
// 进行中的维护围栏、数据库未初始化。这时任何读库都会得到英文的底层错误，调用方据此跳过读库，
// 改为给出这里的中文说明。
func (a *App) databaseUnavailableReason() string {
	if a.databaseSwitchService != nil && a.databaseSwitchService.RelaunchPending() {
		return "数据库后端已切换，请重启应用后再查看"
	}
	if database.MaintenanceActive() {
		// 恢复与切换全程持有 restoreMu；拿得到锁说明已经不在进行中，终态标志可以读。
		if a.restoreMu.TryLock() {
			terminal := a.restoreTerminal
			a.restoreMu.Unlock()
			if terminal {
				return "数据库已恢复，应用即将退出，请重新打开后再查看"
			}
		}
		return "数据库正在恢复或切换，暂时无法读取"
	}
	if database.DB == nil {
		return "数据库未初始化"
	}
	return ""
}

func buildTaskCenterItem(key string, state taskCenterKeyState, src *taskCenterSources) TaskCenterItem {
	running := src.running[key] || state.running
	reason, waiting := src.waiting[key]
	item := TaskCenterItem{
		Key:        key,
		State:      TaskCenterStateIdle,
		GateReason: reason,
		LastRun:    state.lastRun,
		Actions:    []string{},
	}
	switch {
	case running:
		item.State = TaskCenterStateRunning
		item.Progress = state.progress
	case waiting:
		item.State = TaskCenterStateWaitingIdle
	}
	if running && state.canCancel {
		item.Actions = append(item.Actions, TaskCenterActionCancel)
	}
	if waiting {
		item.Actions = append(item.Actions, TaskCenterActionRunNow)
	}
	if !running && !waiting && state.canStart {
		item.Actions = append(item.Actions, TaskCenterActionStart)
	}
	return item
}

// ===== 最近任务（Recent）=====

func (a *App) taskCenterRecent(src *taskCenterSources) []TaskRecentJob {
	recent := make([]TaskRecentJob, 0)
	for _, job := range src.subtitleJobs {
		recent = append(recent, TaskRecentJob{
			Kind:       TaskRecentKindSubtitle,
			ID:         strconv.FormatUint(uint64(job.ID), 10),
			VideoID:    job.VideoID,
			Title:      job.VideoName,
			Status:     string(job.Status),
			ErrorCode:  job.ErrorCode,
			Message:    job.Message,
			FinishedAt: job.FinishedAt,
			Actions:    append([]string{}, job.Actions...),
		})
	}
	for _, task := range src.enhancement {
		recent = append(recent, enhancementRecentJob(task))
	}
	for index, task := range src.downloads {
		if index >= taskCenterRecentPerKind && !browserDownloadActive(task.State) {
			continue
		}
		recent = append(recent, downloadRecentJob(task))
	}
	return append(recent, proxyRecentJobs(src.proxy)...)
}

func enhancementRecentJob(task services.EnhancementTaskView) TaskRecentJob {
	job := TaskRecentJob{
		Kind:       TaskRecentKindEnhancement,
		ID:         strconv.FormatUint(uint64(task.ID), 10),
		VideoID:    task.VideoID,
		Title:      task.VideoName,
		Status:     task.Status,
		ErrorCode:  task.ErrorCode,
		Message:    task.ErrorSummary,
		FinishedAt: task.FinishedAt,
		Actions:    []string{},
	}
	switch task.Status {
	case models.EnhancementStatusQueued, models.EnhancementStatusRunning:
		job.Actions = append(job.Actions, "cancel")
	case models.EnhancementStatusFailed, models.EnhancementStatusCancelled:
		job.Actions = append(job.Actions, "retry")
	case models.EnhancementStatusCompleted:
		if task.OutputVideoID != nil && *task.OutputVideoID != 0 {
			job.OutputVideoID = *task.OutputVideoID
			job.Actions = append(job.Actions, "reveal_output", "open_output_in_library")
		}
	}
	return job
}

// 浏览器下载任务的状态字面量（services 包里的常量未导出，取值见 browser_download_service.go）。
const (
	browserDownloadQueued    = "queued"
	browserDownloadRunning   = "running"
	browserDownloadImporting = "importing"
	browserDownloadDone      = "done"
	browserDownloadFailed    = "failed"
)

func browserDownloadActive(state string) bool {
	return state == browserDownloadQueued || state == browserDownloadRunning || state == browserDownloadImporting
}

func downloadRecentJob(task services.BrowserDownloadTask) TaskRecentJob {
	title := task.Title
	if title == "" {
		title = task.Filename
	}
	job := TaskRecentJob{
		Kind:       TaskRecentKindDownload,
		ID:         task.ID,
		VideoID:    task.VideoID,
		Title:      title,
		Status:     task.State,
		Message:    task.Error,
		FinishedAt: unixMilliTime(task.FinishedAt),
		Actions:    []string{},
	}
	if task.ImportStatus != "" && task.ImportStatus != services.BrowserDownloadImportImported {
		job.ErrorCode = task.ImportStatus
		if job.Message == "" {
			job.Message = task.ImportError
		}
	}
	if browserDownloadActive(task.State) {
		job.Actions = append(job.Actions, "cancel")
	}
	if task.Retryable {
		job.Actions = append(job.Actions, "retry")
	}
	if task.State == browserDownloadDone {
		job.Actions = append(job.Actions, "reveal")
	}
	switch task.ImportStatus {
	case services.BrowserDownloadImportNotInScanRoots:
		job.Actions = append(job.Actions, "add_directory_to_scan", "reimport")
	case services.BrowserDownloadImportFailed:
		job.Actions = append(job.Actions, "reimport")
	}
	return job
}

func unixMilliTime(milli int64) *time.Time {
	if milli <= 0 {
		return nil
	}
	value := time.UnixMilli(milli)
	return &value
}

// proxyRecentJobs 列出播放代理这一轮正在处理的一项与最近的逐项结果（新的在前）。逐项结果只有
// 这一轮的，没有单独的结束时间；本轮已结束时统一用本轮的结束时间。
func proxyRecentJobs(status services.PlaybackProxyStatus) []TaskRecentJob {
	jobs := make([]TaskRecentJob, 0, taskCenterRecentPerKind+1)
	if status.Running && status.CurrentVideoID != 0 {
		jobs = append(jobs, TaskRecentJob{
			Kind:    TaskRecentKindProxy,
			ID:      strconv.FormatUint(uint64(status.CurrentVideoID), 10),
			VideoID: status.CurrentVideoID,
			Title:   status.CurrentVideoName,
			Status:  TaskCenterStateRunning,
			Actions: []string{},
		})
	}
	var finishedAt *time.Time
	if !status.Running {
		finishedAt = status.UpdatedAt
	}
	for index := len(status.Results) - 1; index >= 0 && len(jobs) < taskCenterRecentPerKind+1; index-- {
		result := status.Results[index]
		job := TaskRecentJob{
			Kind:       TaskRecentKindProxy,
			ID:         strconv.FormatUint(uint64(result.VideoID), 10),
			VideoID:    result.VideoID,
			Title:      result.Name,
			Status:     result.Code,
			Message:    result.Message,
			FinishedAt: finishedAt,
			Actions:    []string{},
		}
		if playbackProxyResultFailed(result.Code) && result.Code != services.PlaybackProxyCodeFileMissing {
			job.Actions = append(job.Actions, "retry")
		}
		jobs = append(jobs, job)
	}
	return jobs
}

// playbackProxyResultFailed 与 PlaybackProxyService 的计数口径一致：created 算成功，
// already_exists / in_progress / cancelled 算跳过，其余都是失败。
func playbackProxyResultFailed(code string) bool {
	switch code {
	case services.PlaybackProxyCodeCreated, services.PlaybackProxyCodeAlreadyExists,
		services.PlaybackProxyCodeInProgress, services.PlaybackProxyCodeCancelled:
		return false
	}
	return true
}

// ===== task-center-changed 事件合并（D-PC18）=====
//
// 状态放在包级而不是 App 字段里（本批次不给 App 加字段），与 appQuitGuard 同理。

type taskCenterChangeNotifier struct {
	mu      sync.Mutex
	pending bool
}

var (
	taskCenterChanges     taskCenterChangeNotifier
	taskCenterChangeDelay = 500 * time.Millisecond
)

// notifyTaskCenterChanged 登记「任务中心的输入变了」，合并 taskCenterChangeDelay 内的多次变化后
// 发一次 task-center-changed（载荷为当时的快照）。接线项：startup 里 background-tasks、
// idle-scheduler-state 与各服务状态事件的回调都调用它。
func (a *App) notifyTaskCenterChanged() {
	if a.ctx == nil {
		return
	}
	taskCenterChanges.mu.Lock()
	if taskCenterChanges.pending {
		taskCenterChanges.mu.Unlock()
		return
	}
	taskCenterChanges.pending = true
	delay := taskCenterChangeDelay
	taskCenterChanges.mu.Unlock()
	time.AfterFunc(delay, func() {
		// 先清标记再取快照：取快照期间发生的变化会排下一次，不会被这一次吞掉。
		taskCenterChanges.mu.Lock()
		taskCenterChanges.pending = false
		taskCenterChanges.mu.Unlock()
		if a.ctx.Err() != nil {
			return
		}
		emitRuntimeEvent(a.ctx, taskCenterChangedEvent, a.GetTaskCenterSnapshot())
	})
}
