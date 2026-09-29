package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrSubtitleTaskNotFound reports that a subtitle queue task no longer exists.
var ErrSubtitleTaskNotFound = errors.New("subtitle task not found")

// SubtitleQueueTaskStatus is the lifecycle state exposed for a subtitle queue task.
// 同一组取值也是 subtitle_jobs.status 的取值（D-PC20）。
type SubtitleQueueTaskStatus string

const (
	// SubtitleQueueTaskStatusQueued means the task is waiting for the active task to finish.
	SubtitleQueueTaskStatusQueued SubtitleQueueTaskStatus = "queued"
	// SubtitleQueueTaskStatusRunning means the task is currently executing.
	SubtitleQueueTaskStatusRunning SubtitleQueueTaskStatus = "running"
	// SubtitleQueueTaskStatusSucceeded means the task finished successfully.
	SubtitleQueueTaskStatusSucceeded SubtitleQueueTaskStatus = "succeeded"
	// SubtitleQueueTaskStatusFailed means the task finished with an error.
	SubtitleQueueTaskStatusFailed SubtitleQueueTaskStatus = "failed"
	// SubtitleQueueTaskStatusCancelled means the task was cancelled before completion.
	SubtitleQueueTaskStatusCancelled SubtitleQueueTaskStatus = "cancelled"
	// SubtitleQueueTaskStatusNeedsConfirmation 表示结果留在待确认的临时文件里（疑似幻觉，或写回
	// 同名 .srt 失败），等用户「强制生成」或「放弃」。
	SubtitleQueueTaskStatusNeedsConfirmation SubtitleQueueTaskStatus = "needs_confirmation"
	// SubtitleQueueTaskStatusInterrupted 表示应用退出时任务还在排队或运行，启动时由
	// MarkInterruptedSubtitleJobs 标记。
	SubtitleQueueTaskStatusInterrupted SubtitleQueueTaskStatus = "interrupted"
)

// SubtitleQueueTask is a UI-safe snapshot of a queued or running subtitle generation task.
type SubtitleQueueTask struct {
	TaskID        uint                    `json:"task_id"`
	VideoID       uint                    `json:"video_id"`
	VideoName     string                  `json:"video_name"`
	Engine        SubtitleEngine          `json:"engine"`
	SourceLang    string                  `json:"source_lang"`
	Status        SubtitleQueueTaskStatus `json:"status"`
	Position      int                     `json:"position"`
	ForceGenerate bool                    `json:"force_generate"`
	CanCancel     bool                    `json:"can_cancel"`
	EnqueuedAt    time.Time               `json:"enqueued_at" ts_type:"string"`
	StartedAt     *time.Time              `json:"started_at,omitempty" ts_type:"string"`
	FinishedAt    *time.Time              `json:"finished_at,omitempty" ts_type:"string"`
}

// SubtitleQueueSnapshot is the current active task and FIFO backlog for subtitle generation.
type SubtitleQueueSnapshot struct {
	ActiveTask  *SubtitleQueueTask  `json:"active_task,omitempty"`
	QueuedTasks []SubtitleQueueTask `json:"queued_tasks"`
	Total       int                 `json:"total"`
}

type subtitleTaskExecutor func(ctx context.Context, task *subtitleQueueTask) (*SubtitleGenerateResult, error)

type subtitleQueueTask struct {
	TaskID    uint
	Request   SubtitleGenerateRequest
	VideoPath string
	VideoName string
	Options   SubtitleGenerateOptions
	// claim 非空时，入队把已有的 subtitle_jobs 行条件更新回 queued（重试、强制生成沿用同一行），
	// 不新插一行。
	claim *subtitleJobClaim

	status     SubtitleQueueTaskStatus
	enqueuedAt time.Time
	startedAt  *time.Time
	finishedAt *time.Time
	cancel     context.CancelFunc
	result     *SubtitleGenerateResult
	err        error
	done       chan struct{}
}

type subtitleJobClaim struct {
	jobID uint
	from  []SubtitleQueueTaskStatus
	// required 为真时抢不到就报 ErrSubtitleJobStateConflict（任务中心的按钮）；为假时新插一行
	// （生成对话框里的「强制生成」：那一行多半已被任务中心处理掉，但这仍是一次新的强制生成请求）。
	required bool
}

// subtitleJobStore 把队列的生命周期落到 subtitle_jobs（D-PC20）。队列单元测试不接它，只在内存里跑。
//
// 所有「行 → queued」与「queued → interrupted」的转换都在队列锁内完成，这样启动时的中断标记
// 看得到的活任务集合与库里的 queued/running 行是一致的；其余转换都是带原状态的条件更新（G-2）。
type subtitleJobStore interface {
	// recordQueued 为新任务插一行 queued（或按 claim 把已有行改回 queued），并把行 ID 写进 task.TaskID。
	recordQueued(task *subtitleQueueTask) error
	// recordRunning 把 queued 行条件更新为 running；行已不是 queued（例如视频被删、行随之级联删除）时返回 false。
	recordRunning(task *subtitleQueueTask) (bool, error)
	// recordCancelled 把还在排队就被取消的任务写成 cancelled。
	recordCancelled(task *subtitleQueueTask)
	// recordFinished 写执行结束后的终态。
	recordFinished(task *subtitleQueueTask, result *SubtitleGenerateResult, err error)
	// markInterrupted 把不属于 live 的 queued/running 行改成 interrupted，返回改动的行 ID。
	markInterrupted(live []uint) ([]uint, error)
}

type subtitleTaskQueue struct {
	mu            sync.Mutex
	cond          *sync.Cond
	nextTaskID    uint
	queued        []*subtitleQueueTask
	active        *subtitleQueueTask
	workerStarted bool
	emit          func(SubtitleQueueSnapshot)
	executor      subtitleTaskExecutor
	registry      *BackgroundTaskRegistry
	notifier      DesktopNotifier
	jobs          subtitleJobStore
}

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
// 定义在队列这一侧：字幕任务的运行区间就是队列 worker 的一次 execute，
// 与 SubtitleService 的其余部分无关。
func (s *SubtitleService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.subtitleTaskQueue().setBackgroundTaskRegistry(registry)
}

func (q *subtitleTaskQueue) setBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	q.mu.Lock()
	q.registry = registry
	q.mu.Unlock()
}

// SetDesktopNotifier 接入桌面通知（D-013）。与登记表同理放在队列这一侧：
// 字幕任务的终态就是队列 worker 拿到的那一份 result/err。字幕引擎准备的结果通知（D-PC22）也用它。
func (s *SubtitleService) SetDesktopNotifier(notifier DesktopNotifier) {
	s.subtitleTaskQueue().setDesktopNotifier(notifier)
}

func (q *subtitleTaskQueue) setDesktopNotifier(notifier DesktopNotifier) {
	q.mu.Lock()
	q.notifier = notifier
	q.mu.Unlock()
}

func (q *subtitleTaskQueue) desktopNotifier() DesktopNotifier {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.notifier
}

