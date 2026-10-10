package services

import (
	"context"
	"fmt"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

type TonightRequest struct {
	Filter             LibraryFilter `json:"filter"`
	MaxDurationSeconds int64         `json:"max_duration_seconds"`
	UnwatchedOnly      bool          `json:"unwatched_only"`
	FavoritesOnly      bool          `json:"favorites_only"`
	Limit              int           `json:"limit"`
}

type TonightSuggestion struct {
	Video   models.Video `json:"video"`
	Reasons []string     `json:"reasons"`
}

// SuggestTonight reads the same library scope, then applies a one-use viewing
// budget. It never launches playback or changes the balanced random algorithm.
func (s *VideoService) SuggestTonight(ctx context.Context, request TonightRequest) ([]TonightSuggestion, error) {
	if request.MaxDurationSeconds < 0 || request.MaxDurationSeconds > 86400 {
		return nil, fmt.Errorf("可用时间必须在 0–1440 分钟之间")
	}
	if request.Limit < 0 || request.Limit > 12 {
		return nil, fmt.Errorf("推荐数量必须在 1–12 之间")
	}
	if request.Limit == 0 {
		request.Limit = 6
	}
	filter, err := normalizeLibraryFilter(request.Filter)
	if err != nil {
		return nil, err
	}
	result := make([]TonightSuggestion, 0, request.Limit)
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if libraryFilterNeedsSubtitleSync(filter) {
			if err := syncSubtitleIndexesFromFilesystem(); err != nil {
				return err
			}
		}
		query, err := applyLibraryFilter(db.Model(&models.Video{}), filter, time.Now())
		if err != nil {
			return err
		}
		query = query.Where("videos.is_stale = ?", false)
		if request.MaxDurationSeconds > 0 {
			query = query.Where("videos.duration > 0 AND videos.duration <= ?", request.MaxDurationSeconds)
		}
		if request.UnwatchedOnly {
			query = query.Where("videos.is_watched = ?", false)
		}
		if request.FavoritesOnly {
			query = query.Where("videos.is_favorite = ?", true)
		}
		var videos []models.Video
		if err := query.Preload("Tags").Order("videos.is_favorite DESC").Order("videos.is_watched ASC").
			Order("COALESCE(videos.personal_rating, -1) DESC").Order("videos.created_at DESC").Order("videos.id DESC").
			Limit(request.Limit).Find(&videos).Error; err != nil {
			return err
		}
		for _, video := range videos {
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
			}
			if video.PersonalRating != nil {
				reasons = append(reasons, fmt.Sprintf("个人评分 %.1f", *video.PersonalRating))
			}
			result = append(result, TonightSuggestion{Video: video, Reasons: reasons})
		}
		return nil
	})
	return result, err
}
