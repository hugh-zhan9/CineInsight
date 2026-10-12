package services

import (
	"context"
	"errors"
	"sync"
)

// 失联数据治理（docs/loopx/design/2026-10-12-data-care，D-LD-01..05）：失联报告、找回、清除与孤儿文件回收。
// 报告与预览只读；找回、清除、回收在 lost_media 任务键下运行，同一时刻只允许一个，可取消。

// 媒体类型。
const (
	LostMediaKindVideo = "video"
	LostMediaKindImage = "image"
)

// 失联归类（详细设计 1.1）。卷未挂载与读取出错只展示；其余可找回、可清除。
const (
	LostReasonOffline      = "offline"
	LostReasonReadError    = "read_error"
	LostReasonMissing      = "missing"
	LostReasonRemovedRoot  = "removed_root"
	LostReasonOutsideRoots = "outside_roots"
	LostReasonUnknown      = "unknown"
)

// 失联条目的两种存法。
const (
	LostSourceStale        = "stale"
	LostSourceScannerTrash = "scanner_trash"
)

// LostPurgeConfirmText 是清除的确认文本（详细设计 1.3）。
const LostPurgeConfirmText = "清除记录"

// LostMediaError 是带固定错误码的错误；文案形如 "code: 说明"，前端按前缀取码。
type LostMediaError struct {
	Code    string
	Message string
}

func (e *LostMediaError) Error() string { return e.Code + ": " + e.Message }

var (
	ErrLostMediaBusy        = &LostMediaError{Code: "lost_media_busy", Message: "已有找回、清除或回收在进行，请等它结束或取消"}
	ErrLostPurgeUnconfirmed = &LostMediaError{Code: "lost_purge_unconfirmed", Message: "请输入「" + LostPurgeConfirmText + "」确认"}
	ErrLostMediaInvalidKind = &LostMediaError{Code: "lost_media_invalid_kind", Message: "媒体类型只能是视频或图片"}
	errLostMediaNoDataDir   = &LostMediaError{Code: "lost_media_no_data_dir", Message: "应用数据目录不可用"}
)

func lostMediaInvalid(message string) error {
	return &LostMediaError{Code: "lost_media_invalid_request", Message: message}
}

// LostMediaErrorCode 取出错误码；不是失联错误时返回空串。
func LostMediaErrorCode(err error) string {
	var coded *LostMediaError
	if err != nil && errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func validLostMediaKind(kind string) error {
	if kind != LostMediaKindVideo && kind != LostMediaKindImage {
		return ErrLostMediaInvalidKind
	}
	return nil
}

// lostReasonActionable 报告这一归类能不能找回与清除（两者同一口径，详细设计 1.1）。
func lostReasonActionable(reason string) bool {
	return reason != LostReasonOffline && reason != LostReasonReadError
}

// LostMediaService 是失联数据治理（D-LD-*）。
type LostMediaService struct {
	dataDir  string
	registry *BackgroundTaskRegistry
	videos   *VideoService
	images   *ImageService
	proxies  *PlaybackProxyService

	// queueEmit 是播放队列变更通知（与 PlaybackQueueService 的 emit 同一个 playback-queue-state 事件）。
	queueEmit func(sequence uint64)

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	status  LostMediaStatus
}

// LostMediaStatus 是进行中的找回 / 清除 / 回收的进度（任务中心读它）。
type LostMediaStatus struct {
	Running   bool   `json:"running"`
	Operation string `json:"operation"` // relink / purge / reclaim
	Processed int    `json:"processed"`
	Total     int    `json:"total"`
}

func NewLostMediaService(dataDir string) *LostMediaService {
	return &LostMediaService{dataDir: dataDir}
}

func (s *LostMediaService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s != nil {
		s.registry = registry
	}
}

// SetDependencies 接上找回与清除复用的现有服务（重新定位、补探测、播放代理）。
func (s *LostMediaService) SetDependencies(videos *VideoService, images *ImageService, proxies *PlaybackProxyService) {
	if s == nil {
		return
	}
	s.videos, s.images, s.proxies = videos, images, proxies
}

// SetPlaybackQueueEmitter 注入播放队列变更通知：清除删掉了待播队列里的条目时发一次，队列面板据此重读。
func (s *LostMediaService) SetPlaybackQueueEmitter(emit func(sequence uint64)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.queueEmit = emit
	s.mu.Unlock()
}

func (s *LostMediaService) notifyQueueChanged() {
	s.mu.Lock()
	emit := s.queueEmit
	s.mu.Unlock()
	if emit != nil {
		emit(queueEventSequence.Add(1))
	}
}

// Status 返回进行中的操作进度（没有在跑时 Running=false）。
func (s *LostMediaService) Status() LostMediaStatus {
	if s == nil {
		return LostMediaStatus{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// begin 占住 lost_media 任务键：同一时刻只允许一个找回 / 清除 / 回收，第二个返回 lost_media_busy。
// 返回的 finish 必须调用一次。
func (s *LostMediaService) begin(parent context.Context, operation string, total int) (context.Context, func(), error) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil, nil, ErrLostMediaBusy
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.running, s.cancel, s.done = true, cancel, done
	s.status = LostMediaStatus{Running: true, Operation: operation, Total: total}
	s.mu.Unlock()
	s.registry.Begin(BackgroundTaskLostMedia)
	finish := func() {
		cancel()
		s.mu.Lock()
		s.running, s.cancel, s.done = false, nil, nil
		s.status.Running = false
		s.mu.Unlock()
		s.registry.End(BackgroundTaskLostMedia)
		close(done)
	}
	return ctx, finish, nil
}

// advance 记一条已处理（进度）。
func (s *LostMediaService) advance() {
	s.mu.Lock()
	s.status.Processed++
	s.mu.Unlock()
}

// Cancel 取消进行中的操作：已提交的条目保留，结果里标 Cancelled。没有在跑时什么都不做。
func (s *LostMediaService) Cancel() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// StopAndWait 停止并等待进行中的工作（进入恢复维护与退出时调用）。
func (s *LostMediaService) StopAndWait() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// LostMediaBatchResult 是找回 / 清除的逐条结果（详细设计 1.5）：Items 只列跳过与失败的条目。
type LostMediaBatchResult struct {
	Done      int                    `json:"done"`
	Skipped   int                    `json:"skipped"`
	Failed    int                    `json:"failed"`
	Cancelled bool                   `json:"cancelled"`
	Items     []LostMediaItemOutcome `json:"items"`
}

// LostMediaItemOutcome 是一条跳过或失败的条目；Reason 是固定原因码（不含路径与底层错误原文，G-3）。
type LostMediaItemOutcome struct {
	ID      uint   `json:"id"`
	Outcome string `json:"outcome"` // skipped / failed
	Reason  string `json:"reason"`
}

func (r *LostMediaBatchResult) skip(id uint, reason string) {
	r.Skipped++
	r.Items = append(r.Items, LostMediaItemOutcome{ID: id, Outcome: "skipped", Reason: reason})
}

func (r *LostMediaBatchResult) fail(id uint) {
	r.Failed++
	r.Items = append(r.Items, LostMediaItemOutcome{ID: id, Outcome: "failed", Reason: "error"})
}
