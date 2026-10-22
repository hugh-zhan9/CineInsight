package services

import (
	"context"
	"fmt"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 「今晚看什么」单次候选数：请求 0 取默认 6 条，最多 12 条，越界明确报错。
const (
	tonightDefaultLimit = 6
	tonightMaxLimit     = 12
)

type TonightSuggestion struct {
	Video   models.Video `json:"video"`
	Reasons []string     `json:"reasons"`
}

// SuggestTonight 用随机播放的同一套条件与均衡加权，不放回地抽 limit 部候选并附上事实理由。
// 与随机播放共用候选边界（片库筛选、观看状态、仅收藏、时长上限、最近随机排除），
// 但只读：不启动播放、不写计数或事件。整个读取在维护闸门内、带调用方的 context。
func (s *VideoService) SuggestTonight(ctx context.Context, request RandomPlayRequest, limit int) ([]TonightSuggestion, error) {
	if limit < 0 || limit > tonightMaxLimit {
		return nil, fmt.Errorf("推荐数量必须在 1–%d 之间", tonightMaxLimit)
	}
	if limit == 0 {
		limit = tonightDefaultLimit
	}
	result := make([]TonightSuggestion, 0, limit)
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		pool, videos, err := s.sampleRandomVideosFrom(db, request, limit)
		if err != nil {
			return err
		}
		for _, video := range videos {
			result = append(result, TonightSuggestion{Video: video, Reasons: tonightWorkReasons(video, pool)})
		}
		return nil
	})
	return result, err
}

// tonightReasons 只引用真实字段：满足的条件、收藏、观看状态、已有的个人评分与播放次数。
func tonightReasons(video models.Video, request RandomPlayRequest) []string {
	reasons := []string{"符合当前片库筛选"}
	if request.MaxDurationSeconds > 0 {
		reasons = append(reasons, "整片时长符合本次时间预算")
	} else if video.Duration <= 0 {
		reasons = append(reasons, "时长未记录")
	}
	if video.IsFavorite {
		reasons = append(reasons, "已收藏")
	}
	if !video.IsWatched {
		reasons = append(reasons, "尚未标记已看")
	} else if request.WatchState == RandomWatchWatched {
		reasons = append(reasons, "看过，适合重温")
	}
	if video.PersonalRating != nil {
		reasons = append(reasons, fmt.Sprintf("个人评分 %.1f", *video.PersonalRating))
	}
	if plays := video.PlayCount + video.RandomPlayCount; plays == 0 {
		reasons = append(reasons, "还没播放过")
	} else {
		reasons = append(reasons, fmt.Sprintf("播放过 %d 次", plays))
	}
	return reasons
}

// tonightWorkReasons distinguishes the selected file's metadata from work-level viewing history.
func tonightWorkReasons(video models.Video, pool *randomCandidatePool) []string {
	for _, row := range pool.rows {
		if row.ID == video.ID {
			video.IsWatched = row.workWatched
			reasons := tonightReasons(video, pool.request)
			if row.feedbackFactor == 0.5 {
				reasons = append(reasons, "命中已选择的减少推荐标签，权重减半")
			}
			return reasons
		}
	}
	return tonightReasons(video, pool.request)
}