func newSubtitleTaskQueue(emit func(SubtitleQueueSnapshot), executor subtitleTaskExecutor) *subtitleTaskQueue {
	queue := &subtitleTaskQueue{
		emit:     emit,
		executor: executor,
	}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

// enqueue 把任务放进 FIFO 队尾后立即返回；任务中心的重试与强制生成走这里，不等结果。
func (q *subtitleTaskQueue) enqueue(task *subtitleQueueTask) error {
	if task == nil {
		return fmt.Errorf("subtitle task is nil")
	}
	task.done = make(chan struct{})
	task.status = SubtitleQueueTaskStatusQueued
	task.enqueuedAt = time.Now()

	q.mu.Lock()
	if q.jobs != nil {
		if err := q.jobs.recordQueued(task); err != nil {
			q.mu.Unlock()
			return err
		}
	} else if task.TaskID == 0 {
		q.nextTaskID++
		task.TaskID = q.nextTaskID
	} else if task.TaskID > q.nextTaskID {
		q.nextTaskID = task.TaskID
	}
	q.queued = append(q.queued, task)
	q.startWorkerLocked()
	snapshot := q.snapshotLocked()
	q.cond.Signal()
	q.mu.Unlock()
	q.emitSnapshot(snapshot)
	return nil
}

func (q *subtitleTaskQueue) submit(task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
	if err := q.enqueue(task); err != nil {
		return nil, err
	}
	<-task.done
	return task.result, task.err
}

func (q *subtitleTaskQueue) cancelTask(taskID uint) error {
	q.mu.Lock()
	if q.active != nil && q.active.TaskID == taskID {
		cancel := q.active.cancel
		q.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil
	}

	for idx, task := range q.queued {
		if task.TaskID != taskID {
			continue
		}
		q.queued = append(q.queued[:idx], q.queued[idx+1:]...)
		q.finishQueuedAsCancelledLocked(task, time.Now())
		snapshot := q.snapshotLocked()
		q.mu.Unlock()
		q.emitSnapshot(snapshot)
		return nil
	}

	q.mu.Unlock()
	return ErrSubtitleTaskNotFound
}

// finishQueuedAsCancelledLocked 结束一个还没开始的任务。库里的行也在锁内改掉，
// 否则启动时的中断标记可能在两步之间把它记成 interrupted。
func (q *subtitleTaskQueue) finishQueuedAsCancelledLocked(task *subtitleQueueTask, now time.Time) {
	task.status = SubtitleQueueTaskStatusCancelled
	task.finishedAt = &now
	task.result = &SubtitleGenerateResult{
		Status:  SubtitleResultStatusCancelled,
		VideoID: task.Request.VideoID,
		Message: "字幕任务已取消",
	}
	if q.jobs != nil {
		q.jobs.recordCancelled(task)
	}
	close(task.done)
}

func (q *subtitleTaskQueue) cancelActiveTask() error {
	q.mu.Lock()
	if q.active == nil {
		q.mu.Unlock()
		return ErrSubtitleTaskNotFound
	}
	taskID := q.active.TaskID
	q.mu.Unlock()
	return q.cancelTask(taskID)
}

func (q *subtitleTaskQueue) cancelAllAndWait() {
	q.mu.Lock()
	queued := q.queued
	q.queued = nil
	active := q.active
	var cancel context.CancelFunc
	if active != nil {
		cancel = active.cancel
	}
	now := time.Now()
	for _, task := range queued {
		q.finishQueuedAsCancelledLocked(task, now)
	}
	snapshot := q.snapshotLocked()
	q.mu.Unlock()
	q.emitSnapshot(snapshot)
	if cancel != nil {
		cancel()
	}
	if active != nil {
		<-active.done
	}
}

// markInterrupted 把上一次进程留下的 queued/running 行改成 interrupted（D-PC20）。持队列锁执行：
// 本进程已入队或正在跑的任务都在活任务集合里，不会被误标。
func (q *subtitleTaskQueue) markInterrupted() ([]uint, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.jobs == nil {
		return nil, nil
	}
	live := make([]uint, 0, len(q.queued)+1)
	if q.active != nil {
		live = append(live, q.active.TaskID)
	}
	for _, task := range q.queued {
		live = append(live, task.TaskID)
	}
	return q.jobs.markInterrupted(live)
}

func (q *subtitleTaskQueue) snapshot() SubtitleQueueSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.snapshotLocked()
}

func (q *subtitleTaskQueue) startWorkerLocked() {
	if q.workerStarted {
		return
	}
	q.workerStarted = true
	go q.worker()
}

func (q *subtitleTaskQueue) worker() {
	for {
		q.mu.Lock()
		for len(q.queued) == 0 {
			q.cond.Wait()
		}
		task := q.queued[0]
		q.queued = q.queued[1:]
		task.status = SubtitleQueueTaskStatusRunning
		now := time.Now()
		task.startedAt = &now
		ctx, cancel := context.WithCancel(context.Background())
		task.cancel = cancel
		q.active = task
		jobs := q.jobs
		snapshot := q.snapshotLocked()
		q.mu.Unlock()
		q.emitSnapshot(snapshot)

		var result *SubtitleGenerateResult
		var err error
		runnable := true
		if jobs != nil {
			runnable, err = jobs.recordRunning(task)
		}
		switch {
		case err != nil:
			err = fmt.Errorf("记录字幕任务状态失败: %w", err)
		case !runnable:
			// 行已经不在队列里（视频被删后级联删掉了记录）：不再为它跑识别。
			result = &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: task.Request.VideoID, Message: "字幕任务已失效"}
		default:
			result, err = q.execute(ctx, task)
		}
		cancel()
		// 终态先落库再发通知：通知里说「可在任务中心查看」，任务中心此刻就要查得到。
		// 两步都在 close(task.done) 之前：execute 的 defer 已经把登记表 End 掉（角标先减），
		// 等待 submit 的调用方也还没返回。
		if jobs != nil {
			jobs.recordFinished(task, result, err)
		}
		q.notifyTerminal(task, result, err)

		q.mu.Lock()
		finishedAt := time.Now()
		task.finishedAt = &finishedAt
		task.cancel = nil
		task.result = result
		task.err = err
		task.status = subtitleTerminalStatus(result, err)
		q.active = nil
		close(task.done)
		snapshot = q.snapshotLocked()
		q.mu.Unlock()
		q.emitSnapshot(snapshot)
	}
}

// notifyTerminal 在字幕任务走到终态时发一条系统通知（D-013）。
// 取消不发：那是用户自己按的，桌面上再弹一条只是噪音。失败原因不进文案（AI-CONTEXT 2.20），
// 原因与后续操作都在任务中心（D-PC20）。
func (q *subtitleTaskQueue) notifyTerminal(task *subtitleQueueTask, result *SubtitleGenerateResult, err error) {
	notifier := q.desktopNotifier()
	if notifier == nil {
		return
	}
	name := notificationMediaName(task.VideoName)
	var replaceErr *subtitleReplaceFailedError
	switch {
	case errors.As(err, &replaceErr):
		notifyDesktop(notifier, "字幕写入失败", fmt.Sprintf("《%s》字幕已生成但未能写入，可在任务中心重试收尾", name))
	case err != nil:
		notifyDesktop(notifier, "字幕生成失败", fmt.Sprintf("《%s》字幕生成失败，可在任务中心查看", name))
	case result != nil && result.Status == SubtitleResultStatusValidationFailed:
		notifyDesktop(notifier, "字幕需要确认", fmt.Sprintf("《%s》检测到疑似模型幻觉，可在任务中心确认是否强制生成", name))
	case result != nil && result.Status == SubtitleResultStatusSuccess:
		notifyDesktop(notifier, "字幕生成完成", fmt.Sprintf("《%s》字幕已生成", name))
	}
}

func (q *subtitleTaskQueue) execute(ctx context.Context, task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
	if q.executor == nil {
		return nil, fmt.Errorf("subtitle task executor is nil")
	}
	q.mu.Lock()
	registry := q.registry
	q.mu.Unlock()
	registry.Begin(BackgroundTaskSubtitle)
	defer registry.End(BackgroundTaskSubtitle)
	return q.executor(ctx, task)
}

func (q *subtitleTaskQueue) snapshotLocked() SubtitleQueueSnapshot {
	snapshot := SubtitleQueueSnapshot{
		QueuedTasks: make([]SubtitleQueueTask, 0, len(q.queued)),
	}
	if q.active != nil {
		active := subtitleQueueTaskSnapshot(q.active, 0)
		snapshot.ActiveTask = &active
		snapshot.Total++
	}
	for idx, task := range q.queued {
		snapshot.QueuedTasks = append(snapshot.QueuedTasks, subtitleQueueTaskSnapshot(task, idx+1))
		snapshot.Total++
	}
	return snapshot
}

func subtitleQueueTaskSnapshot(task *subtitleQueueTask, position int) SubtitleQueueTask {
	return SubtitleQueueTask{
		TaskID:        task.TaskID,
		VideoID:       task.Request.VideoID,
		VideoName:     task.VideoName,
		Engine:        task.Request.Engine,
		SourceLang:    task.Request.SourceLang,
		Status:        task.status,
		Position:      position,
		ForceGenerate: task.Options.ForceGenerate,
		CanCancel:     task.status == SubtitleQueueTaskStatusQueued || task.status == SubtitleQueueTaskStatusRunning,
		EnqueuedAt:    task.enqueuedAt,
		StartedAt:     task.startedAt,
		FinishedAt:    task.finishedAt,
	}
}

// subtitleTerminalStatus 把一次执行的结果映射成终态；内存快照与 subtitle_jobs 共用这一份口径。
func subtitleTerminalStatus(result *SubtitleGenerateResult, err error) SubtitleQueueTaskStatus {
	var replaceErr *subtitleReplaceFailedError
	switch {
	case result != nil && result.Status == SubtitleResultStatusCancelled:
		return SubtitleQueueTaskStatusCancelled
	case errors.As(err, &replaceErr):
		return SubtitleQueueTaskStatusNeedsConfirmation
	case err != nil, result == nil:
		return SubtitleQueueTaskStatusFailed
	case result.Status == SubtitleResultStatusValidationFailed:
		return SubtitleQueueTaskStatusNeedsConfirmation
	default:
		return SubtitleQueueTaskStatusSucceeded
	}
}

