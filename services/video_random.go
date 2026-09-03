package services

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
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

// PlayRandomVideo 智能加权随机发起播放
func (s *VideoService) PlayRandomVideo() (*PlaybackAttemptResult, error) {
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

// PlayRandomVideoWithFilter 在当前筛选范围内执行加权随机播放。
func (s *VideoService) PlayRandomVideoWithFilter(request RandomPlayRequest) (*PlaybackAttemptResult, error) {
	pool, err := s.collectRandomCandidates(request)
	if err != nil {
		return nil, err
	}
	if len(pool.rows) == 0 {
		return &PlaybackAttemptResult{
			DispatchSucceeded: false,
			ReasonCode:        "no_filtered_videos",
			UserMessage:       "随机播放失败：当前筛选范围没有可播放的视频。",
			SelectionReason:   randomModeReason(pool.mode),
		}, nil
	}
	return s.playRandomFromRows(pool.rows, pool.playWeight, pool.halfLifeDays, time.Now(), randomModeReason(pool.mode))
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
		return "在当前筛选范围内优先选择未看视频"
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

	// 仅对选中的视频查询完整记录（含 Tags）
	var selectedVideo models.Video
	if err := database.DB.Preload("Tags").First(&selectedVideo, rows[selectedIdx].ID).Error; err != nil {
		return nil, fmt.Errorf("查询选中视频失败: %w", err)
	}

	// 使用数据库原子操作更新随机播放次数和最后播放时间
	result, err := s.dispatchFormalPlayback(&selectedVideo, true)
	if result != nil {
		result.SelectionReason = selectionReason
	}
	return result, err
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
