package services

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
)

const (
	RandomPlayModeBalanced  = "balanced"
	RandomPlayModeUnwatched = "unwatched"
	RandomPlayModeFavorites = "favorites"
)

// RandomPlayRequest 描述筛选内随机播放的边界和模式。
type RandomPlayRequest struct {
	Filter     LibraryFilter `json:"filter"`
	Mode       string        `json:"mode"`
	ExcludeIDs []uint        `json:"exclude_ids"`
}

type videoScoreRow struct {
	ID              uint
	PlayCount       int
	RandomPlayCount int
	LastPlayedAt    *time.Time
}

func (s *VideoService) getRandomPlayConfig() (float64, int, error) {
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return 0, 0, fmt.Errorf("获取设置失败: %w", err)
	}
	playWeight := settings.PlayWeight
	if playWeight < 0.1 {
		playWeight = 0.1
	}
	return playWeight, normalizeRandomHalfLifeDays(settings.RandomHalfLifeDays), nil
}

// PlayRandomVideo 智能加权随机发起播放。全库随机没有「换一个」，启动成功即记账；
// 开始前同样先提交上一条未决的筛选内随机，让它的计数进入这一轮的权重。
func (s *VideoService) PlayRandomVideo() (*PlaybackAttemptResult, error) {
	randomCommits.flush()
	// 获取播放权重配置
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}
	playWeight := settings.PlayWeight
	if playWeight < 0.1 {
		playWeight = 0.1
	}
	halfLifeDays := normalizeRandomHalfLifeDays(settings.RandomHalfLifeDays)

	// 仅查询计算权重所需的最少字段，避免全量加载
	var rows []videoScoreRow
	if err := database.DB.Model(&models.Video{}).
		Select("id, play_count, random_play_count, last_played_at").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return &PlaybackAttemptResult{
			DispatchSucceeded: false,
			ReasonCode:        "no_videos",
			UserMessage:       "随机播放失败：当前没有可播放的视频记录。",
		}, nil
	}

	return s.playRandomFromRows(rows, playWeight, halfLifeDays, time.Now(), "按全库均衡权重选择")
}

// randomCandidatePool 是筛选内随机的候选集合，随机播放和随机取样共用同一份边界、模式和权重配置。
type randomCandidatePool struct {
	mode         string
	rows         []videoScoreRow
	playWeight   float64
	halfLifeDays int
}

// collectRandomCandidates 按请求的筛选条件和随机模式组装候选行。
func (s *VideoService) collectRandomCandidates(request RandomPlayRequest) (*randomCandidatePool, error) {
	mode := strings.TrimSpace(request.Mode)
	if mode == "" {
		mode = RandomPlayModeBalanced
	}
	if mode != RandomPlayModeBalanced && mode != RandomPlayModeUnwatched && mode != RandomPlayModeFavorites {
		return nil, fmt.Errorf("不支持的随机播放模式: %s", mode)
	}
	if libraryFilterNeedsSubtitleSync(request.Filter) {
		if err := syncSubtitleIndexesFromFilesystem(); err != nil {
			return nil, err
		}
	}
	playWeight, halfLifeDays, err := s.getRandomPlayConfig()
	if err != nil {
		return nil, err
	}
	query := database.DB.Model(&models.Video{}).
		Select("videos.id, videos.play_count, videos.random_play_count, videos.last_played_at").
		Where("videos.is_stale = ?", false)
	query, err = applyLibraryFilter(query, request.Filter, time.Now())
	if err != nil {
		return nil, err
	}
	switch mode {
	case RandomPlayModeUnwatched:
		query = query.Where("videos.is_watched = ?", false)
	case RandomPlayModeFavorites:
		query = query.Where("videos.is_favorite = ?", true)
	}
	excludeIDs := uniqueUintIDs(request.ExcludeIDs)
	if len(excludeIDs) > 100 {
		excludeIDs = excludeIDs[len(excludeIDs)-100:]
	}
	if len(excludeIDs) > 0 {
		query = query.Where("videos.id NOT IN ?", excludeIDs)
	}
	var rows []videoScoreRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return &randomCandidatePool{mode: mode, rows: rows, playWeight: playWeight, halfLifeDays: halfLifeDays}, nil
}