func (q *subtitleTaskQueue) emitSnapshot(snapshot SubtitleQueueSnapshot) {
	if q.emit != nil {
		q.emit(snapshot)
	}
}

// ===== subtitle_jobs 落库（D-PC20） =====
//
// 队列仍是单一 FIFO，转写槽不变；这里只把每个任务的状态持久化，让失败原因、待确认的临时字幕
// 与退出时没跑完的任务在任务中心查得到、处理得了（MEDIA-04、MEDIA-10）。

const (
	// subtitleJobHistoryKeep 是保留的终态行数；每次写入终态后裁剪。needs_confirmation 不参与裁剪：
	// 它引用着磁盘上的临时字幕，要等用户强制生成或放弃。interrupted 行在被重新排队或忽略之前
	// 同样不参与（见 subtitleJobPrunableStatuses）。
	subtitleJobHistoryKeep = 100
	// subtitleJobMessageMaxRunes 是落库与事件里失败原因的长度上限。
	subtitleJobMessageMaxRunes = 500
	subtitleFailedEvent        = "subtitle-failed"
	subtitleJobPruneBatch      = 500
	subtitleJobSourceLangMax   = 16
	subtitleJobEngineMax       = 32
)

// 任务中心可以对字幕任务执行的操作。cancel 只出现在 Actions 里，执行走既有的 CancelSubtitleTask。
type SubtitleJobAction string

const (
	SubtitleJobActionForce   SubtitleJobAction = "force"
	SubtitleJobActionDiscard SubtitleJobAction = "discard"
	SubtitleJobActionRetry   SubtitleJobAction = "retry"
	SubtitleJobActionCancel  SubtitleJobAction = "cancel"
)

// 任务中心操作的错误码（G-3），放在 SubtitleJobResolveResult.ErrorCode。
const (
	// SubtitleErrorJobConflict：任务状态已被别的操作改变（重复点击、另一个窗口已处理）。
	SubtitleErrorJobConflict = "subtitle_job_conflict"
	// SubtitleErrorPendingMissing：待确认的临时字幕已不在磁盘上，无法强制生成，只能重新生成。
	SubtitleErrorPendingMissing = "subtitle_pending_missing"
)

var (
	ErrSubtitleJobNotFound          = errors.New("字幕任务不存在")
	ErrSubtitleJobStateConflict     = errors.New("字幕任务状态已变化，请刷新后重试")
	ErrSubtitleJobActionUnsupported = errors.New("不支持的字幕任务操作")
)

var (
	subtitleJobActiveStatuses = []string{string(SubtitleQueueTaskStatusQueued), string(SubtitleQueueTaskStatusRunning)}
	// interrupted 不在其中：它与 needs_confirmation 一样在等用户处理（重新排队或忽略），
	// 裁剪掉就是「静默丢失」（MEDIA-10）。忽略会把它改成 cancelled，之后才参与裁剪。
	subtitleJobPrunableStatuses = []string{
		string(SubtitleQueueTaskStatusSucceeded), string(SubtitleQueueTaskStatusFailed),
		string(SubtitleQueueTaskStatusCancelled),
	}
	subtitleJobRetryableStatuses = []SubtitleQueueTaskStatus{
		SubtitleQueueTaskStatusFailed, SubtitleQueueTaskStatusCancelled, SubtitleQueueTaskStatusInterrupted,
	}
)

// SubtitleJobItem 是任务中心列出的一条字幕任务。不带临时文件路径：路径不出后端（G-3）。
type SubtitleJobItem struct {
	ID         uint                    `json:"id"`
	VideoID    uint                    `json:"video_id"`
	VideoName  string                  `json:"video_name"`
	Engine     SubtitleEngine          `json:"engine"`
	SourceLang string                  `json:"source_lang"`
	Status     SubtitleQueueTaskStatus `json:"status"`
	// Message 是失败原因或待确认说明，已擦掉路径。
	Message   string `json:"message"`
	ErrorCode string `json:"error_code,omitempty"`
	// PendingRetained 表示待确认的临时字幕还在磁盘上（force / discard 可用）。
	PendingRetained bool       `json:"pending_retained"`
	ForceGenerate   bool       `json:"force_generate"`
	CreatedAt       time.Time  `json:"created_at" ts_type:"string"`
	StartedAt       *time.Time `json:"started_at,omitempty" ts_type:"string"`
	FinishedAt      *time.Time `json:"finished_at,omitempty" ts_type:"string"`
	// Actions 取值 cancel（走 CancelSubtitleTask，任务 ID 即本行 ID）、force、discard、retry。
	Actions []string `json:"actions"`
}

// SubtitleJobResolveInput 是强制生成与重试需要的视频信息与当前设置（App 层读取）；放弃不需要。
type SubtitleJobResolveInput struct {
	VideoPath string
	VideoName string
	Options   SubtitleGenerateOptions
}

// SubtitleJobResolveResult 是 ResolveSubtitleJob 的结果。force / retry 只负责入队，立即返回 queued；
// 之后的进度与终态照常经 subtitle-progress、subtitle-queue 与任务列表反映。
type SubtitleJobResolveResult struct {
	JobID     uint                    `json:"job_id"`
	Action    SubtitleJobAction       `json:"action"`
	Status    SubtitleQueueTaskStatus `json:"status"`
	ErrorCode string                  `json:"error_code,omitempty"`
	Message   string                  `json:"message,omitempty"`
}

// SubtitleInterruptedJobs 是「上次中断 N 个字幕任务」提示所需的数据：本次启动时被标成 interrupted、
// 且至今仍是 interrupted 的任务。
type SubtitleInterruptedJobs struct {
	Count  int    `json:"count"`
	JobIDs []uint `json:"job_ids"`
}

// SubtitleFailedEvent 是 subtitle-failed 事件的载荷。Message 已擦掉路径；ErrorCode 只在写回失败
// （subtitle_replace_failed）时出现。
type SubtitleFailedEvent struct {
	JobID     uint   `json:"job_id"`
	VideoID   uint   `json:"video_id"`
	Message   string `json:"message"`
	ErrorCode string `json:"error_code,omitempty"`
}

// subtitleJobPayload 是 subtitle_jobs.options_json 的内容：只存展示与复现需要的非敏感字段。
// 翻译服务的 API Key 一律不落库，重试与强制生成时由 App 从当前设置重新读取。
type subtitleJobPayload struct {
	ForceGenerate       bool   `json:"force_generate,omitempty"`
	BilingualEnabled    bool   `json:"bilingual_enabled,omitempty"`
	BilingualLang       string `json:"bilingual_lang,omitempty"`
	TranslationProvider string `json:"translation_provider,omitempty"`
	WhisperXModel       string `json:"whisperx_model,omitempty"`
	WhisperXBatchSize   int    `json:"whisperx_batch_size,omitempty"`
	// 以下只在 needs_confirmation 时写入：重启之后「强制生成」复用临时文件所需的登记信息。
	ErrorCode          string `json:"error_code,omitempty"`
	ValidationCode     string `json:"validation_code,omitempty"`
	DetectedLang       string `json:"detected_lang,omitempty"`
	TranslationApplied bool   `json:"translation_applied,omitempty"`
}

func subtitleJobPayloadFromOptions(options SubtitleGenerateOptions) subtitleJobPayload {
	return subtitleJobPayload{
		ForceGenerate:       options.ForceGenerate,
		BilingualEnabled:    options.BilingualEnabled,
		BilingualLang:       options.BilingualLang,
		TranslationProvider: options.TranslationConfig.Provider,
		WhisperXModel:       options.RecognitionConfig.WhisperXModel,
		WhisperXBatchSize:   options.RecognitionConfig.WhisperXBatchSize,
	}
}

func encodeSubtitleJobPayload(payload subtitleJobPayload) string {
	data, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func decodeSubtitleJobPayload(raw string) subtitleJobPayload {
	var payload subtitleJobPayload
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &payload)
	}
	return payload
}

func subtitleStatusStrings(statuses []SubtitleQueueTaskStatus) []string {
	values := make([]string, 0, len(statuses))
	for _, status := range statuses {
		values = append(values, string(status))
	}
	return values
}

