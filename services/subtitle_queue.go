package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrSubtitleTaskNotFound reports that a subtitle queue task no longer exists.
var ErrSubtitleTaskNotFound = errors.New("subtitle task not found")

// SubtitleQueueTaskStatus is the lifecycle state exposed for a subtitle queue task.
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

	status     SubtitleQueueTaskStatus
	enqueuedAt time.Time
	startedAt  *time.Time
	finishedAt *time.Time
	cancel     context.CancelFunc
	result     *SubtitleGenerateResult
	err        error
	done       chan struct{}
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
// 字幕任务的终态就是队列 worker 拿到的那一份 result/err。
func (s *SubtitleService) SetDesktopNotifier(notifier DesktopNotifier) {
	s.subtitleTaskQueue().setDesktopNotifier(notifier)
}

func (q *subtitleTaskQueue) setDesktopNotifier(notifier DesktopNotifier) {
	q.mu.Lock()
	q.notifier = notifier
	q.mu.Unlock()
}

func newSubtitleTaskQueue(emit func(SubtitleQueueSnapshot), executor subtitleTaskExecutor) *subtitleTaskQueue {
	queue := &subtitleTaskQueue{
		emit:     emit,
		executor: executor,
	}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

func (q *subtitleTaskQueue) submit(task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
	if task == nil {
		return nil, fmt.Errorf("subtitle task is nil")
	}
	task.done = make(chan struct{})
	task.status = SubtitleQueueTaskStatusQueued
	task.enqueuedAt = time.Now()

	q.mu.Lock()
	if task.TaskID == 0 {
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
		task.status = SubtitleQueueTaskStatusCancelled
		now := time.Now()
		task.finishedAt = &now
		task.result = &SubtitleGenerateResult{
			Status:  SubtitleResultStatusCancelled,
			VideoID: task.Request.VideoID,
			Message: "字幕任务已取消",
		}
		close(task.done)
		snapshot := q.snapshotLocked()
		q.mu.Unlock()
		q.emitSnapshot(snapshot)
		return nil
	}

	q.mu.Unlock()
	return ErrSubtitleTaskNotFound
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
		task.status = SubtitleQueueTaskStatusCancelled
		task.finishedAt = &now
		task.result = &SubtitleGenerateResult{
			Status:  SubtitleResultStatusCancelled,
			VideoID: task.Request.VideoID,
			Message: "字幕任务已取消",
		}
		close(task.done)
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
		snapshot := q.snapshotLocked()
		q.mu.Unlock()
		q.emitSnapshot(snapshot)

		result, err := q.execute(ctx, task)
		cancel()
		// 通知放在 close(task.done) 之前：execute 的 defer 已经把登记表 End 掉
		// （角标先减），此刻正是这一项的终态，而等待 submit 的调用方也还没返回。
		q.notifyTerminal(task, result, err)

		q.mu.Lock()
		finishedAt := time.Now()
		task.finishedAt = &finishedAt
		task.cancel = nil
		task.result = result
		task.err = err
		task.status = queueStatusFromResult(result, err)
		q.active = nil
		close(task.done)
		snapshot = q.snapshotLocked()
		q.mu.Unlock()
		q.emitSnapshot(snapshot)
	}
}

// notifyTerminal 在字幕任务走到终态时发一条系统通知（D-013）。
// 取消不发：那是用户自己按的，桌面上再弹一条只是噪音。
func (q *subtitleTaskQueue) notifyTerminal(task *subtitleQueueTask, result *SubtitleGenerateResult, err error) {
	q.mu.Lock()
	notifier := q.notifier
	q.mu.Unlock()
	if notifier == nil {
		return
	}
	name := notificationMediaName(task.VideoName)
	switch {
	case err != nil:
		// 失败原因不进通知文案：识别与转写的报错里经常带绝对路径。
		notifyDesktop(notifier, "字幕生成失败", fmt.Sprintf("《%s》字幕生成失败，可在字幕任务面板查看原因", name))
	case result != nil && result.Status == SubtitleResultStatusValidationFailed:
		notifyDesktop(notifier, "字幕需要确认", fmt.Sprintf("《%s》检测到疑似模型幻觉，需要确认是否强制生成", name))
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

func queueStatusFromResult(result *SubtitleGenerateResult, err error) SubtitleQueueTaskStatus {
	if result != nil && result.Status == SubtitleResultStatusCancelled {
		return SubtitleQueueTaskStatusCancelled
	}
	if err != nil {
		return SubtitleQueueTaskStatusFailed
	}
	return SubtitleQueueTaskStatusSucceeded
}

func (q *subtitleTaskQueue) emitSnapshot(snapshot SubtitleQueueSnapshot) {
	if q.emit != nil {
		q.emit(snapshot)
	}
}