// PlayRandomVideoWithFilter 在当前筛选范围内执行加权随机播放（D-PC43、R10）。
//
// 播放器启动成功后不立即写统计，而是登记为全应用唯一的未决提交：30 秒后、下一次随机播放之前或
// 应用关闭时，才在同一个事务里写入 random_play_count、last_played_at 与 desktop_random 事件；
// 30 秒内用返回的 reroll_token 调用 RerollRandom 则整次丢弃。启动失败不登记，统计仍只在成功后写。
func (s *VideoService) PlayRandomVideoWithFilter(request RandomPlayRequest) (*PlaybackAttemptResult, error) {
	// 用户没有「换一个」就又随机了一次，上一条就是真的播了。先提交再抽，
	// 它的计数才会进入这一轮的权重，与启动即记账时的抽取分布一致。
	randomCommits.flush()
	pool, err := s.collectRandomCandidates(request)
	if err != nil {
		return nil, err
	}
	reason := randomModeReason(pool.mode)
	if len(pool.rows) == 0 {
		return &PlaybackAttemptResult{
			DispatchSucceeded: false,
			ReasonCode:        "no_filtered_videos",
			UserMessage:       "随机播放失败：当前筛选范围没有可播放的视频。",
			SelectionReason:   reason,
		}, nil
	}
	// 令牌在启动播放器之前生成：生成失败时什么都还没发生，直接报错即可。
	token, err := newRandomRerollToken()
	if err != nil {
		return nil, err
	}
	video, err := selectWeightedRandomVideo(pool.rows, pool.playWeight, pool.halfLifeDays, time.Now())
	if err != nil {
		return nil, err
	}
	if failure := s.launchFormalPlayback(video); failure != nil {
		failure.SelectionReason = reason
		return failure, nil
	}
	randomCommits.register(&pendingRandomCommit{token: token, videoID: video.ID, at: time.Now(), request: request})
	// 返回的视频是库里的现值：计数与 last_played_at 要等提交后才变。
	return &PlaybackAttemptResult{Video: video, DispatchSucceeded: true, SelectionReason: reason, RerollToken: token}, nil
}

// RerollRandom 是随机结果条上的「换一个」：令牌仍未决时丢弃那次随机的统计（计数与事件都不写），
// 再用原来的筛选、模式与排除表（加上被换掉的这一部）抽下一条，新结果带新的令牌。
// 令牌已提交、已被换掉或不认识时返回 reason_code=reroll_expired，不抽取、不写任何东西。
func (s *VideoService) RerollRandom(token string) (*PlaybackAttemptResult, error) {
	discarded, ok := randomCommits.discard(strings.TrimSpace(token))
	if !ok {
		return &PlaybackAttemptResult{
			DispatchSucceeded: false,
			ReasonCode:        "reroll_expired",
			UserMessage:       "「换一个」已失效：上一次随机播放已计入统计，请重新随机。",
		}, nil
	}
	request := discarded.request
	request.ExcludeIDs = append(append([]uint(nil), discarded.request.ExcludeIDs...), discarded.videoID)
	return s.PlayRandomVideoWithFilter(request)
}

// randomRerollWindow 是随机播放启动后允许「换一个」的时长，也是统计延迟提交的时长（R10）。
const randomRerollWindow = 30 * time.Second

// pendingRandomCommit 是一次已启动、尚未记账的筛选内随机播放。at 是播放器启动的时刻，
// 提交时写进 last_played_at 与事件；request 留给「换一个」按同一范围再抽。
type pendingRandomCommit struct {
	token   string
	videoID uint
	at      time.Time
	request RandomPlayRequest
	timer   stoppableTimer
}

type stoppableTimer interface{ Stop() bool }

// randomCommitSlot 保存全应用唯一的一条未决随机提交。
//
// 一把互斥锁同时覆盖「取出未决项」与「写库」：到期、换一个、新的随机、应用关闭四条路径
// 无论怎样并发，每条未决项都恰好被提交或丢弃一次；flush 返回时也不会还有提交在写库。
// 定时器回调只按令牌认领，已被取走的项再触发也是空操作。这把锁只在进程内，不是数据库锁。
type randomCommitSlot struct {
	mu        sync.Mutex
	pending   *pendingRandomCommit
	afterFunc func(time.Duration, func()) stoppableTimer
	commit    func(videoID uint, playedAt time.Time) error
}