// scrubSubtitleJobMessage 擦掉失败原因里的路径并截断：已知的视频、字幕、临时文件路径整段替换
// （含空格的路径也擦得干净），剩下以 / 开头的片段再按通用规则擦一遍（G-3）。
func scrubSubtitleJobMessage(message string, videoPath string) string {
	message = strings.TrimSpace(message)
	if videoPath != "" {
		srtPath := subtitleparser.SRTPathForVideo(videoPath)
		message = scrubFacePaths(message, videoPath, filepath.Dir(videoPath), srtPath, subtitlePendingPath(srtPath))
	}
	message = scrubPlaybackProxyPaths(message)
	if runes := []rune(message); len(runes) > subtitleJobMessageMaxRunes {
		message = string(runes[:subtitleJobMessageMaxRunes])
	}
	return message
}

// subtitleReplaceFailedMessage 是写回失败时的统一说明。
func subtitleReplaceFailedMessage(err error, videoPath string) string {
	return fmt.Sprintf("字幕已生成但未能写入（%s），临时结果已保留，可重试收尾", scrubSubtitleJobMessage(err.Error(), videoPath))
}

// subtitleReplaceFailedResult 把收尾替换失败映射成带错误码的结果（D-PC13、D-PC20）：Status 仍是
// validation_failed 且 ForceEligible，现有对话框会给出「强制生成」，而强制生成正是复用临时文件
// 重试收尾；前端据 error_code 区分文案。不是替换失败时返回 nil。
func subtitleReplaceFailedResult(req SubtitleGenerateRequest, videoPath string, err error) *SubtitleGenerateResult {
	var replaceErr *subtitleReplaceFailedError
	if !errors.As(err, &replaceErr) {
		return nil
	}
	return &SubtitleGenerateResult{
		Status:          SubtitleResultStatusValidationFailed,
		VideoID:         req.VideoID,
		Message:         subtitleReplaceFailedMessage(err, videoPath),
		ForceEligible:   true,
		Engine:          req.Engine,
		SourceLang:      req.SourceLang,
		ErrorCode:       SubtitleErrorReplaceFailed,
		PendingRetained: true,
	}
}

type subtitleJobOutcome struct {
	status      SubtitleQueueTaskStatus
	message     string
	pendingPath string
	payload     subtitleJobPayload
}

// subtitleJobOutcome 把一次执行的结果翻成要落库的终态。待确认时临时文件路径与重启后复用它
// 所需的登记（识别出的语言、是否已合并译文）取自内存里的 pending 登记。
func (s *SubtitleService) subtitleJobOutcome(task *subtitleQueueTask, result *SubtitleGenerateResult, err error) subtitleJobOutcome {
	outcome := subtitleJobOutcome{status: subtitleTerminalStatus(result, err), payload: subtitleJobPayloadFromOptions(task.Options)}
	switch outcome.status {
	case SubtitleQueueTaskStatusCancelled:
		outcome.message = "字幕任务已取消"
		if result != nil && strings.TrimSpace(result.Message) != "" {
			outcome.message = result.Message
		}
	case SubtitleQueueTaskStatusFailed:
		message := "字幕任务没有返回结果"
		if err != nil {
			message = err.Error()
		}
		outcome.message = scrubSubtitleJobMessage(message, task.VideoPath)
	case SubtitleQueueTaskStatusNeedsConfirmation:
		outcome.pendingPath = subtitlePendingPath(subtitleparser.SRTPathForVideo(task.VideoPath))
		if artifact := s.peekPendingSubtitle(task.Request.VideoID); artifact != nil && artifact.VideoPath == task.VideoPath {
			outcome.pendingPath = artifact.SRTPath
			outcome.payload.DetectedLang = artifact.DetectedLang
			outcome.payload.TranslationApplied = artifact.TranslationApplied
		}
		var replaceErr *subtitleReplaceFailedError
		if errors.As(err, &replaceErr) {
			outcome.payload.ErrorCode = SubtitleErrorReplaceFailed
			outcome.payload.TranslationApplied = outcome.payload.TranslationApplied || replaceErr.translationApplied
			outcome.message = subtitleReplaceFailedMessage(err, task.VideoPath)
		} else if result != nil {
			outcome.payload.ValidationCode = string(result.ValidationCode)
			outcome.message = scrubSubtitleJobMessage(result.Message, task.VideoPath)
		}
	}
	return outcome
}

// subtitleJobDBStore 是 subtitleJobStore 的数据库实现，写 database.DB 当前指向的库。
type subtitleJobDBStore struct {
	service *SubtitleService
}

func subtitleJobsDB() (*gorm.DB, error) {
	if database.DB == nil {
		return nil, errors.New("数据库未初始化")
	}
	return database.DB, nil
}

func (st *subtitleJobDBStore) recordQueued(task *subtitleQueueTask) error {
	db, err := subtitleJobsDB()
	if err != nil {
		return err
	}
	// 列宽守卫：PG 上超长会报 22001，这里给出能读懂的原因。
	if len(task.Request.SourceLang) > subtitleJobSourceLangMax {
		return fmt.Errorf("不支持的识别语言: %s", task.Request.SourceLang)
	}
	if len(task.Request.Engine) > subtitleJobEngineMax {
		return fmt.Errorf("不支持的字幕引擎: %s", task.Request.Engine)
	}
	now := time.Now()
	payload := encodeSubtitleJobPayload(subtitleJobPayloadFromOptions(task.Options))
	if claim := task.claim; claim != nil {
		result := db.Model(&models.SubtitleJob{}).
			Where("id = ? AND status IN ?", claim.jobID, subtitleStatusStrings(claim.from)).
			Updates(map[string]any{
				"status":                string(SubtitleQueueTaskStatusQueued),
				"message":               "",
				"pending_artifact_path": "",
				"options_json":          payload,
				"started_at":            nil,
				"finished_at":           nil,
				"updated_at":            now,
			})
		if result.Error != nil {
			return fmt.Errorf("记录字幕任务失败: %w", result.Error)
		}
		if result.RowsAffected == 1 {
			task.TaskID = claim.jobID
			return nil
		}
		if claim.required {
			return ErrSubtitleJobStateConflict
		}
	}
	job := models.SubtitleJob{
		VideoID:     task.Request.VideoID,
		Engine:      string(task.Request.Engine),
		SourceLang:  task.Request.SourceLang,
		OptionsJSON: payload,
		Status:      string(SubtitleQueueTaskStatusQueued),
	}
	if err := db.Omit(clause.Associations).Create(&job).Error; err != nil {
		return fmt.Errorf("记录字幕任务失败: %w", err)
	}
	task.TaskID = job.ID
	return nil
}

func (st *subtitleJobDBStore) recordRunning(task *subtitleQueueTask) (bool, error) {
	db, err := subtitleJobsDB()
	if err != nil {
		return false, err
	}
	now := time.Now()
	result := db.Model(&models.SubtitleJob{}).
		Where("id = ? AND status = ?", task.TaskID, string(SubtitleQueueTaskStatusQueued)).
		Updates(map[string]any{"status": string(SubtitleQueueTaskStatusRunning), "started_at": now, "updated_at": now})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	// 同一视频更早的待确认行：本任务会重写同一个临时文件，那些行的临时产物从此不再属于它们。
	superseded := db.Model(&models.SubtitleJob{}).
		Where("video_id = ? AND id <> ? AND status = ?", task.Request.VideoID, task.TaskID, string(SubtitleQueueTaskStatusNeedsConfirmation)).
		Updates(map[string]any{
			"status":                string(SubtitleQueueTaskStatusCancelled),
			"message":               "已被同一视频的新字幕任务取代",
			"pending_artifact_path": "",
			"finished_at":           now,
			"updated_at":            now,
		})
	if superseded.Error != nil {
		log.Printf("[Subtitle] supersede pending subtitle jobs video_id=%d err=%v", task.Request.VideoID, superseded.Error)
	} else if superseded.RowsAffected > 0 {
		pruneSubtitleJobHistory(db)
	}
	return true, nil
}

func (st *subtitleJobDBStore) recordCancelled(task *subtitleQueueTask) {
	db, err := subtitleJobsDB()
	if err != nil {
		log.Printf("[Subtitle] record cancelled subtitle job task_id=%d err=%v", task.TaskID, err)
		return
	}
	now := time.Now()
	if err := db.Model(&models.SubtitleJob{}).
		Where("id = ? AND status = ?", task.TaskID, string(SubtitleQueueTaskStatusQueued)).
		Updates(map[string]any{
			"status":      string(SubtitleQueueTaskStatusCancelled),
			"message":     "字幕任务已取消",
			"finished_at": now,
			"updated_at":  now,
		}).Error; err != nil {
		log.Printf("[Subtitle] record cancelled subtitle job task_id=%d err=%v", task.TaskID, err)
		return
	}
	pruneSubtitleJobHistory(db)
}

