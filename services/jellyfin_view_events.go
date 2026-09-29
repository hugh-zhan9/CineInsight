package services

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"sync"
	"time"
	"video-master/models"
)

// PlayEventSourceJellyfinView 是 Jellyfin 客户端的「有效观看」事件来源（D-PC43、详细设计 §8.4）：
// 同一播放会话累计播放首次越过 viewThreshold，或上报位置判为看完时，记一条。与 inline_view 一样
// 只追加 play_events，不动 videos 上的计数列。
const PlayEventSourceJellyfinView = "jellyfin_view"

// jellyfinViewTrackerCapacity 限制同时跟踪的播放会话数；超出时丢掉最久没有上报的会话。
const jellyfinViewTrackerCapacity = 256

type jellyfinPlayState struct {
	position float64
	at       time.Time
	played   float64
}

// jellyfinViewTracker 按会话累计 Jellyfin 客户端的播放时长。只在内存里：重启后正在进行的会话
// 从下一次上报重新累计，去重仍由 viewEvents 负责。
type jellyfinViewTracker struct {
	mu      sync.Mutex
	entries map[string]*jellyfinPlayState
}

// advance 记下本次上报的位置并返回会话累计播放秒数。只累计向前走的部分，且每一段不超过两次上报之间
// 的墙钟时间：往前拖进度条不算播放，暂停时位置不动也不算。ended=true（停止上报）时会话随之结束。
func (t *jellyfinViewTracker) advance(key string, position float64, now time.Time, ended bool) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = make(map[string]*jellyfinPlayState)
	}
	state, ok := t.entries[key]
	if !ok {
		if len(t.entries) >= jellyfinViewTrackerCapacity {
			t.evictOldestLocked()
		}
		state = &jellyfinPlayState{position: position, at: now}
		t.entries[key] = state
	} else {
		if delta := position - state.position; delta > 0 {
			if elapsed := now.Sub(state.at).Seconds(); elapsed > 0 {
				state.played += math.Min(delta, elapsed)
			}
		}
		state.position, state.at = position, now
	}
	played := state.played
	if ended {
		delete(t.entries, key)
	}
	return played
}

func (t *jellyfinViewTracker) forget(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

func (t *jellyfinViewTracker) evictOldestLocked() {
	oldestKey := ""
	var oldest time.Time
	for key, state := range t.entries {
		if oldestKey == "" || state.at.Before(oldest) {
			oldestKey, oldest = key, state.at
		}
	}
	delete(t.entries, oldestKey)
}

// jellyfinViewSessionKey 是一次播放会话的去重键：优先用客户端上报的 PlaySessionId，缺失时用
// 条目 + 设备标识（详细设计 §8.4）；两者都没有时用登录会话代替设备。客户端给的字符串只经哈希
// 进入内存表，长度固定。
func jellyfinViewSessionKey(videoID uint, playSessionID, deviceID string, token [32]byte) string {
	material := ""
	switch {
	case playSessionID != "":
		material = fmt.Sprintf("play\x00%d\x00%s", videoID, playSessionID)
	case deviceID != "":
		material = fmt.Sprintf("device\x00%d\x00%s", videoID, deviceID)
	default:
		material = fmt.Sprintf("login\x00%d\x00%x", videoID, token)
	}
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:16])
}

// recordJellyfinView 处理 Progress / Stopped 上报的有效观看判定。阈值只调用 viewThreshold，看完
// 只调用 isWatchCompleted；写入复用 viewEvents.record 的会话去重。写库失败只记日志（不含路径），
// 不影响进度上报本身，下一次上报会再试。
func (s *JellyfinServer) recordJellyfinView(video models.Video, key string, position float64, ended bool) {
	now := s.now()
	played := s.viewSessions.advance(key, position, now, ended)
	if played < viewThreshold(video.Duration) && !isWatchCompleted(position, video.Duration) {
		return
	}
	if _, err := viewEvents.record(video.ID, PlayEventSourceJellyfinView, key, now); err != nil {
		log.Printf("[Jellyfin] 视频 %d 的观看记录写入失败", video.ID)
	}
}