func newRandomCommitSlot() *randomCommitSlot {
	return &randomCommitSlot{
		afterFunc: func(d time.Duration, f func()) stoppableTimer { return time.AfterFunc(d, f) },
		commit:    recordDeferredRandomPlaybackStats,
	}
}

var randomCommits = newRandomCommitSlot()

// FlushPendingRandomCommit 立即提交未决的随机播放统计。接线项：App.shutdown 在关闭数据库之前调用，
// 让 30 秒窗口内退出的那一次随机照样计数。
func FlushPendingRandomCommit() {
	randomCommits.flush()
}

// register 登记新的未决项；已有未决项时先提交它（每个应用只有一条）。
func (s *randomCommitSlot) register(pending *pendingRandomCommit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitLocked()
	token := pending.token
	pending.timer = s.afterFunc(randomRerollWindow, func() { s.expire(token) })
	s.pending = pending
}

// expire 是定时器回调：令牌仍是当前未决项时提交它。
func (s *randomCommitSlot) expire(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil && s.pending.token == token {
		s.commitLocked()
	}
}

// discard 在令牌仍未决时取走并丢弃它，返回被丢弃的项。
func (s *randomCommitSlot) discard(token string) (*pendingRandomCommit, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.pending
	if pending == nil || token == "" || pending.token != token {
		return nil, false
	}
	s.pending = nil
	pending.timer.Stop()
	return pending, true
}

func (s *randomCommitSlot) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitLocked()
}

// commitLocked 取走并提交当前未决项。写失败沿用正式播放的语义：只记一行日志，
// 计数与事件在同一事务里一起回滚，两者仍然一致。
func (s *randomCommitSlot) commitLocked() {
	pending := s.pending
	if pending == nil {
		return
	}
	s.pending = nil
	pending.timer.Stop()
	if err := s.commit(pending.videoID, pending.at); err != nil {
		log.Printf("提交随机播放统计失败 id=%d err=%v", pending.videoID, err)
	}
}

// newRandomRerollToken 生成 32 位十六进制的「换一个」令牌（G-1：随机标识不用 uuid）。
func newRandomRerollToken() (string, error) {
	var buffer [16]byte
	if _, err := cryptorand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("生成随机播放令牌失败: %w", err)
	}
	return hex.EncodeToString(buffer[:]), nil
}

// RandomPickMaxCount 限制单次随机取样的条数，避免把过大的结果集一次性加载进内存。
const RandomPickMaxCount = 50

// RandomPickResult 是「随机 N 部」的取样结果。取样只挑视频、不派发播放，因此不写播放统计。
type RandomPickResult struct {
	Videos          []models.Video `json:"videos"`
	SelectionReason string         `json:"selection_reason"`
	ReasonCode      string         `json:"reason_code"`
	UserMessage     string         `json:"user_message"`
}

// PickRandomVideos 用与随机播放完全相同的候选边界和加权规则，抽取 count 条互不重复的视频。
func (s *VideoService) PickRandomVideos(request RandomPlayRequest, count int) (*RandomPickResult, error) {
	if count <= 0 {
		return nil, fmt.Errorf("随机取样条数必须大于 0")
	}
	if count > RandomPickMaxCount {
		return nil, fmt.Errorf("随机取样条数不能超过 %d", RandomPickMaxCount)
	}
	pool, err := s.collectRandomCandidates(request)
	if err != nil {
		return nil, err
	}
	reason := randomModeReason(pool.mode)
	if len(pool.rows) == 0 {
		return &RandomPickResult{
			Videos:          []models.Video{},
			SelectionReason: reason,
			ReasonCode:      "no_filtered_videos",
			UserMessage:     "随机取样失败：当前筛选范围没有可用的视频。",
		}, nil
	}
	weights, totalWeight := randomSelectionWeights(pool.rows, pool.playWeight, pool.halfLifeDays, time.Now())
	ids := make([]uint, 0, count)
	for _, index := range weightedSampleWithoutReplacement(weights, totalWeight, count) {
		ids = append(ids, pool.rows[index].ID)
	}
	videos, err := s.GetVideosByIDs(ids)
	if err != nil {
		return nil, fmt.Errorf("查询随机取样结果失败: %w", err)
	}
	return &RandomPickResult{Videos: videos, SelectionReason: reason}, nil
}