// recordFinished 写终态；失败（含写回失败）时再发 subtitle-failed。事件放在落库之后：
// 前端收到事件去查任务列表时，这一行已经是终态。
func (st *subtitleJobDBStore) recordFinished(task *subtitleQueueTask, result *SubtitleGenerateResult, err error) {
	outcome := st.service.subtitleJobOutcome(task, result, err)
	defer func() {
		if outcome.status == SubtitleQueueTaskStatusFailed || outcome.payload.ErrorCode == SubtitleErrorReplaceFailed {
			st.service.emitEvent(subtitleFailedEvent, SubtitleFailedEvent{
				JobID: task.TaskID, VideoID: task.Request.VideoID, Message: outcome.message, ErrorCode: outcome.payload.ErrorCode,
			})
		}
	}()
	db, dbErr := subtitleJobsDB()
	if dbErr != nil {
		log.Printf("[Subtitle] record finished subtitle job task_id=%d err=%v", task.TaskID, dbErr)
		return
	}
	now := time.Now()
	if updateErr := db.Model(&models.SubtitleJob{}).
		Where("id = ? AND status IN ?", task.TaskID, subtitleJobActiveStatuses).
		Updates(map[string]any{
			"status":                string(outcome.status),
			"message":               outcome.message,
			"pending_artifact_path": outcome.pendingPath,
			"options_json":          encodeSubtitleJobPayload(outcome.payload),
			"finished_at":           now,
			"updated_at":            now,
		}).Error; updateErr != nil {
		log.Printf("[Subtitle] record finished subtitle job task_id=%d status=%s err=%v", task.TaskID, outcome.status, updateErr)
		return
	}
	pruneSubtitleJobHistory(db)
}

func (st *subtitleJobDBStore) markInterrupted(live []uint) ([]uint, error) {
	db, err := subtitleJobsDB()
	if err != nil {
		return nil, err
	}
	query := db.Model(&models.SubtitleJob{}).Where("status IN ?", subtitleJobActiveStatuses)
	if len(live) > 0 {
		// 空集合不能写成 NOT IN ()：GORM 会渲染成 NOT IN (NULL)，一行都匹配不上。
		query = query.Where("id NOT IN ?", live)
	}
	var ids []uint
	if err := query.Order("id ASC").Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	now := time.Now()
	if err := db.Model(&models.SubtitleJob{}).
		Where("id IN ? AND status IN ?", ids, subtitleJobActiveStatuses).
		Updates(map[string]any{
			"status":      string(SubtitleQueueTaskStatusInterrupted),
			"message":     "应用退出时任务尚未完成",
			"finished_at": now,
			"updated_at":  now,
		}).Error; err != nil {
		return nil, err
	}
	recordInterruptedPendingArtifacts(db, ids)
	return ids, nil
}

// recordInterruptedPendingArtifacts 把中断任务留在磁盘上的隐藏临时字幕记进行里（MEDIA-10）：应用退出时
// 正在跑的任务可能已经写出了 `.<基本名>.cineinsight-pending.srt`，重启后没有任何记录指向它。按视频路径
// 算出临时文件的位置，只有那里是普通文件（不跟随符号链接）才写进 pending_artifact_path；「忽略」时据此
// 清掉（见 DismissInterruptedSubtitleJobs），重新排队会覆盖同一个文件。记录失败只记日志，不影响标记本身。
func recordInterruptedPendingArtifacts(db *gorm.DB, ids []uint) {
	var jobs []models.SubtitleJob
	if err := db.Select("id", "video_id").
		Where("id IN ? AND status = ? AND pending_artifact_path = ?", ids, string(SubtitleQueueTaskStatusInterrupted), "").
		Find(&jobs).Error; err != nil {
		log.Printf("[Subtitle] list interrupted subtitle jobs for pending artifacts err=%v", err)
		return
	}
	videoIDs := make([]uint, 0, len(jobs))
	for _, job := range jobs {
		videoIDs = append(videoIDs, job.VideoID)
	}
	if len(videoIDs) == 0 {
		return
	}
	// 进了回收站的视频也照样认：路径还在记录里，临时文件留在原目录。
	var videos []models.Video
	if err := db.Unscoped().Model(&models.Video{}).Select("id", "path").Where("id IN ?", videoIDs).Find(&videos).Error; err != nil {
		log.Printf("[Subtitle] load video paths for interrupted subtitle jobs err=%v", err)
		return
	}
	paths := make(map[uint]string, len(videos))
	for _, video := range videos {
		paths[video.ID] = video.Path
	}
	for _, job := range jobs {
		videoPath := strings.TrimSpace(paths[job.VideoID])
		if videoPath == "" {
			continue
		}
		pendingPath := subtitlePendingPath(subtitleparser.SRTPathForVideo(videoPath))
		if info, err := os.Lstat(pendingPath); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := db.Model(&models.SubtitleJob{}).
			Where("id = ? AND status = ? AND pending_artifact_path = ?", job.ID, string(SubtitleQueueTaskStatusInterrupted), "").
			Update("pending_artifact_path", pendingPath).Error; err != nil {
			log.Printf("[Subtitle] record pending artifact of interrupted subtitle job id=%d err=%v", job.ID, err)
		}
	}
}

// pruneSubtitleJobHistory 只保留最近 subtitleJobHistoryKeep 条终态行（按最后更新时间）。
// 删除带上终态条件：两步之间被重试改回 queued 的行不会被误删。中断与待确认的行不在可裁剪之列
// （subtitleJobPrunableStatuses，I-3），这里无需另外保护。
func pruneSubtitleJobHistory(db *gorm.DB) {
	var ids []uint
	if err := db.Model(&models.SubtitleJob{}).
		Where("status IN ?", subtitleJobPrunableStatuses).
		Order("updated_at DESC").Order("id DESC").
		Pluck("id", &ids).Error; err != nil {
		log.Printf("[Subtitle] list subtitle job history err=%v", err)
		return
	}
	if len(ids) <= subtitleJobHistoryKeep {
		return
	}
	stale := ids[subtitleJobHistoryKeep:]
	for start := 0; start < len(stale); start += subtitleJobPruneBatch {
		end := min(start+subtitleJobPruneBatch, len(stale))
		if err := db.Where("id IN ? AND status IN ?", stale[start:end], subtitleJobPrunableStatuses).
			Delete(&models.SubtitleJob{}).Error; err != nil {
			log.Printf("[Subtitle] prune subtitle job history err=%v", err)
			return
		}
	}
}

// ===== 任务中心接口（D-PC20） =====

// ListSubtitleJobs 按最后更新时间从新到旧列出字幕任务（含排队、运行中与历史）。limit ≤ 0 取 100，最多 200。
func (s *SubtitleService) ListSubtitleJobs(limit int) ([]SubtitleJobItem, error) {
	db, err := subtitleJobsDB()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = subtitleJobHistoryKeep
	}
	if limit > 2*subtitleJobHistoryKeep {
		limit = 2 * subtitleJobHistoryKeep
	}
	var jobs []models.SubtitleJob
	if err := db.Order("updated_at DESC").Order("id DESC").Limit(limit).Find(&jobs).Error; err != nil {
		return nil, err
	}
	names, err := subtitleJobVideoNames(db, jobs)
	if err != nil {
		return nil, err
	}
	items := make([]SubtitleJobItem, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, subtitleJobItemFrom(job, names[job.VideoID]))
	}
	return items, nil
}

// GetSubtitleJob 返回一条字幕任务；不存在时返回 ErrSubtitleJobNotFound。
func (s *SubtitleService) GetSubtitleJob(jobID uint) (*SubtitleJobItem, error) {
	job, err := loadSubtitleJob(jobID)
	if err != nil {
		return nil, err
	}
	db, err := subtitleJobsDB()
	if err != nil {
		return nil, err
	}
	names, err := subtitleJobVideoNames(db, []models.SubtitleJob{*job})
	if err != nil {
		return nil, err
	}
	item := subtitleJobItemFrom(*job, names[job.VideoID])
	return &item, nil
}

