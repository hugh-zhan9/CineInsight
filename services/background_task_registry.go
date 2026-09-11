package services

import (
	"fmt"
	"sync"
)

// BackgroundTaskKey 是后台长任务的固定标识（D-014），与设置页的任务面板一一对应。
type BackgroundTaskKey string

const (
	BackgroundTaskSubtitle          BackgroundTaskKey = "subtitle"
	BackgroundTaskEnhancement       BackgroundTaskKey = "enhancement"
	BackgroundTaskProxy             BackgroundTaskKey = "proxy"
	BackgroundTaskFace              BackgroundTaskKey = "face"
	BackgroundTaskFrameHash         BackgroundTaskKey = "frame_hash"
	BackgroundTaskPerceptualHash    BackgroundTaskKey = "phash"
	BackgroundTaskTechnical         BackgroundTaskKey = "technical"
	BackgroundTaskLocalMetadata     BackgroundTaskKey = "local_metadata"
	BackgroundTaskSemantic          BackgroundTaskKey = "semantic"
	BackgroundTaskImageSemantic     BackgroundTaskKey = "image_semantic"
	BackgroundTaskAITagging         BackgroundTaskKey = "ai_tagging"
	BackgroundTaskImageAITagging    BackgroundTaskKey = "image_ai_tagging"
	BackgroundTaskEXIF              BackgroundTaskKey = "exif"
	BackgroundTaskCleanup           BackgroundTaskKey = "cleanup"
	BackgroundTaskCollectionSuggest BackgroundTaskKey = "collection_suggest"
	BackgroundTaskBackup            BackgroundTaskKey = "backup"
	// BackgroundTaskBrowserDownload 是浏览器插件桥接推过来的下载任务（D-B04）。
	// 它不进空闲门——那道门只挡自动触发的任务，而这些是用户在浏览器里点出来的。
	BackgroundTaskBrowserDownload BackgroundTaskKey = "browser_download"
	// BackgroundTaskWatchlistEnrich 是想看片单的在线补全（D-WM13）。
	// 同样不进空闲门：补全由用户添加条目或点重试触发，与上面那条同口径。
	BackgroundTaskWatchlistEnrich BackgroundTaskKey = "watchlist_enrich"
)

// backgroundTaskKeyOrder 既是合法 key 的全集，也是 Snapshot 的稳定顺序。
// 顺序取自 D-014 的清单，不按字典序：它对应设置页面板的排布。
var backgroundTaskKeyOrder = []BackgroundTaskKey{
	BackgroundTaskSubtitle,
	BackgroundTaskEnhancement,
	BackgroundTaskProxy,
	BackgroundTaskFace,
	BackgroundTaskFrameHash,
	BackgroundTaskPerceptualHash,
	BackgroundTaskTechnical,
	BackgroundTaskLocalMetadata,
	BackgroundTaskSemantic,
	BackgroundTaskImageSemantic,
	BackgroundTaskAITagging,
	BackgroundTaskImageAITagging,
	BackgroundTaskEXIF,
	BackgroundTaskCleanup,
	BackgroundTaskCollectionSuggest,
	BackgroundTaskBackup,
	BackgroundTaskBrowserDownload,
	BackgroundTaskWatchlistEnrich,
}

// IsBackgroundTaskKey 报告字符串是否属于固定 key 集合，供前端传入的 key 校验。
func IsBackgroundTaskKey(key string) bool {
	for _, known := range backgroundTaskKeyOrder {
		if string(known) == key {
			return true
		}
	}
	return false
}

// BackgroundTaskKeys 返回固定 key 集合的副本（顺序即面板顺序）。
func BackgroundTaskKeys() []string {
	keys := make([]string, 0, len(backgroundTaskKeyOrder))
	for _, key := range backgroundTaskKeyOrder {
		keys = append(keys, string(key))
	}
	return keys
}

// BackgroundTaskRegistry 记录当前正在跑的长任务（D-014）。
//
// 用计数而不是集合：本地元数据的补全与写出共用一个 key，任一在跑就算这项在跑，
// 两条 worker 各自 Begin/End 时只有计数能配得上。
//
// 不持久化：进程重启即清零。
type BackgroundTaskRegistry struct {
	// notifyMu 串行化"改状态 + 投递回调"整体：只用 mu 的话，两个并发的
	// Begin/End 可能先后改完状态、再乱序投递，订阅方（Dock 角标、前端事件）
	// 会收到一份比当前状态更旧的快照。回调里不得再调 Begin/End。
	notifyMu sync.Mutex
	mu       sync.Mutex
	running  map[BackgroundTaskKey]int
	onChange func([]string)
}

func NewBackgroundTaskRegistry() *BackgroundTaskRegistry {
	return &BackgroundTaskRegistry{running: make(map[BackgroundTaskKey]int)}
}

// SetOnChange 注入变化回调（Dock 角标与 background-tasks 事件）。回调在锁外调用。
func (r *BackgroundTaskRegistry) SetOnChange(onChange func(running []string)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.onChange = onChange
	r.mu.Unlock()
}

// Begin 登记一个任务进入运行态。nil 接收者是允许的：服务在没有接入登记表的
// 场景（单测夹具）里照常工作。
func (r *BackgroundTaskRegistry) Begin(key BackgroundTaskKey) {
	if r == nil {
		return
	}
	assertBackgroundTaskKey(key)
	r.notifyMu.Lock()
	defer r.notifyMu.Unlock()
	r.mu.Lock()
	if r.running == nil {
		r.running = make(map[BackgroundTaskKey]int)
	}
	r.running[key]++
	snapshot, onChange := r.snapshotLocked(), r.onChange
	r.mu.Unlock()
	if onChange != nil {
		onChange(snapshot)
	}
}

// End 登记一个任务离开运行态。多余的 End 是配对错误，直接 panic：
// 计数漂移会让角标永远挂着一个不存在的任务。
func (r *BackgroundTaskRegistry) End(key BackgroundTaskKey) {
	if r == nil {
		return
	}
	assertBackgroundTaskKey(key)
	r.notifyMu.Lock()
	defer r.notifyMu.Unlock()
	r.mu.Lock()
	count := r.running[key]
	if count <= 0 {
		r.mu.Unlock()
		panic(fmt.Sprintf("services: BackgroundTaskRegistry.End(%q) 未配对 Begin", key))
	}
	if count == 1 {
		delete(r.running, key)
	} else {
		r.running[key] = count - 1
	}
	snapshot, onChange := r.snapshotLocked(), r.onChange
	r.mu.Unlock()
	if onChange != nil {
		onChange(snapshot)
	}
}

// Snapshot 返回当前运行中的任务 key，顺序固定为面板顺序。
func (r *BackgroundTaskRegistry) Snapshot() []string {
	if r == nil {
		return []string{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *BackgroundTaskRegistry) snapshotLocked() []string {
	running := make([]string, 0, len(r.running))
	for _, key := range backgroundTaskKeyOrder {
		if r.running[key] > 0 {
			running = append(running, string(key))
		}
	}
	return running
}

func assertBackgroundTaskKey(key BackgroundTaskKey) {
	if !IsBackgroundTaskKey(string(key)) {
		panic(fmt.Sprintf("services: 未知的后台任务 key %q", key))
	}
}
