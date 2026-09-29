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
// 别名表与看完窗口表用同一个上限。
const jellyfinViewTrackerCapacity = 256

// jellyfinViewCompletionWindow 是「看完」捷径的去重窗口（B-m2）：同一「条目 + 设备 / 登录会话」
// 在这段时间内记过一条 jellyfin_view 之后，只因位置落在片尾区间而成立的看完不再另记——客户端
// 中途换了 PlaySessionId（切换音轨、重新协商播放）再报片尾位置，仍是同一次观看。累计播放越过
// 阈值的照记：那是真的又看了一遍。
const jellyfinViewCompletionWindow = 10 * time.Minute

type jellyfinPlayState struct {
	position float64
	at       time.Time
	played   float64
}

// jellyfinViewAlias 是某个「条目 + 设备 / 登录会话」最近上报过的 PlaySessionId 对应的会话键。
type jellyfinViewAlias struct {
	session string
	at      time.Time
}

// jellyfinViewKeys 是一次上报的两把键：session 是累计与去重用的播放会话键，scope 是
// 「条目 + 设备 / 登录会话」，看完窗口按它计。
type jellyfinViewKeys struct {
	session string
	scope   string
}

// jellyfinViewTracker 按会话累计 Jellyfin 客户端的播放时长。只在内存里：重启后正在进行的会话
// 从下一次上报重新累计，去重仍由 viewEvents 负责。
type jellyfinViewTracker struct {
	mu      sync.Mutex
	entries map[string]*jellyfinPlayState
	// aliases：同一 scope 见过 PlaySessionId 之后，不带 PlaySessionId 的上报（常见于 Stopped）
	// 沿用它的会话键（B-m2），不会另起一个会话把同一次播放记两遍。新的 PlaySessionId 覆盖旧的。
	aliases map[string]jellyfinViewAlias
	// recorded：每个 scope 最近一次记下 jellyfin_view 的时间，供看完窗口判定。
	recorded map[string]time.Time
}

// keys 算出一次上报的会话键与 scope：优先用客户端上报的 PlaySessionId；缺失时沿用同一 scope 最近
// 见过的 PlaySessionId；都没有时就用 scope（条目 + 设备标识，设备标识也没有时用登录会话，
// 详细设计 §8.4）。客户端给的字符串只经哈希进入内存表，长度固定。
func (t *jellyfinViewTracker) keys(videoID uint, playSessionID, deviceID string, token [32]byte, now time.Time) jellyfinViewKeys {
	scope := jellyfinViewKey(fmt.Sprintf("login\x00%d\x00%x", videoID, token))
	if deviceID != "" {
		scope = jellyfinViewKey(fmt.Sprintf("device\x00%d\x00%s", videoID, deviceID))
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.aliases == nil {
		t.aliases = make(map[string]jellyfinViewAlias)
	}
	if playSessionID != "" {
		session := jellyfinViewKey(fmt.Sprintf("play\x00%d\x00%s", videoID, playSessionID))
		if _, ok := t.aliases[scope]; !ok && len(t.aliases) >= jellyfinViewTrackerCapacity {
			evictOldestAliasLocked(t.aliases)
		}
		t.aliases[scope] = jellyfinViewAlias{session: session, at: now}
		return jellyfinViewKeys{session: session, scope: scope}
	}
	if alias, ok := t.aliases[scope]; ok {
		t.aliases[scope] = jellyfinViewAlias{session: alias.session, at: now}
		return jellyfinViewKeys{session: alias.session, scope: scope}
	}
	return jellyfinViewKeys{session: scope, scope: scope}
}

func jellyfinViewKey(material string) string {
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:16])
}

func evictOldestAliasLocked(aliases map[string]jellyfinViewAlias) {
	oldestKey := ""
	var oldest time.Time
	for key, alias := range aliases {
		if oldestKey == "" || alias.at.Before(oldest) {
			oldestKey, oldest = key, alias.at
		}
	}
	delete(aliases, oldestKey)
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

// recordedWithin 报告 scope 在窗口内是否记过 jellyfin_view。
func (t *jellyfinViewTracker) recordedWithin(scope string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.recorded[scope]
	return ok && now.Sub(at) < jellyfinViewCompletionWindow
}

// noteRecorded 记下 scope 刚记过一条 jellyfin_view。表满时先丢出了窗口的，仍满时丢最早的一条。
func (t *jellyfinViewTracker) noteRecorded(scope string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.recorded == nil {
		t.recorded = make(map[string]time.Time)
	}
	if _, ok := t.recorded[scope]; !ok && len(t.recorded) >= jellyfinViewTrackerCapacity {
		oldestKey := ""
		var oldest time.Time
		for key, at := range t.recorded {
			if now.Sub(at) >= jellyfinViewCompletionWindow {
				delete(t.recorded, key)
				continue
			}
			if oldestKey == "" || at.Before(oldest) {
				oldestKey, oldest = key, at
			}
		}
		if len(t.recorded) >= jellyfinViewTrackerCapacity {
			delete(t.recorded, oldestKey)
		}
	}
	t.recorded[scope] = now
}

// recordJellyfinView 处理 Progress / Stopped 上报的有效观看判定。阈值只调用 viewThreshold，看完
// 只调用 isWatchCompleted；写入复用 viewEvents.record 的会话去重，只因看完成立的另按 scope 的
// 看完窗口去重。写库失败只记日志（不含路径），不影响进度上报本身，下一次上报会再试。
// 调用方持有 s.writes，同一台服务上的判定与写入按到达顺序串行。
func (s *JellyfinServer) recordJellyfinView(video models.Video, keys jellyfinViewKeys, position float64, ended bool) {
	now := s.now()
	played := s.viewSessions.advance(keys.session, position, now, ended)
	reachedThreshold := played >= viewThreshold(video.Duration)
	if !reachedThreshold && !isWatchCompleted(position, video.Duration) {
		return
	}
	if !reachedThreshold && s.viewSessions.recordedWithin(keys.scope, now) {
		return
	}
	recorded, err := viewEvents.record(video.ID, PlayEventSourceJellyfinView, keys.session, now)
	if err != nil {
		log.Printf("[Jellyfin] 视频 %d 的观看记录写入失败", video.ID)
		return
	}
	if recorded {
		s.viewSessions.noteRecorded(keys.scope, now)
	}
}