func loadSubtitleJob(jobID uint) (*models.SubtitleJob, error) {
	db, err := subtitleJobsDB()
	if err != nil {
		return nil, err
	}
	var job models.SubtitleJob
	if err := db.First(&job, jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSubtitleJobNotFound
		}
		return nil, err
	}
	return &job, nil
}

// subtitleJobVideoNames 取任务对应视频的显示名；视频已进回收站也照样显示。
func subtitleJobVideoNames(db *gorm.DB, jobs []models.SubtitleJob) (map[uint]string, error) {
	names := make(map[uint]string, len(jobs))
	ids := make([]uint, 0, len(jobs))
	for _, job := range jobs {
		if _, seen := names[job.VideoID]; !seen {
			names[job.VideoID] = ""
			ids = append(ids, job.VideoID)
		}
	}
	if len(ids) == 0 {
		return names, nil
	}
	var videos []models.Video
	if err := db.Unscoped().Model(&models.Video{}).Select("id", "name").Where("id IN ?", ids).Find(&videos).Error; err != nil {
		return nil, err
	}
	for _, video := range videos {
		names[video.ID] = video.Name
	}
	return names, nil
}

func subtitleJobItemFrom(job models.SubtitleJob, videoName string) SubtitleJobItem {
	payload := decodeSubtitleJobPayload(job.OptionsJSON)
	status := SubtitleQueueTaskStatus(job.Status)
	item := SubtitleJobItem{
		ID:              job.ID,
		VideoID:         job.VideoID,
		VideoName:       videoName,
		Engine:          SubtitleEngine(job.Engine),
		SourceLang:      job.SourceLang,
		Status:          status,
		Message:         job.Message,
		PendingRetained: status == SubtitleQueueTaskStatusNeedsConfirmation && strings.TrimSpace(job.PendingArtifactPath) != "",
		ForceGenerate:   payload.ForceGenerate,
		CreatedAt:       job.CreatedAt,
		StartedAt:       job.StartedAt,
		FinishedAt:      job.FinishedAt,
		Actions:         subtitleJobActions(status),
	}
	if status == SubtitleQueueTaskStatusNeedsConfirmation {
		item.ErrorCode = payload.ErrorCode
	}
	return item
}

func subtitleJobActions(status SubtitleQueueTaskStatus) []string {
	switch status {
	case SubtitleQueueTaskStatusQueued, SubtitleQueueTaskStatusRunning:
		return []string{string(SubtitleJobActionCancel)}
	case SubtitleQueueTaskStatusNeedsConfirmation:
		return []string{string(SubtitleJobActionForce), string(SubtitleJobActionDiscard)}
	case SubtitleQueueTaskStatusFailed, SubtitleQueueTaskStatusCancelled, SubtitleQueueTaskStatusInterrupted:
		return []string{string(SubtitleJobActionRetry)}
	default:
		return []string{}
	}
}

// ResolveSubtitleJob 处理任务中心里的一条字幕任务（D-PC20）：
//   - force：待确认的任务复用临时字幕收尾（与「强制生成」同一条路径，不重跑识别）；
//   - discard：删除待确认的临时字幕，任务置为 cancelled；
//   - retry：失败、取消、中断的任务按当前设置重新排队。
//
// 状态转换都是带原状态的条件更新：重复点击或并发处理时，输的一方拿到 subtitle_job_conflict。
func (s *SubtitleService) ResolveSubtitleJob(jobID uint, action SubtitleJobAction, input SubtitleJobResolveInput) (*SubtitleJobResolveResult, error) {
	switch action {
	case SubtitleJobActionForce, SubtitleJobActionDiscard, SubtitleJobActionRetry:
	default:
		return nil, ErrSubtitleJobActionUnsupported
	}
	job, err := loadSubtitleJob(jobID)
	if err != nil {
		return nil, err
	}
	switch action {
	case SubtitleJobActionDiscard:
		return s.discardSubtitleJob(job)
	case SubtitleJobActionForce:
		return s.forceSubtitleJob(job, input)
	default:
		return s.retrySubtitleJob(job, input)
	}
}

func (s *SubtitleService) subtitleJobConflict(jobID uint, action SubtitleJobAction) *SubtitleJobResolveResult {
	result := &SubtitleJobResolveResult{JobID: jobID, Action: action, ErrorCode: SubtitleErrorJobConflict, Message: ErrSubtitleJobStateConflict.Error()}
	if job, err := loadSubtitleJob(jobID); err == nil {
		result.Status = SubtitleQueueTaskStatus(job.Status)
	}
	return result
}

