package services

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrShortFeedNoEligibleVideos = errors.New("no eligible short-feed videos")

type ShortFeedMedia struct {
	Path        string
	DisplayName string
	MIME        string
	ModTime     time.Time
}

type ShortFeedService struct {
	videoService *VideoService
	now          func() time.Time
	randFloat64  func() float64
}

func NewShortFeedService(videoService *VideoService) *ShortFeedService {
	if videoService == nil {
		videoService = &VideoService{}
	}
	return &ShortFeedService{
		videoService: videoService,
		now:          time.Now,
		randFloat64:  rand.Float64,
	}
}

func (s *ShortFeedService) NextVideo(excludeIDs []uint) (*ShortFeedVideoDTO, error) {
	videos, err := s.loadEligibleVideos(nil)
	if err != nil {
		return nil, err
	}
	if len(videos) == 0 {
		return nil, ErrShortFeedNoEligibleVideos
	}

	filtered := videos
	if len(excludeIDs) > 0 {
		excludedVideos, err := s.loadEligibleVideos(excludeIDs)
		if err != nil {
			return nil, err
		}
		if len(excludedVideos) > 0 {
			filtered = excludedVideos
		}
	}

	supported := make([]models.Video, 0, len(filtered))
	for _, video := range filtered {
		if _, ok := inlinePreviewMIME(video.Path); ok {
			supported = append(supported, video)
		}
	}
	if len(supported) == 0 {
		return s.videoDTO(&filtered[0], "inline_not_supported", "当前文件格式不适合浏览器内播放。")
	}

	prefs, err := s.tagPreferenceMap()
	if err != nil {
		return nil, err
	}
	selected := s.weightedSelect(supported, prefs)
	return s.videoDTO(&selected, "", "")
}

func (s *ShortFeedService) FavoriteVideos() ([]ShortFeedVideoDTO, error) {
	var videos []models.Video
	err := database.DB.Model(&models.Video{}).
		Preload("Tags").
		Joins("JOIN short_feed_interactions ON short_feed_interactions.video_id = videos.id").
		Where("short_feed_interactions.favorited = ?", true).
		Where("videos.is_stale = ?", false).
		Where("videos.duration > ? AND videos.duration < ?", 0, ShortFeedMaxDurationSeconds).
		Order("short_feed_interactions.updated_at DESC").
		Find(&videos).Error
	if err != nil {
		return nil, err
	}

	result := make([]ShortFeedVideoDTO, 0, len(videos))
	for i := range videos {
		dto, err := s.videoDTO(&videos[i], "", "")
		if err != nil {
			return nil, err
		}
		result = append(result, *dto)
	}
	return result, nil
}

func (s *ShortFeedService) RecordShortFeedPlayback(videoID uint) (*ShortFeedInteractionDTO, error) {
	now := s.now()
	var interaction models.ShortFeedInteraction
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var video models.Video
		if err := tx.First(&video, videoID).Error; err != nil {
			return err
		}
		if !shortFeedEligible(video) {
			return ErrShortFeedNoEligibleVideos
		}
		if err := tx.Model(&models.Video{}).Where("id = ?", videoID).Updates(map[string]interface{}{
			"random_play_count": gorm.Expr("random_play_count + 1"),
			"last_played_at":    now,
			"is_stale":          false,
		}).Error; err != nil {
			return err
		}

		return upsertShortFeedInteraction(tx, videoID, func(row *models.ShortFeedInteraction) {
			row.ViewCount++
			row.LastViewedAt = &now
			interaction = *row
		})
	})
	if err != nil {
		return nil, err
	}
	return interactionDTO(&interaction), nil
}

func (s *ShortFeedService) SetLiked(videoID uint, liked bool) (*ShortFeedInteractionDTO, error) {
	now := s.now()
	var interaction models.ShortFeedInteraction
	wasLiked := false
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var video models.Video
		if err := tx.Preload("Tags").First(&video, videoID).Error; err != nil {
			return err
		}
		if !shortFeedEligible(video) {
			return ErrShortFeedNoEligibleVideos
		}

		if err := upsertShortFeedInteraction(tx, videoID, func(row *models.ShortFeedInteraction) {
			wasLiked = row.Liked
			row.Liked = liked
			if liked {
				row.LikedAt = &now
			} else {
				row.LikedAt = nil
			}
			interaction = *row
		}); err != nil {
			return err
		}

		if liked && !wasLiked {
			for _, tag := range video.Tags {
				if err := incrementShortFeedTagPreference(tx, tag.ID, ShortFeedPreferenceStep); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return interactionDTO(&interaction), nil
}

func (s *ShortFeedService) SetFavorited(videoID uint, favorited bool) (*ShortFeedInteractionDTO, error) {
	now := s.now()
	var interaction models.ShortFeedInteraction
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var video models.Video
		if err := tx.First(&video, videoID).Error; err != nil {
			return err
		}
		if !shortFeedEligible(video) {
			return ErrShortFeedNoEligibleVideos
		}
		return upsertShortFeedInteraction(tx, videoID, func(row *models.ShortFeedInteraction) {
			row.Favorited = favorited
			if favorited {
				row.FavoritedAt = &now
			} else {
				row.FavoritedAt = nil
			}
			interaction = *row
		})
	})
	if err != nil {
		return nil, err
	}
	return interactionDTO(&interaction), nil
}

func (s *ShortFeedService) DeleteVideo(videoID uint) error {
	return s.videoService.DeleteVideo(videoID, true)
}

