package services

import (
	"container/list"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
)

// PlayEventSourceInlineView 是内嵌播放器的「有效观看」事件来源（D-PC43）：同一会话累计播放首次越过
// viewThreshold，或播到结尾时，前端调用 RecordViewEvent 记一条。它只写账本，不动 videos 上的计数列，
// 因此不影响随机算法（随机打分仍只读三列）。
const PlayEventSourceInlineView = "inline_view"

const (
	// viewThresholdCapSeconds 是有效观看阈值的上限，也是时长未知时的阈值（R10）。
	viewThresholdCapSeconds = 60.0
	// viewEventDedupCapacity 是会话去重表的容量：超出后淘汰最久未出现的会话。
	viewEventDedupCapacity = 1024
	// viewEventSessionIDMaxLength 限制前端传入的会话标识长度，防止无界字符串进入内存表。
	viewEventSessionIDMaxLength = 64
)

// viewThreshold 是有效观看的累计播放阈值（秒）：min(60, 时长 × 50%)；时长未知（≤0、NaN、Inf）取 60。
// 后端只在这里定义；服务端自己判定阈值的路径（Jellyfin 进度上报）直接调用它，前端内嵌播放器与
// 手机端用同一组样例对齐的 JS 版本。
func viewThreshold(duration float64) float64 {
	if !(duration > 0) || math.IsInf(duration, 0) {
		return viewThresholdCapSeconds
	}
	return math.Min(viewThresholdCapSeconds, duration*0.5)
}

// viewEventDedup 是按会话去重的内存 LRU。它不持久化：应用重启后同一会话标识不会再出现，
// 因为会话标识由前端在每次打开播放器时新生成。
type viewEventDedup struct {
	mu       sync.Mutex
	capacity int
	order    *list.List // 表头是最近出现的会话
	entries  map[string]*list.Element
}

func newViewEventDedup(capacity int) *viewEventDedup {
	return &viewEventDedup{capacity: capacity, order: list.New(), entries: make(map[string]*list.Element, capacity)}
}

var viewEvents = newViewEventDedup(viewEventDedupCapacity)

// seenLocked 报告会话是否已记过，已记过时把它移到表头。调用方持有 mu。
func (d *viewEventDedup) seenLocked(key string) bool {
	element, ok := d.entries[key]
	if ok {
		d.order.MoveToFront(element)
	}
	return ok
}

// rememberLocked 记下一个会话，超出容量时淘汰表尾。调用方持有 mu。
func (d *viewEventDedup) rememberLocked(key string) {
	d.entries[key] = d.order.PushFront(key)
	for d.order.Len() > d.capacity {
		oldest := d.order.Back()
		d.order.Remove(oldest)
		delete(d.entries, oldest.Value.(string))
	}
}

var errViewEventVideoMissing = errors.New("视频不存在或已删除")

// RecordViewEvent 记录一次有效观看。source 目前只接受 inline_view；同一个 (source, videoID, sessionID)
// 只记一次，重复调用返回 (false, nil)。事件只追加到 play_events，不做任何 UPDATE / DELETE。
// 写入失败时不记入去重表，调用方可以用同一个会话标识重试。
func (s *VideoService) RecordViewEvent(videoID uint, source string, sessionID string) (bool, error) {
	source = strings.TrimSpace(source)
	if source != PlayEventSourceInlineView {
		return false, fmt.Errorf("不支持的观看来源: %s", source)
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, errors.New("观看会话标识不能为空")
	}
	if len(sessionID) > viewEventSessionIDMaxLength {
		return false, fmt.Errorf("观看会话标识不能超过 %d 个字符", viewEventSessionIDMaxLength)
	}
	if videoID == 0 {
		return false, errViewEventVideoMissing
	}
	return viewEvents.record(videoID, source, sessionID, time.Now())
}

// record 在去重锁内完成「查重 → 写事件 → 记入去重表」：同一会话的并发调用恰好写一条。
// 锁只在进程内，不是数据库锁；每个会话只写一次，持锁写库不会形成排队热点。
func (d *viewEventDedup) record(videoID uint, source, sessionID string, at time.Time) (bool, error) {
	key := fmt.Sprintf("%s|%d|%s", source, videoID, sessionID)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seenLocked(key) {
		return false, nil
	}
	var active int64
	if err := database.DB.Model(&models.Video{}).Where("id = ?", videoID).Count(&active).Error; err != nil {
		return false, err
	}
	if active == 0 {
		return false, errViewEventVideoMissing
	}
	if err := database.DB.Create(&models.PlayEvent{VideoID: videoID, PlayedAt: at, Source: source}).Error; err != nil {
		return false, err
	}
	d.rememberLocked(key)
	return true, nil
}