func (s *SubtitleService) forceSubtitleJob(job *models.SubtitleJob, input SubtitleJobResolveInput) (*SubtitleJobResolveResult, error) {
	if SubtitleQueueTaskStatus(job.Status) != SubtitleQueueTaskStatusNeedsConfirmation {
		return s.subtitleJobConflict(job.ID, SubtitleJobActionForce), nil
	}
	if strings.TrimSpace(input.VideoPath) == "" {
		return nil, errors.New("字幕任务缺少视频路径")
	}
	pendingPath := subtitlePendingPath(subtitleparser.SRTPathForVideo(input.VideoPath))
	if !subtitlePendingUsable(job.PendingArtifactPath, pendingPath) {
		// 临时字幕已经不在（被外部删除、视频改名或搬走）：强制生成无从谈起，改记失败，任务中心给出「重试」。
		const message = "临时字幕已不存在，请重新生成"
		now := time.Now()
		db, err := subtitleJobsDB()
		if err != nil {
			return nil, err
		}
		result := db.Model(&models.SubtitleJob{}).
			Where("id = ? AND status = ?", job.ID, string(SubtitleQueueTaskStatusNeedsConfirmation)).
			Updates(map[string]any{
				"status": string(SubtitleQueueTaskStatusFailed), "message": message, "pending_artifact_path": "",
				"finished_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			return s.subtitleJobConflict(job.ID, SubtitleJobActionForce), nil
		}
		pruneSubtitleJobHistory(db)
		return &SubtitleJobResolveResult{
			JobID: job.ID, Action: SubtitleJobActionForce, Status: SubtitleQueueTaskStatusFailed,
			ErrorCode: SubtitleErrorPendingMissing, Message: message,
		}, nil
	}
	s.restorePendingFromJob(job, input.VideoPath, pendingPath)
	options := normalizeSubtitleGenerateOptions(input.Options)
	options.ForceGenerate = true
	task := &subtitleQueueTask{
		Request:   SubtitleGenerateRequest{VideoID: job.VideoID, VideoName: strings.TrimSpace(input.VideoName), Engine: SubtitleEngine(job.Engine), SourceLang: job.SourceLang},
		VideoPath: input.VideoPath,
		VideoName: strings.TrimSpace(input.VideoName),
		Options:   options,
		claim:     &subtitleJobClaim{jobID: job.ID, from: []SubtitleQueueTaskStatus{SubtitleQueueTaskStatusNeedsConfirmation}, required: true},
	}
	return s.enqueueResolvedSubtitleJob(task, SubtitleJobActionForce)
}

func (s *SubtitleService) retrySubtitleJob(job *models.SubtitleJob, input SubtitleJobResolveInput) (*SubtitleJobResolveResult, error) {
	retryable := false
	for _, status := range subtitleJobRetryableStatuses {
		if SubtitleQueueTaskStatus(job.Status) == status {
			retryable = true
		}
	}
	if !retryable {
		return s.subtitleJobConflict(job.ID, SubtitleJobActionRetry), nil
	}
	if strings.TrimSpace(input.VideoPath) == "" {
		return nil, errors.New("字幕任务缺少视频路径")
	}
	options := normalizeSubtitleGenerateOptions(input.Options)
	options.ForceGenerate = false
	task := &subtitleQueueTask{
		Request:   SubtitleGenerateRequest{VideoID: job.VideoID, VideoName: strings.TrimSpace(input.VideoName), Engine: SubtitleEngine(job.Engine), SourceLang: job.SourceLang},
		VideoPath: input.VideoPath,
		VideoName: strings.TrimSpace(input.VideoName),
		Options:   options,
		claim:     &subtitleJobClaim{jobID: job.ID, from: subtitleJobRetryableStatuses, required: true},
	}
	return s.enqueueResolvedSubtitleJob(task, SubtitleJobActionRetry)
}

func (s *SubtitleService) enqueueResolvedSubtitleJob(task *subtitleQueueTask, action SubtitleJobAction) (*SubtitleJobResolveResult, error) {
	jobID := task.claim.jobID
	if err := s.subtitleTaskQueue().enqueue(task); err != nil {
		if errors.Is(err, ErrSubtitleJobStateConflict) {
			return s.subtitleJobConflict(jobID, action), nil
		}
		return nil, err
	}
	return &SubtitleJobResolveResult{JobID: task.TaskID, Action: action, Status: SubtitleQueueTaskStatusQueued}, nil
}

// discardSubtitleJob 放弃待确认的任务：先以条件更新赢下这一行，再删临时字幕（持字幕文件锁，
// 与强制生成的收尾互斥）。只删符合临时文件命名的路径，库里的值再奇怪也不会删到别的文件。
//
// 两种情况不删文件（M-7）：
//   - 另有待确认的行引用同一个临时文件（同目录同名、扩展名不同的视频共用同名 .srt，临时文件
//     也是同一个）：删了它，那一行的「强制生成」就无从谈起；
//   - 删除失败：这一行按条件更新改回 needs_confirmation 并报错，不留下一个没人认领的临时文件。
func (s *SubtitleService) discardSubtitleJob(job *models.SubtitleJob) (*SubtitleJobResolveResult, error) {
	if SubtitleQueueTaskStatus(job.Status) != SubtitleQueueTaskStatusNeedsConfirmation {
		return s.subtitleJobConflict(job.ID, SubtitleJobActionDiscard), nil
	}
	db, err := subtitleJobsDB()
	if err != nil {
		return nil, err
	}
	pendingPath := strings.TrimSpace(job.PendingArtifactPath)
	finalPath := ""
	if pendingPath != "" {
		if resolved, pathErr := subtitleFinalPathForPending(pendingPath); pathErr == nil {
			finalPath = resolved
			unlock := lockSubtitleFile(finalPath)
			defer unlock()
		}
	}
	now := time.Now()
	const discardedMessage = "已放弃待确认的字幕"
	result := db.Model(&models.SubtitleJob{}).
		Where("id = ? AND status = ?", job.ID, string(SubtitleQueueTaskStatusNeedsConfirmation)).
		Updates(map[string]any{
			"status": string(SubtitleQueueTaskStatusCancelled), "message": discardedMessage, "pending_artifact_path": "",
			"finished_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return s.subtitleJobConflict(job.ID, SubtitleJobActionDiscard), nil
	}
	if finalPath != "" {
		shared, sharedErr := subtitlePendingReferencedElsewhere(db, pendingPath, job.ID, 0)
		var removeErr error
		switch {
		case sharedErr != nil:
			removeErr = sharedErr
		case !shared:
			if err := os.Remove(pendingPath); err != nil && !os.IsNotExist(err) {
				removeErr = err
			}
		}
		if removeErr != nil {
			s.reopenDiscardedSubtitleJob(db, job, pendingPath, discardedMessage)
			if sharedErr != nil {
				return nil, fmt.Errorf("检查临时字幕的引用失败: %w", sharedErr)
			}
			return nil, fmt.Errorf("删除临时字幕失败: %s", subtitleIOReason(removeErr))
		}
		s.forgetPendingSubtitle(job.VideoID, pendingPath)
	}
	pruneSubtitleJobHistory(db)
	return &SubtitleJobResolveResult{JobID: job.ID, Action: SubtitleJobActionDiscard, Status: SubtitleQueueTaskStatusCancelled}, nil
}

// reopenDiscardedSubtitleJob 把刚被放弃、临时文件却没删掉的那一行改回 needs_confirmation。
// 条件更新只认「仍是这次放弃写下的样子」：两步之间已被重试改走的行不动。
func (s *SubtitleService) reopenDiscardedSubtitleJob(db *gorm.DB, job *models.SubtitleJob, pendingPath, discardedMessage string) {
	result := db.Model(&models.SubtitleJob{}).
		Where("id = ? AND status = ? AND message = ? AND pending_artifact_path = ?",
			job.ID, string(SubtitleQueueTaskStatusCancelled), discardedMessage, "").
		Updates(map[string]any{
			"status": string(SubtitleQueueTaskStatusNeedsConfirmation), "message": job.Message, "pending_artifact_path": pendingPath,
			"finished_at": job.FinishedAt, "updated_at": time.Now(),
		})
	if result.Error != nil || result.RowsAffected == 0 {
		log.Printf("[Subtitle] reopen discarded subtitle job id=%d rows=%d err=%v", job.ID, result.RowsAffected, result.Error)
	}
}

// subtitlePendingReferencedElsewhere 报告是否还有别的待确认行引用同一个临时文件。excludeJobID 与
// excludeVideoID 为 0 时不排除。
//
// 「同一个」按 subtitleFileLockKey（Clean → NFC → 小写）在 Go 里比较，与 lockSubtitleFile 同一口径（m6）：
// darwin 的文件系统不区分大小写、也不区分 Unicode 规范化形式，Movie.mp4 与 movie.mkv、NFC 与 NFD
// 写法的「Café」落到的是同一个临时文件。SQL 的 LOWER 做不到这一点（SQLite 只转 ASCII，两个后端都
// 不做规范化）。在区分大小写的文件系统上最多多留一个文件。
func subtitlePendingReferencedElsewhere(db *gorm.DB, pendingPath string, excludeJobID, excludeVideoID uint) (bool, error) {
	query := db.Model(&models.SubtitleJob{}).
		Where("status = ? AND pending_artifact_path <> ?", string(SubtitleQueueTaskStatusNeedsConfirmation), "")
	if excludeJobID != 0 {
		query = query.Where("id <> ?", excludeJobID)
	}
	if excludeVideoID != 0 {
		query = query.Where("video_id <> ?", excludeVideoID)
	}
	var candidates []string
	if err := query.Pluck("pending_artifact_path", &candidates).Error; err != nil {
		return false, err
	}
	key := subtitleFileLockKey(pendingPath)
	for _, candidate := range candidates {
		if subtitleFileLockKey(candidate) == key {
			return true, nil
		}
	}
	return false, nil
}

// subtitlePendingUsable 报告行里记的临时字幕是否就是当前视频路径对应的那个、且仍是磁盘上的普通文件。
func subtitlePendingUsable(recorded, expected string) bool {
	if strings.TrimSpace(recorded) == "" || filepath.Clean(recorded) != filepath.Clean(expected) {
		return false
	}
	info, err := os.Lstat(expected)
	return err == nil && info.Mode().IsRegular()
}

// restorePendingFromJob 让「强制生成」在重启之后也能复用临时字幕：内存登记丢了（或指向别处）时，
// 按行里记的引擎、语言与登记信息补回去（MEDIA-10）。
func (s *SubtitleService) restorePendingFromJob(job *models.SubtitleJob, videoPath, pendingPath string) {
	if artifact := s.peekPendingSubtitle(job.VideoID); artifact != nil && artifact.SRTPath == pendingPath && artifact.VideoPath == videoPath {
		return
	}
	payload := decodeSubtitleJobPayload(job.OptionsJSON)
	s.cachePendingSubtitle(&pendingSubtitleArtifact{
		VideoID:            job.VideoID,
		VideoPath:          videoPath,
		SRTPath:            pendingPath,
		Engine:             SubtitleEngine(job.Engine),
		SourceLang:         job.SourceLang,
		DetectedLang:       payload.DetectedLang,
		TranslationApplied: payload.TranslationApplied,
	})
}

// forgetPendingSubtitle 清掉指向 pendingPath 的内存登记（放弃之后）。
func (s *SubtitleService) forgetPendingSubtitle(videoID uint, pendingPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if artifact := s.pending[videoID]; artifact != nil && artifact.SRTPath == pendingPath {
		delete(s.pending, videoID)
	}
}

// needsConfirmationClaim 让生成对话框里的「强制生成」接上任务中心里同一视频的待确认行，
// 两个入口处理的是同一件事，不该在任务中心留下一条永远待确认的旧行。
func (s *SubtitleService) needsConfirmationClaim(req SubtitleGenerateRequest, videoPath string) *subtitleJobClaim {
	db, err := subtitleJobsDB()
	if err != nil {
		return nil
	}
	var job models.SubtitleJob
	if err := db.Where("video_id = ? AND engine = ? AND status = ?", req.VideoID, string(req.Engine), string(SubtitleQueueTaskStatusNeedsConfirmation)).
		Order("id DESC").Limit(1).Find(&job).Error; err != nil || job.ID == 0 {
		return nil
	}
	pendingPath := subtitlePendingPath(subtitleparser.SRTPathForVideo(videoPath))
	if subtitlePendingUsable(job.PendingArtifactPath, pendingPath) {
		s.restorePendingFromJob(&job, videoPath, pendingPath)
	}
	return &subtitleJobClaim{jobID: job.ID, from: []SubtitleQueueTaskStatus{SubtitleQueueTaskStatusNeedsConfirmation}}
}

// discardNeedsConfirmationJobs 放弃某个视频的全部待确认行（生成对话框里选了「不强制生成」）。
func (s *SubtitleService) discardNeedsConfirmationJobs(videoID uint) error {
	db, err := subtitleJobsDB()
	if err != nil {
		return err
	}
	var jobs []models.SubtitleJob
	if err := db.Where("video_id = ? AND status = ?", videoID, string(SubtitleQueueTaskStatusNeedsConfirmation)).
		Order("id ASC").Find(&jobs).Error; err != nil {
		return err
	}
	for index := range jobs {
		if _, err := s.discardSubtitleJob(&jobs[index]); err != nil {
			return err
		}
	}
	return nil
}

// MarkInterruptedSubtitleJobs 在启动时调用（接线项）：把上一次进程退出时还在排队或运行的任务
// 标成 interrupted，返回条数；前端据 GetInterruptedSubtitleJobs 提示「上次中断 N 个字幕任务」。
func (s *SubtitleService) MarkInterruptedSubtitleJobs() (int, error) {
	ids, err := s.subtitleTaskQueue().markInterrupted()
	return len(ids), err
}

// GetInterruptedSubtitleJobs 返回全部仍是 interrupted 的任务：不只本次启动标记的，上一次启动
// 留下、用户既没重新排队也没忽略的也在内——「中断」状态本身就是「还没处理」的持久标记（I-3）。
// 已被单独重试的行状态已变，不再计入。
func (s *SubtitleService) GetInterruptedSubtitleJobs() (SubtitleInterruptedJobs, error) {
	summary := SubtitleInterruptedJobs{JobIDs: []uint{}}
	db, err := subtitleJobsDB()
	if err != nil {
		return summary, err
	}
	var still []uint
	if err := db.Model(&models.SubtitleJob{}).
		Where("status = ?", string(SubtitleQueueTaskStatusInterrupted)).
		Order("id ASC").Pluck("id", &still).Error; err != nil {
		return summary, err
	}
	summary.JobIDs = append(summary.JobIDs, still...)
	summary.Count = len(still)
	return summary, nil
}

// subtitleJobDismissedInterruptedMessage 是「忽略」后写入的说明：行改为 cancelled，留在历史里，仍可逐条重试。
const subtitleJobDismissedInterruptedMessage = "应用退出时任务尚未完成（已忽略）"

// DismissInterruptedSubtitleJobs 是提示上的「忽略」：全部 interrupted 行改为 cancelled，不再提示；
// 任务留在历史里，仍可逐条重试，并与其他终态行一样参与历史裁剪（I-3）。「已忽略」的文案只写在
// 这里——用户真的点了「忽略」的行；「全部重新排队」没能入队的行走 FailInterruptedSubtitleJob（m7）。
//
// 中断时留下的临时字幕（markInterrupted 记在 pending_artifact_path）在没有别的行引用时删掉（MEDIA-10）。
func (s *SubtitleService) DismissInterruptedSubtitleJobs() error {
	db, err := subtitleJobsDB()
	if err != nil {
		return err
	}
	var leftovers []string
	if err := db.Model(&models.SubtitleJob{}).
		Where("status = ? AND pending_artifact_path <> ?", string(SubtitleQueueTaskStatusInterrupted), "").
		Pluck("pending_artifact_path", &leftovers).Error; err != nil {
		return err
	}
	now := time.Now()
	result := db.Model(&models.SubtitleJob{}).
		Where("status = ?", string(SubtitleQueueTaskStatusInterrupted)).
		Updates(map[string]any{
			"status":                string(SubtitleQueueTaskStatusCancelled),
			"message":               subtitleJobDismissedInterruptedMessage,
			"pending_artifact_path": "",
			"updated_at":            now,
		})
	if result.Error != nil {
		return result.Error
	}
	s.removeUnreferencedPendingArtifacts(db, leftovers)
	if result.RowsAffected > 0 {
		pruneSubtitleJobHistory(db)
	}
	return nil
}

// FailInterruptedSubtitleJob 把「全部重新排队」时没能入队的一条中断任务改为 failed，message 写真实原因
// （例如「视频已删除，无法重新排队」），不再随后续的「忽略」被标成「已忽略」（m7）。failed 与其他
// 「这一步没能做成、原因见说明」的行同一语义，任务中心照常给出「重试」。
//
// 条件更新只认仍是 interrupted 的行：两步之间已被单独处理的行不动，返回 false。中断时留下的临时字幕
// 与「忽略」同样处理：没有别的行引用时删掉。
func (s *SubtitleService) FailInterruptedSubtitleJob(jobID uint, reason string) (bool, error) {
	db, err := subtitleJobsDB()
	if err != nil {
		return false, err
	}
	var job models.SubtitleJob
	if err := db.Select("id", "pending_artifact_path").Where("id = ? AND status = ?", jobID, string(SubtitleQueueTaskStatusInterrupted)).
		Limit(1).Find(&job).Error; err != nil {
		return false, err
	}
	if job.ID == 0 {
		return false, nil
	}
	message := scrubSubtitleJobMessage(reason, "")
	if message == "" {
		message = "重新排队失败"
	}
	result := db.Model(&models.SubtitleJob{}).
		Where("id = ? AND status = ?", jobID, string(SubtitleQueueTaskStatusInterrupted)).
		Updates(map[string]any{
			"status":                string(SubtitleQueueTaskStatusFailed),
			"message":               message,
			"pending_artifact_path": "",
			"updated_at":            time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	if strings.TrimSpace(job.PendingArtifactPath) != "" {
		s.removeUnreferencedPendingArtifacts(db, []string{job.PendingArtifactPath})
	}
	pruneSubtitleJobHistory(db)
	return true, nil
}

// removeUnreferencedPendingArtifacts 删掉不再有人认领的临时字幕（中断任务被忽略或重新排队失败之后）。
// 同一个文件（按 subtitleFileLockKey）只处理一次；单个文件删不掉只记日志——行已经结束，临时文件
// 留下也不会被误用。
func (s *SubtitleService) removeUnreferencedPendingArtifacts(db *gorm.DB, pendingPaths []string) {
	seen := make(map[string]struct{}, len(pendingPaths))
	for _, pendingPath := range pendingPaths {
		key := subtitleFileLockKey(pendingPath)
		if _, done := seen[key]; done {
			continue
		}
		seen[key] = struct{}{}
		if err := s.removeUnreferencedPendingArtifact(db, pendingPath); err != nil {
			log.Printf("[Subtitle] remove leftover pending subtitle err=%s", scrubSubtitleJobMessage(err.Error(), ""))
		}
	}
}

// removeUnreferencedPendingArtifact 只删符合临时文件命名、且是普通文件的路径——库里记的值再奇怪也不会
// 删到同名 .srt 或别的文件。持字幕文件锁（与生成、强制生成、放弃互斥），在锁内确认：没有待确认行
// 引用它（m6 同一口径），本进程也没有登记它（刚跑完、结果还没落库的任务）。
func (s *SubtitleService) removeUnreferencedPendingArtifact(db *gorm.DB, pendingPath string) error {
	finalPath, err := subtitleFinalPathForPending(pendingPath)
	if err != nil {
		return nil
	}
	unlock := lockSubtitleFile(finalPath)
	defer unlock()
	shared, err := subtitlePendingReferencedElsewhere(db, pendingPath, 0, 0)
	if err != nil {
		return fmt.Errorf("检查临时字幕的引用失败: %w", err)
	}
	if shared || s.pendingSubtitleRegistered(pendingPath) {
		return nil
	}
	info, err := os.Lstat(pendingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取临时字幕失败: %s", subtitleIOReason(err))
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if err := os.Remove(pendingPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除临时字幕失败: %s", subtitleIOReason(err))
	}
	return nil
}

// pendingSubtitleRegistered 报告本进程的内存登记里是否有指向同一个临时文件的待确认产物。
func (s *SubtitleService) pendingSubtitleRegistered(pendingPath string) bool {
	key := subtitleFileLockKey(pendingPath)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, artifact := range s.pending {
		if artifact != nil && subtitleFileLockKey(artifact.SRTPath) == key {
			return true
		}
	}
	return false
}