func (s *ShortFeedService) ResolveMedia(videoID uint) (*ShortFeedMedia, error) {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return nil, err
	}
	if !shortFeedEligible(video) {
		return nil, ErrShortFeedNoEligibleVideos
	}
	info, err := os.Stat(video.Path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("short-feed media path is directory")
	}
	mimeType, ok := inlinePreviewMIME(video.Path)
	if !ok {
		mimeType = fallbackVideoMIME(video.Path)
	}
	return &ShortFeedMedia{
		Path:        video.Path,
		DisplayName: video.Name,
		MIME:        mimeType,
		ModTime:     info.ModTime(),
	}, nil
}

func (s *ShortFeedService) loadEligibleVideos(excludeIDs []uint) ([]models.Video, error) {
	var videos []models.Video
	query := database.DB.Model(&models.Video{}).
		Preload("Tags").
		Where("is_stale = ?", false).
		Where("duration > ? AND duration < ?", 0, ShortFeedMaxDurationSeconds).
		Order("id ASC")
	if len(excludeIDs) > 0 {
		query = query.Where("id NOT IN ?", excludeIDs)
	}
	if err := query.Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

func (s *ShortFeedService) tagPreferenceMap() (map[uint]float64, error) {
	var rows []models.ShortFeedTagPreference
	if err := database.DB.Find(&rows).Error; err != nil {
		return nil, err
	}
	prefs := make(map[uint]float64, len(rows))
	for _, row := range rows {
		prefs[row.TagID] = row.Score
	}
	return prefs, nil
}

func (s *ShortFeedService) weightedSelect(videos []models.Video, prefs map[uint]float64) models.Video {
	if len(videos) == 1 {
		return videos[0]
	}
	weights := make([]float64, len(videos))
	total := 0.0
	for i := range videos {
		weights[i] = shortFeedWeight(videos[i], prefs)
		total += weights[i]
	}
	if total <= 0 {
		return videos[0]
	}
	draw := s.randFloat64() * total
	cumulative := 0.0
	for i, weight := range weights {
		cumulative += weight
		if draw <= cumulative {
			return videos[i]
		}
	}
	return videos[len(videos)-1]
}

func shortFeedWeight(video models.Video, prefs map[uint]float64) float64 {
	boost := 0.0
	for _, tag := range video.Tags {
		boost += prefs[tag.ID]
	}
	if boost > ShortFeedPreferenceBoostCap {
		boost = ShortFeedPreferenceBoostCap
	}
	if boost < 0 {
		boost = 0
	}
	return 1.0 + boost
}

func (s *ShortFeedService) videoDTO(video *models.Video, reasonCode string, reasonMessage string) (*ShortFeedVideoDTO, error) {
	interaction, err := interactionForVideo(video.ID)
	if err != nil {
		return nil, err
	}
	mediaURL := ""
	mediaMIME := ""
	if mimeType, ok := inlinePreviewMIME(video.Path); ok {
		mediaURL = fmt.Sprintf("/short-media/%d", video.ID)
		mediaMIME = mimeType
	} else if reasonCode == "" {
		reasonCode = "inline_not_supported"
		reasonMessage = "当前文件格式不适合浏览器内播放。"
	}

	tags := make([]ShortFeedTagDTO, 0, len(video.Tags))
	for _, tag := range video.Tags {
		tags = append(tags, ShortFeedTagDTO{ID: tag.ID, Name: tag.Name, Color: tag.Color})
	}
	return &ShortFeedVideoDTO{
		ID:            video.ID,
		Name:          video.Name,
		Duration:      video.Duration,
		Width:         video.Width,
		Height:        video.Height,
		Tags:          tags,
		MediaURL:      mediaURL,
		MediaMIME:     mediaMIME,
		Liked:         interaction.Liked,
		Favorited:     interaction.Favorited,
		ReasonCode:    reasonCode,
		ReasonMessage: reasonMessage,
	}, nil
}

func shortFeedEligible(video models.Video) bool {
	return !video.IsStale && video.Duration > 0 && video.Duration < ShortFeedMaxDurationSeconds
}

func interactionForVideo(videoID uint) (models.ShortFeedInteraction, error) {
	var interaction models.ShortFeedInteraction
	err := database.DB.Where("video_id = ?", videoID).First(&interaction).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.ShortFeedInteraction{VideoID: videoID}, nil
	}
	return interaction, err
}

func upsertShortFeedInteraction(tx *gorm.DB, videoID uint, mutate func(*models.ShortFeedInteraction)) error {
	var interaction models.ShortFeedInteraction
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("video_id = ?", videoID).First(&interaction).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		interaction = models.ShortFeedInteraction{VideoID: videoID}
	} else if err != nil {
		return err
	}

	mutate(&interaction)
	if interaction.ID == 0 {
		return tx.Create(&interaction).Error
	}
	return tx.Save(&interaction).Error
}

func incrementShortFeedTagPreference(tx *gorm.DB, tagID uint, delta float64) error {
	var tag models.Tag
	if err := tx.First(&tag, tagID).Error; err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tag_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"score":      gorm.Expr("short_feed_tag_preferences.score + ?", delta),
			"updated_at": time.Now(),
		}),
	}).Create(&models.ShortFeedTagPreference{TagID: tagID, Score: delta}).Error
}

func interactionDTO(interaction *models.ShortFeedInteraction) *ShortFeedInteractionDTO {
	return &ShortFeedInteractionDTO{
		VideoID:      interaction.VideoID,
		Liked:        interaction.Liked,
		Favorited:    interaction.Favorited,
		ViewCount:    interaction.ViewCount,
		LastViewedAt: interaction.LastViewedAt,
		LikedAt:      interaction.LikedAt,
		FavoritedAt:  interaction.FavoritedAt,
	}
}