// weightedSampleWithoutReplacement 连续做加权抽取：每轮只在尚未抽中的候选里按权重选一个，
// 再把它的权重从剩余总量里扣掉，保证抽出的下标互不重复且单次抽取的分布与随机播放一致。
func weightedSampleWithoutReplacement(weights []float64, totalWeight float64, count int) []int {
	if count > len(weights) {
		count = len(weights)
	}
	taken := make([]bool, len(weights))
	picked := make([]int, 0, count)
	remaining := totalWeight
	for len(picked) < count {
		target := rand.Float64() * remaining
		selected := -1
		cumulative := 0.0
		for index, weight := range weights {
			if taken[index] {
				continue
			}
			cumulative += weight
			if target <= cumulative {
				selected = index
				break
			}
		}
		if selected < 0 {
			// 防御浮点精度：累计和可能略小于 remaining，退回最后一个还没抽中的候选。
			for index := len(weights) - 1; index >= 0; index-- {
				if !taken[index] {
					selected = index
					break
				}
			}
		}
		if selected < 0 {
			break
		}
		taken[selected] = true
		remaining -= weights[selected]
		picked = append(picked, selected)
	}
	return picked
}

func randomModeReason(mode string) string {
	switch mode {
	case RandomPlayModeUnwatched:
		return "在当前筛选范围内仅选择未看视频"
	case RandomPlayModeFavorites:
		return "在当前筛选范围内选择收藏视频"
	default:
		return "在当前筛选范围内按播放次数均衡选择"
	}
}

func (s *VideoService) playRandomFromRows(rows []videoScoreRow, playWeight float64, halfLifeDays int, now time.Time, selectionReason string) (*PlaybackAttemptResult, error) {
	if len(rows) == 0 {
		return &PlaybackAttemptResult{
			DispatchSucceeded: false,
			ReasonCode:        "no_videos",
			UserMessage:       "随机播放失败：当前没有可播放的视频记录。",
		}, nil
	}
	selectedVideo, err := selectWeightedRandomVideo(rows, playWeight, halfLifeDays, now)
	if err != nil {
		return nil, err
	}

	// 使用数据库原子操作更新随机播放次数和最后播放时间
	result, err := s.dispatchFormalPlayback(selectedVideo, true)
	if result != nil {
		result.SelectionReason = selectionReason
	}
	return result, err
}

// selectWeightedRandomVideo 按权重抽出一条候选，并只对它查询完整记录（含 Tags）。rows 不能为空。
func selectWeightedRandomVideo(rows []videoScoreRow, playWeight float64, halfLifeDays int, now time.Time) (*models.Video, error) {
	weights, totalWeight := randomSelectionWeights(rows, playWeight, halfLifeDays, now)

	// 使用加权随机选择（Go 1.20+ 全局 rand 已自动 seed，无需手动调用）
	randomValue := rand.Float64() * totalWeight
	selectedIdx := len(rows) - 1 // 默认最后一个（防御浮点精度）
	cumulative := 0.0
	for i, w := range weights {
		cumulative += w
		if randomValue <= cumulative {
			selectedIdx = i
			break
		}
	}

	var selectedVideo models.Video
	if err := database.DB.Preload("Tags").First(&selectedVideo, rows[selectedIdx].ID).Error; err != nil {
		return nil, fmt.Errorf("查询选中视频失败: %w", err)
	}
	return &selectedVideo, nil
}

func randomSelectionWeights(rows []videoScoreRow, playWeight float64, halfLifeDays int, now time.Time) ([]float64, float64) {
	scores := make([]float64, len(rows))
	maxScore := 0.0
	for index, row := range rows {
		scores[index] = decayedPlayScore(row, playWeight, halfLifeDays, now)
		if scores[index] > maxScore {
			maxScore = scores[index]
		}
	}
	weights := make([]float64, len(rows))
	totalWeight := 0.0
	for index, score := range scores {
		weights[index] = maxScore - score + 1.0
		totalWeight += weights[index]
	}
	return weights, totalWeight
}

func decayedPlayScore(row videoScoreRow, playWeight float64, halfLifeDays int, now time.Time) float64 {
	base := float64(row.PlayCount)*playWeight + float64(row.RandomPlayCount)
	if halfLifeDays <= 0 || row.LastPlayedAt == nil || !now.After(*row.LastPlayedAt) {
		return base
	}
	ageDays := now.Sub(*row.LastPlayedAt).Hours() / 24
	return base * math.Exp(-math.Ln2*ageDays/float64(halfLifeDays))
}
