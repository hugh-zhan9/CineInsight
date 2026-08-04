package services

import (
	"errors"
	"fmt"
	"log"
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

type ShortFeedFeedbackSyncResult struct {
	Enabled        bool  `json:"enabled"`
	TagID          uint  `json:"tag_id"`
	LikesAdded     int64 `json:"likes_added"`
	LikesRemoved   int64 `json:"likes_removed"`
	FavoritesAdded int64 `json:"favorites_added"`
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

	existing := s.filterExistingVideos(filtered)
	if len(existing) == 0 {
		return nil, ErrShortFeedNoEligibleVideos
	}

	supported := make([]models.Video, 0, len(existing))
	for _, video := range existing {
		if _, ok := inlinePreviewMIME(video.Path); ok {
			supported = append(supported, video)
		}
	}
	if len(supported) == 0 {
		return s.videoDTO(&existing[0], "inline_not_supported", "当前文件格式不适合浏览器内播放。")
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
	maxDurationSeconds := s.maxDurationSeconds()
	err := database.DB.Model(&models.Video{}).
		Preload("Tags").
		Joins("JOIN short_feed_interactions ON short_feed_interactions.video_id = videos.id").
		Where("short_feed_interactions.favorited = ?", true).
		Where("videos.is_stale = ?", false).
		Where("videos.duration > ? AND videos.duration < ?", 0, maxDurationSeconds).
		Order("short_feed_interactions.updated_at DESC").
		Find(&videos).Error
	if err != nil {
		return nil, err
	}

	result := make([]ShortFeedVideoDTO, 0, len(videos))
	existing := s.filterExistingVideos(videos)
	for i := range existing {
		dto, err := s.videoDTO(&existing[i], "", "")
		if err != nil {
			return nil, err
		}
		result = append(result, *dto)
	}
	return result, nil
}

func (s *ShortFeedService) RecordShortFeedPlayback(videoID uint) (*ShortFeedInteractionDTO, error) {
	now := s.now()
	maxDurationSeconds := s.maxDurationSeconds()
	var interaction models.ShortFeedInteraction
	err := database.Transaction(func(tx *gorm.DB) error {
		var video models.Video
		if err := tx.First(&video, videoID).Error; err != nil {
			return err
		}
		if !shortFeedEligible(video, maxDurationSeconds) {
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
	maxDurationSeconds := s.maxDurationSeconds()
	var interaction models.ShortFeedInteraction
	wasLiked := false
	err := database.Transaction(func(tx *gorm.DB) error {
		var video models.Video
		if err := tx.Preload("Tags").First(&video, videoID).Error; err != nil {
			return err
		}
		if !shortFeedEligible(video, maxDurationSeconds) {
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
	// 交互已提交；投影同步失败只记录日志，不把已成功的操作报成失败。
	// 投影会在下次交互、设置保存或启动同步时自愈。
	if _, err := s.SyncFeedback(); err != nil {
		log.Printf("short-feed: 同步喜欢状态到主片库失败（将于下次同步自愈）: %v", err)
	}
	return interactionDTO(&interaction), nil
}

func (s *ShortFeedService) SetFavorited(videoID uint, favorited bool) (*ShortFeedInteractionDTO, error) {
	now := s.now()
	maxDurationSeconds := s.maxDurationSeconds()
	var interaction models.ShortFeedInteraction
	err := database.Transaction(func(tx *gorm.DB) error {
		var video models.Video
		if err := tx.First(&video, videoID).Error; err != nil {
			return err
		}
		if !shortFeedEligible(video, maxDurationSeconds) {
			return ErrShortFeedNoEligibleVideos
		}
		return upsertShortFeedInteraction(tx, videoID, func(row *models.ShortFeedInteraction) {
			if row.Favorited != favorited {
				row.FavoriteSyncedToLibrary = false
			}
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
	if _, err := s.SyncFeedback(); err != nil {
		log.Printf("short-feed: 同步收藏状态到主片库失败（将于下次同步自愈）: %v", err)
	}
	return interactionDTO(&interaction), nil
}

// SyncFeedback projects phone-feed state into the main library. The liked tag
// is fully owned by this projection and is reconciled both ways. Favorites
// project exactly once per phone-side favorite action (tracked by
// favorite_synced_to_library): a feed un-favorite never erases a favorite set
// manually in the main library, and a manual un-favorite in the main library
// is never overwritten by a later sync of the same phone-side action.
func (s *ShortFeedService) SyncFeedback() (ShortFeedFeedbackSyncResult, error) {
	result := ShortFeedFeedbackSyncResult{}
	if database.DB == nil {
		return result, errors.New("database is not initialized")
	}
	var settings models.Settings
	if err := database.DB.Select("short_feed_feedback_sync_enabled").First(&settings).Error; err != nil {
		return result, err
	}
	result.Enabled = settings.ShortFeedFeedbackSyncEnabled
	if !result.Enabled {
		return result, nil
	}

	err := database.Transaction(func(tx *gorm.DB) error {
		tag, err := ensureShortFeedLikedAutomaticTag(tx)
		if err != nil {
			return err
		}
		result.TagID = tag.ID

		inserted := tx.Exec(`
			INSERT INTO video_tags(video_id, tag_id)
			SELECT interactions.video_id, ?
			FROM short_feed_interactions interactions
			JOIN videos ON videos.id = interactions.video_id
			WHERE interactions.liked = ? AND videos.deleted_at IS NULL
			ON CONFLICT DO NOTHING
		`, tag.ID, true)
		if inserted.Error != nil {
			return inserted.Error
		}
		result.LikesAdded = inserted.RowsAffected

		removed := tx.Exec(`
			DELETE FROM video_tags
			WHERE tag_id = ?
			  AND NOT EXISTS (
				SELECT 1
				FROM short_feed_interactions interactions
				JOIN videos ON videos.id = interactions.video_id
				WHERE interactions.video_id = video_tags.video_id
				  AND interactions.liked = ?
				  AND videos.deleted_at IS NULL
			  )
		`, tag.ID, true)
		if removed.Error != nil {
			return removed.Error
		}
		result.LikesRemoved = removed.RowsAffected

		favorited := tx.Model(&models.Video{}).
			Where("is_favorite = ?", false).
			Where("id IN (?)", tx.Model(&models.ShortFeedInteraction{}).Select("video_id").
				Where("favorited = ? AND favorite_synced_to_library = ?", true, false)).
			Update("is_favorite", true)
		if favorited.Error != nil {
			return favorited.Error
		}
		result.FavoritesAdded = favorited.RowsAffected

		// 只把"视频当前已是收藏"的行标记为已同步：并发提交的新收藏若落在
		// 投影 UPDATE 之后，不会被误标，下次同步仍会投影。
		marked := tx.Model(&models.ShortFeedInteraction{}).
			Where("favorited = ? AND favorite_synced_to_library = ?", true, false).
			Where("video_id IN (?)", tx.Model(&models.Video{}).Select("id").Where("is_favorite = ?", true)).
			Update("favorite_synced_to_library", true)
		if marked.Error != nil {
			return marked.Error
		}
		return nil
	})
	return result, err
}

func (s *ShortFeedService) DeleteVideo(videoID uint) error {
	return s.videoService.DeleteVideo(videoID, true)
}

func (s *ShortFeedService) ResolveMedia(videoID uint) (*ShortFeedMedia, error) {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return nil, err
	}
	if !shortFeedEligible(video, s.maxDurationSeconds()) {
		return nil, ErrShortFeedNoEligibleVideos
	}
	info, err := os.Stat(video.Path)
	if err != nil {
		if os.IsNotExist(err) {
			s.markStale(video.ID)
		}
		return nil, err
	}
	if info.IsDir() {
		s.markStale(video.ID)
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
	maxDurationSeconds := s.maxDurationSeconds()
	query := database.DB.Model(&models.Video{}).
		Preload("Tags").
		Where("is_stale = ?", false).
		Where("duration > ? AND duration < ?", 0, maxDurationSeconds).
		Order("id ASC")
	if len(excludeIDs) > 0 {
		query = query.Where("id NOT IN ?", excludeIDs)
	}
	if err := query.Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

func (s *ShortFeedService) filterExistingVideos(videos []models.Video) []models.Video {
	existing := make([]models.Video, 0, len(videos))
	for _, video := range videos {
		if s.videoFileExists(video) {
			existing = append(existing, video)
		}
	}
	return existing
}

func (s *ShortFeedService) videoFileExists(video models.Video) bool {
	info, err := os.Stat(video.Path)
	if err != nil {
		if os.IsNotExist(err) {
			s.markStale(video.ID)
		}
		return false
	}
	if info.IsDir() {
		s.markStale(video.ID)
		return false
	}
	return true
}

func (s *ShortFeedService) markStale(videoID uint) {
	_ = database.DB.Model(&models.Video{}).Where("id = ?", videoID).Update("is_stale", true).Error
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

func (s *ShortFeedService) maxDurationSeconds() float64 {
	if database.DB == nil {
		return defaultShortFeedMaxDurationSeconds
	}
	var settings models.Settings
	if err := database.DB.Select("short_feed_max_duration_minutes").First(&settings).Error; err != nil {
		return defaultShortFeedMaxDurationSeconds
	}
	if settings.ShortFeedMaxDurationMinutes <= 0 {
		return defaultShortFeedMaxDurationSeconds
	}
	return float64(settings.ShortFeedMaxDurationMinutes * 60)
}

func shortFeedEligible(video models.Video, maxDurationSeconds float64) bool {
	if maxDurationSeconds <= 0 {
		maxDurationSeconds = defaultShortFeedMaxDurationSeconds
	}
	return !video.IsStale && video.Duration > 0 && video.Duration < maxDurationSeconds
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
	if tag.AutomaticKind != "" {
		return nil
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
