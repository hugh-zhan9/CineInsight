package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrShortFeedNoEligibleVideos = errors.New("no eligible short-feed videos")

// ErrShortFeedUnsupportedMedia 表示这个媒体类型在当前构建下还没有实现。
var ErrShortFeedUnsupportedMedia = errors.New("unsupported short-feed media kind")

// shortFeedInlineImageMaxBytes 是直接把原图发给手机的体积上限。超过它就发降采样后的
// JPEG：手机屏幕用不上原始分辨率，而几十 MB 走 WiFi 要好几秒。
const shortFeedInlineImageMaxBytes int64 = 3 << 20

// shortFeedCandidateTTL 是候选快照的有效期。Feed 本身就是近似的随机抽取，
// 没必要每划一次就把整库重读一遍。删除等会主动让它失效。
const shortFeedCandidateTTL = 30 * time.Second

// shortFeedMissingRetries 是选中项文件缺失后的重试次数：只 stat 被选中的那一条，
// 而不是先把整库 stat 一遍。
const shortFeedMissingRetries = 8

type ShortFeedMedia struct {
	Path        string
	DisplayName string
	MIME        string
	ModTime     time.Time
}

type ShortFeedService struct {
	videoService *VideoService
	// imageThumbnail 复用桌面端的图片解码矩阵与缓存：RAW/HEIC 经 sips 转成 JPEG
	// 后才可在浏览器显示，因此"能不能显示"这件事由它裁定，而不是另立一张白名单。
	// 为 nil 时图片一律不入选（测试与降级路径）。
	imageThumbnail *ImageThumbnailService
	now            func() time.Time
	randFloat64    func() float64
	// statFile 可注入，测试用它统计"一次抽取到底 stat 了几个文件"。
	statFile func(string) (os.FileInfo, error)

	candidateMu   sync.Mutex
	candidates    []shortFeedCandidate
	candidateHint *models.Video
	candidatesAt  time.Time
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
		statFile:     os.Stat,
	}
}

// invalidateCandidates 让候选快照立即失效（删除等改变可选集合的操作后调用）。
func (s *ShortFeedService) invalidateCandidates() {
	s.candidateMu.Lock()
	s.candidates = nil
	s.candidateHint = nil
	s.candidatesAt = time.Time{}
	s.candidateMu.Unlock()
}

// SetImageThumbnailService 注入图片解码/缓存服务。app 层在构造完
// ImageThumbnailService 之后调用；未注入时图片不参与 feed。
func (s *ShortFeedService) SetImageThumbnailService(service *ImageThumbnailService) {
	s.imageThumbnail = service
}

// shortFeedImageEligible 图片入选判据。图片没有时长，所以视频那条时长门槛
// 在这里不适用；判据是"未失效 + 有可用解码器"，文件是否存在由调用方另行 stat。
func (s *ShortFeedService) shortFeedImageEligible(img models.Image) bool {
	if s.imageThumbnail == nil || img.IsStale {
		return false
	}
	return imageDecoderForFormatAndPath(img.Format, img.Path) != imageDecoderUnsupported
}

// loadEligibleImages 读取可入选的图片。与视频侧对称：稳定按 id 升序，预载标签。
func (s *ShortFeedService) loadEligibleImages(excludeIDs []uint) ([]models.Image, error) {
	if s.imageThumbnail == nil {
		return nil, nil
	}
	var images []models.Image
	query := database.DB.Model(&models.Image{}).
		Preload("Tags").
		Where("is_stale = ?", false).
		Order("id ASC")
	if len(excludeIDs) > 0 {
		query = query.Where("id NOT IN ?", excludeIDs)
	}
	if err := query.Find(&images).Error; err != nil {
		return nil, err
	}
	eligible := make([]models.Image, 0, len(images))
	for _, img := range images {
		if s.shortFeedImageEligible(img) {
			eligible = append(eligible, img)
		}
	}
	return eligible, nil
}

// resolveImageMedia 解析图片的可下发字节。view=true 取适配大图（RAW/HEIC 会被
// 转成 JPEG 缓存），否则取缩略图。
func (s *ShortFeedService) resolveImageMedia(imageID uint, view bool) (*ShortFeedMedia, error) {
	if s.imageThumbnail == nil {
		return nil, ErrShortFeedUnsupportedMedia
	}
	var img models.Image
	if err := database.DB.First(&img, imageID).Error; err != nil {
		return nil, err
	}
	if !s.shortFeedImageEligible(img) {
		return nil, ErrShortFeedNoEligibleVideos
	}
	ctx := context.Background()
	var media *ImageMedia
	var err error
	if view {
		media, err = s.imageThumbnail.ResolveImageFeedView(ctx, imageID, shortFeedInlineImageMaxBytes)
	} else {
		media, err = s.imageThumbnail.ResolveImageThumbnail(ctx, imageID)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.markImageStale(imageID)
		}
		return nil, err
	}
	mime := media.MIME
	if !view || mime == "" {
		// 缩略图恒为 JPEG；大图分支缺 MIME 时同样按 JPEG 缓存处理。
		mime = "image/jpeg"
	}
	return &ShortFeedMedia{
		Path:        media.Path,
		DisplayName: img.Name,
		MIME:        mime,
		ModTime:     media.ModTime,
	}, nil
}

func (s *ShortFeedService) markImageStale(imageID uint) {
	_ = database.DB.Model(&models.Image{}).Where("id = ?", imageID).Update("is_stale", true).Error
}

// shortFeedCandidate 是抽取阶段的统一候选：两种媒体在这一层不再有分支。
type shortFeedCandidate struct {
	ref   ShortFeedMediaRef
	tags  []models.Tag
	video *models.Video
	image *models.Image
}

// NextItem 抽取下一条内容。exclude 是客户端最近看过的类型化标识，
// 混编流里图片与视频按同一套标签偏好加权后随机抽一条。
func (s *ShortFeedService) NextItem(exclude []ShortFeedMediaRef) (*ShortFeedItemDTO, error) {
	all, unsupportedVideo, err := s.cachedCandidates()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		// 一条能播/能显示的都没有：如果还有存在但不可内联播放的视频，
		// 就把它连同原因一起给出去，让手机端能显示"这个格式放不了"。
		if unsupportedVideo != nil {
			return s.videoDTO(unsupportedVideo, "inline_not_supported", "当前文件格式不适合浏览器内播放。")
		}
		return nil, ErrShortFeedNoEligibleVideos
	}

	pool := shortFeedExcludeCandidates(all, exclude)
	if len(pool) == 0 {
		// 排除集把所有候选都盖住了。小库上这很常见（客户端固定发最近 12 条），
		// 硬报耗尽会让几张图的库刷十几下就停流，所以回退到全量允许重复，
		// 但至少把"最近一条"排掉，避免同一条连着出现两次。
		pool = shortFeedExcludeCandidates(all, lastShortFeedRef(exclude))
		if len(pool) == 0 {
			pool = all
		}
	}

	prefs, err := s.tagPreferenceMap()
	if err != nil {
		return nil, err
	}

	// 只对被选中的那一条做 stat。以前是先把整库每个文件都 stat 一遍再抽签，
	// 外置盘上几千次系统调用会让每一次划动都等上好几秒。
	for attempt := 0; attempt < shortFeedMissingRetries && len(pool) > 0; attempt++ {
		index := s.weightedSelectIndex(pool, prefs)
		selected := pool[index]
		if !s.candidateFileExists(selected) {
			s.invalidateCandidates()
			pool = append(pool[:index:index], pool[index+1:]...)
			continue
		}
		if selected.image != nil {
			return s.imageDTO(selected.image)
		}
		return s.videoDTO(selected.video, "", "")
	}
	return nil, ErrShortFeedNoEligibleVideos
}

// cachedCandidates 返回候选快照。Feed 是近似的随机抽取，没必要每划一次就把整库
// 重读一遍；快照过期或被主动失效后才重建。
func (s *ShortFeedService) cachedCandidates() ([]shortFeedCandidate, *models.Video, error) {
	s.candidateMu.Lock()
	if s.candidates != nil && s.now().Sub(s.candidatesAt) < shortFeedCandidateTTL {
		items, hint := s.candidates, s.candidateHint
		s.candidateMu.Unlock()
		return items, hint, nil
	}
	s.candidateMu.Unlock()

	items, hint, err := s.collectCandidates()
	if err != nil {
		return nil, nil, err
	}
	s.candidateMu.Lock()
	s.candidates, s.candidateHint, s.candidatesAt = items, hint, s.now()
	s.candidateMu.Unlock()
	return items, hint, nil
}

// candidateFileExists 只检查这一条；缺失的顺手标记 stale，下次重建快照时自然排除。
func (s *ShortFeedService) candidateFileExists(candidate shortFeedCandidate) bool {
	path := ""
	if candidate.video != nil {
		path = candidate.video.Path
	} else if candidate.image != nil {
		path = candidate.image.Path
	}
	if path == "" {
		return false
	}
	info, err := s.stat(path)
	if err != nil || info.IsDir() {
		if candidate.video != nil {
			s.markStale(candidate.ref.ID)
		} else {
			s.markImageStale(candidate.ref.ID)
		}
		return false
	}
	return true
}

func (s *ShortFeedService) stat(path string) (os.FileInfo, error) {
	if s.statFile != nil {
		return s.statFile(path)
	}
	return os.Stat(path)
}

// collectCandidates 合并两种媒体的可入选集合。第二个返回值是"存在但不可内联
// 播放"的视频样本，仅在候选为空时用于给出可解释的原因。
func (s *ShortFeedService) collectCandidates() ([]shortFeedCandidate, *models.Video, error) {
	// 这里不做文件存在性检查：抽中之后只 stat 那一条即可。
	existingVideos, err := s.loadEligibleVideos(nil)
	if err != nil {
		return nil, nil, err
	}

	candidates := make([]shortFeedCandidate, 0, len(existingVideos))
	var unsupportedVideo *models.Video
	for i := range existingVideos {
		video := existingVideos[i]
		if _, ok := inlinePreviewMIME(video.Path); !ok {
			if unsupportedVideo == nil {
				unsupportedVideo = &existingVideos[i]
			}
			continue
		}
		candidates = append(candidates, shortFeedCandidate{
			ref:   ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID},
			tags:  video.Tags,
			video: &existingVideos[i],
		})
	}

	images, err := s.loadEligibleImages(nil)
	if err != nil {
		return nil, nil, err
	}
	for i := range images {
		candidates = append(candidates, shortFeedCandidate{
			ref:   ShortFeedMediaRef{Kind: ShortFeedMediaImage, ID: images[i].ID},
			tags:  images[i].Tags,
			image: &images[i],
		})
	}
	return candidates, unsupportedVideo, nil
}

func (s *ShortFeedService) imageFileExists(img models.Image) bool {
	info, err := os.Stat(img.Path)
	if err != nil {
		if os.IsNotExist(err) {
			s.markImageStale(img.ID)
		}
		return false
	}
	if info.IsDir() {
		s.markImageStale(img.ID)
		return false
	}
	return true
}

func shortFeedExcludeCandidates(candidates []shortFeedCandidate, exclude []ShortFeedMediaRef) []shortFeedCandidate {
	if len(exclude) == 0 {
		return candidates
	}
	excluded := make(map[ShortFeedMediaRef]struct{}, len(exclude))
	for _, ref := range exclude {
		excluded[ref] = struct{}{}
	}
	kept := make([]shortFeedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, skip := excluded[candidate.ref]; skip {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
}

func lastShortFeedRef(exclude []ShortFeedMediaRef) []ShortFeedMediaRef {
	if len(exclude) == 0 {
		return nil
	}
	return exclude[len(exclude)-1:]
}

func (s *ShortFeedService) weightedSelectIndex(candidates []shortFeedCandidate, prefs map[uint]float64) int {
	if len(candidates) == 1 {
		return 0
	}
	weights := make([]float64, len(candidates))
	total := 0.0
	for i := range candidates {
		weights[i] = shortFeedTagWeight(candidates[i].tags, prefs)
		total += weights[i]
	}
	if total <= 0 {
		return 0
	}
	draw := s.randFloat64() * total
	cumulative := 0.0
	for i, weight := range weights {
		cumulative += weight
		if draw <= cumulative {
			return i
		}
	}
	return len(candidates) - 1
}

// FavoriteItems 收藏页：两种媒体合并，各自按最近收藏时间倒序后再按时间归并。
func (s *ShortFeedService) FavoriteItems() ([]ShortFeedItemDTO, error) {
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

	result := make([]ShortFeedItemDTO, 0, len(videos))
	existing := s.filterExistingVideos(videos)
	for i := range existing {
		dto, err := s.videoDTO(&existing[i], "", "")
		if err != nil {
			return nil, err
		}
		result = append(result, *dto)
	}

	if s.imageThumbnail != nil {
		var images []models.Image
		err = database.DB.Model(&models.Image{}).
			Preload("Tags").
			Joins("JOIN short_feed_image_interactions ON short_feed_image_interactions.image_id = images.id").
			Where("short_feed_image_interactions.favorited = ?", true).
			Where("images.is_stale = ?", false).
			Order("short_feed_image_interactions.updated_at DESC").
			Find(&images).Error
		if err != nil {
			return nil, err
		}
		for i := range images {
			if !s.shortFeedImageEligible(images[i]) || !s.imageFileExists(images[i]) {
				continue
			}
			dto, err := s.imageDTO(&images[i])
			if err != nil {
				return nil, err
			}
			result = append(result, *dto)
		}
	}
	return result, nil
}

// RecordPlayback 记录一次播放/浏览。
func (s *ShortFeedService) RecordPlayback(ref ShortFeedMediaRef) (*ShortFeedInteractionDTO, error) {
	if ref.Kind == ShortFeedMediaImage {
		return s.recordImageView(ref)
	}
	if ref.Kind != ShortFeedMediaVideo {
		return nil, ErrShortFeedUnsupportedMedia
	}
	videoID := ref.ID
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

func (s *ShortFeedService) SetLiked(ref ShortFeedMediaRef, liked bool) (*ShortFeedInteractionDTO, error) {
	if ref.Kind == ShortFeedMediaImage {
		return s.setImageLiked(ref, liked)
	}
	if ref.Kind != ShortFeedMediaVideo {
		return nil, ErrShortFeedUnsupportedMedia
	}
	videoID := ref.ID
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

func (s *ShortFeedService) SetFavorited(ref ShortFeedMediaRef, favorited bool) (*ShortFeedInteractionDTO, error) {
	if ref.Kind == ShortFeedMediaImage {
		return s.setImageFavorited(ref, favorited)
	}
	if ref.Kind != ShortFeedMediaVideo {
		return nil, ErrShortFeedUnsupportedMedia
	}
	videoID := ref.ID
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

		return syncShortFeedImageFeedback(tx, tag.ID, &result)
	})
	return result, err
}

// syncShortFeedImageFeedback 是图片侧的等价投影：喜欢完全拥有那个自动标签
// （含反向清理），收藏每次动作只投影一次。表名与收藏列换成图片侧的等价物，
// 语义与视频侧逐条对齐。
func syncShortFeedImageFeedback(tx *gorm.DB, tagID uint, result *ShortFeedFeedbackSyncResult) error {
	inserted := tx.Exec(`
		INSERT INTO image_tags(image_id, tag_id)
		SELECT interactions.image_id, ?
		FROM short_feed_image_interactions interactions
		JOIN images ON images.id = interactions.image_id
		WHERE interactions.liked = ? AND images.deleted_at IS NULL
		ON CONFLICT DO NOTHING
	`, tagID, true)
	if inserted.Error != nil {
		return inserted.Error
	}
	result.LikesAdded += inserted.RowsAffected

	removed := tx.Exec(`
		DELETE FROM image_tags
		WHERE tag_id = ?
		  AND NOT EXISTS (
			SELECT 1
			FROM short_feed_image_interactions interactions
			JOIN images ON images.id = interactions.image_id
			WHERE interactions.image_id = image_tags.image_id
			  AND interactions.liked = ?
			  AND images.deleted_at IS NULL
		  )
	`, tagID, true)
	if removed.Error != nil {
		return removed.Error
	}
	result.LikesRemoved += removed.RowsAffected

	favorited := tx.Model(&models.Image{}).
		Where("is_favorite = ?", false).
		Where("id IN (?)", tx.Model(&models.ShortFeedImageInteraction{}).Select("image_id").
			Where("favorited = ? AND favorite_synced_to_library = ?", true, false)).
		Update("is_favorite", true)
	if favorited.Error != nil {
		return favorited.Error
	}
	result.FavoritesAdded += favorited.RowsAffected

	marked := tx.Model(&models.ShortFeedImageInteraction{}).
		Where("favorited = ? AND favorite_synced_to_library = ?", true, false).
		Where("image_id IN (?)", tx.Model(&models.Image{}).Select("id").Where("is_favorite = ?", true)).
		Update("favorite_synced_to_library", true)
	return marked.Error
}

// DeleteItem 把一条内容移入对应媒体的回收站。图片走图片回收站链路，可恢复。
func (s *ShortFeedService) DeleteItem(ref ShortFeedMediaRef) error {
	switch ref.Kind {
	case ShortFeedMediaVideo:
		err := s.videoService.DeleteVideo(ref.ID, true)
		if err == nil {
			s.invalidateCandidates()
		}
		return err
	case ShortFeedMediaImage:
		err := NewImageService().DeleteImage(ref.ID, true)
		if err == nil {
			s.invalidateCandidates()
		}
		return err
	default:
		return ErrShortFeedUnsupportedMedia
	}
}

// recordImageView 图片没有播放计数与 last_played_at 这类视频专属列，
// 只记浏览次数与最近浏览时间，并顺带清掉 stale 标记（文件刚被成功读出）。
func (s *ShortFeedService) recordImageView(ref ShortFeedMediaRef) (*ShortFeedInteractionDTO, error) {
	now := s.now()
	var state shortFeedInteractionState
	err := database.Transaction(func(tx *gorm.DB) error {
		img, err := s.loadEligibleImage(tx, ref.ID)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.Image{}).Where("id = ?", img.ID).Update("is_stale", false).Error; err != nil {
			return err
		}
		state, err = upsertShortFeedInteractionFor(tx, ref, func(row *shortFeedInteractionState) {
			row.ViewCount++
			row.LastViewedAt = &now
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return stateInteractionDTO(ref, state), nil
}

func (s *ShortFeedService) setImageLiked(ref ShortFeedMediaRef, liked bool) (*ShortFeedInteractionDTO, error) {
	now := s.now()
	var state shortFeedInteractionState
	err := database.Transaction(func(tx *gorm.DB) error {
		img, err := s.loadEligibleImage(tx, ref.ID)
		if err != nil {
			return err
		}
		wasLiked := false
		state, err = upsertShortFeedInteractionFor(tx, ref, func(row *shortFeedInteractionState) {
			wasLiked = row.Liked
			row.Liked = liked
			if liked {
				row.LikedAt = &now
			} else {
				row.LikedAt = nil
			}
		})
		if err != nil {
			return err
		}
		// 标签偏好表是图片与视频共用的，因为 tags 表本身就共用。
		if liked && !wasLiked {
			for _, tag := range img.Tags {
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
	if _, err := s.SyncFeedback(); err != nil {
		log.Printf("short-feed: 同步图片喜欢状态到图片库失败（将于下次同步自愈）: %v", err)
	}
	return stateInteractionDTO(ref, state), nil
}

func (s *ShortFeedService) setImageFavorited(ref ShortFeedMediaRef, favorited bool) (*ShortFeedInteractionDTO, error) {
	now := s.now()
	var state shortFeedInteractionState
	err := database.Transaction(func(tx *gorm.DB) error {
		if _, err := s.loadEligibleImage(tx, ref.ID); err != nil {
			return err
		}
		var err error
		state, err = upsertShortFeedInteractionFor(tx, ref, func(row *shortFeedInteractionState) {
			if row.Favorited != favorited {
				row.FavoriteSyncedToLibrary = false
			}
			row.Favorited = favorited
			if favorited {
				row.FavoritedAt = &now
			} else {
				row.FavoritedAt = nil
			}
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.SyncFeedback(); err != nil {
		log.Printf("short-feed: 同步图片收藏状态到图片库失败（将于下次同步自愈）: %v", err)
	}
	return stateInteractionDTO(ref, state), nil
}

// loadEligibleImage 读取并校验图片资格，交互类方法统一走它，避免各处重复判据。
func (s *ShortFeedService) loadEligibleImage(tx *gorm.DB, imageID uint) (*models.Image, error) {
	var img models.Image
	if err := tx.Preload("Tags").First(&img, imageID).Error; err != nil {
		return nil, err
	}
	if !s.shortFeedImageEligible(img) {
		return nil, ErrShortFeedNoEligibleVideos
	}
	return &img, nil
}

// ResolveMedia 解析一条内容的可下发字节。
func (s *ShortFeedService) ResolveMedia(ref ShortFeedMediaRef) (*ShortFeedMedia, error) {
	if ref.Kind == ShortFeedMediaImage {
		return s.resolveImageMedia(ref.ID, true)
	}
	if ref.Kind != ShortFeedMediaVideo {
		return nil, ErrShortFeedUnsupportedMedia
	}
	videoID := ref.ID
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

// ResolveThumbnail 解析缩略图字节。视频暂无缩略图路线，只对图片有效。
func (s *ShortFeedService) ResolveThumbnail(ref ShortFeedMediaRef) (*ShortFeedMedia, error) {
	if ref.Kind != ShortFeedMediaImage {
		return nil, ErrShortFeedUnsupportedMedia
	}
	return s.resolveImageMedia(ref.ID, false)
}

// shortFeedRefIDs 取出指定媒体类型的裸 ID，供仍按单表查询的内部路径使用。
func shortFeedRefIDs(refs []ShortFeedMediaRef, kind ShortFeedMediaKind) []uint {
	ids := make([]uint, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind == kind && ref.ID > 0 {
			ids = append(ids, ref.ID)
		}
	}
	return ids
}

func shortFeedMediaURL(ref ShortFeedMediaRef) string {
	return fmt.Sprintf("/short-media/%s/%d", ref.Kind, ref.ID)
}

func shortFeedWeight(video models.Video, prefs map[uint]float64) float64 {
	return shortFeedTagWeight(video.Tags, prefs)
}

// shortFeedTagWeight 权重只看标签，与媒体类型无关：tags 表本身图片与视频共用。
func shortFeedTagWeight(tags []models.Tag, prefs map[uint]float64) float64 {
	boost := 0.0
	for _, tag := range tags {
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

func (s *ShortFeedService) videoDTO(video *models.Video, reasonCode string, reasonMessage string) (*ShortFeedItemDTO, error) {
	interaction, err := interactionForVideo(video.ID)
	if err != nil {
		return nil, err
	}
	mediaURL := ""
	mediaMIME := ""
	if mimeType, ok := inlinePreviewMIME(video.Path); ok {
		mediaURL = shortFeedMediaURL(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID})
		mediaMIME = mimeType
	} else if reasonCode == "" {
		reasonCode = "inline_not_supported"
		reasonMessage = "当前文件格式不适合浏览器内播放。"
	}

	tags := make([]ShortFeedTagDTO, 0, len(video.Tags))
	for _, tag := range video.Tags {
		tags = append(tags, ShortFeedTagDTO{ID: tag.ID, Name: tag.Name, Color: tag.Color})
	}
	return &ShortFeedItemDTO{
		MediaKind:     ShortFeedMediaVideo,
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

// imageDTO 图片条目。没有时长与进度条，Description 带上已生成的 AI 描述当图说。
func (s *ShortFeedService) imageDTO(img *models.Image) (*ShortFeedItemDTO, error) {
	ref := ShortFeedMediaRef{Kind: ShortFeedMediaImage, ID: img.ID}
	state, err := loadShortFeedInteraction(ref)
	if err != nil {
		return nil, err
	}
	tags := make([]ShortFeedTagDTO, 0, len(img.Tags))
	for _, tag := range img.Tags {
		tags = append(tags, ShortFeedTagDTO{ID: tag.ID, Name: tag.Name, Color: tag.Color})
	}
	description := ""
	var row models.ImageAIDescription
	if err := database.DB.Select("description").
		Where("image_id = ? AND status = ?", img.ID, imageAIDescriptionStatusCompleted).
		First(&row).Error; err == nil {
		description = row.Description
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return &ShortFeedItemDTO{
		MediaKind:   ShortFeedMediaImage,
		ID:          img.ID,
		Name:        img.Name,
		Width:       img.Width,
		Height:      img.Height,
		Tags:        tags,
		MediaURL:    shortFeedMediaURL(ref),
		MediaMIME:   "image/jpeg",
		Description: description,
		Liked:       state.Liked,
		Favorited:   state.Favorited,
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

// shortFeedInteractionState 是两种媒体互动的公共形态。两张并行表形状一致，
// 上层只跟它打交道，媒体类型的分派集中在下面这三个函数里，而不是散落在每个方法。
type shortFeedInteractionState struct {
	Liked                   bool
	Favorited               bool
	FavoriteSyncedToLibrary bool
	ViewCount               int
	LastViewedAt            *time.Time
	LikedAt                 *time.Time
	FavoritedAt             *time.Time
}

func stateFromVideoInteraction(row models.ShortFeedInteraction) shortFeedInteractionState {
	return shortFeedInteractionState{
		Liked: row.Liked, Favorited: row.Favorited, FavoriteSyncedToLibrary: row.FavoriteSyncedToLibrary,
		ViewCount: row.ViewCount, LastViewedAt: row.LastViewedAt, LikedAt: row.LikedAt, FavoritedAt: row.FavoritedAt,
	}
}

func stateFromImageInteraction(row models.ShortFeedImageInteraction) shortFeedInteractionState {
	return shortFeedInteractionState{
		Liked: row.Liked, Favorited: row.Favorited, FavoriteSyncedToLibrary: row.FavoriteSyncedToLibrary,
		ViewCount: row.ViewCount, LastViewedAt: row.LastViewedAt, LikedAt: row.LikedAt, FavoritedAt: row.FavoritedAt,
	}
}

func applyStateToImageInteraction(row *models.ShortFeedImageInteraction, state shortFeedInteractionState) {
	row.Liked = state.Liked
	row.Favorited = state.Favorited
	row.FavoriteSyncedToLibrary = state.FavoriteSyncedToLibrary
	row.ViewCount = state.ViewCount
	row.LastViewedAt = state.LastViewedAt
	row.LikedAt = state.LikedAt
	row.FavoritedAt = state.FavoritedAt
}

// loadShortFeedInteraction 读取互动状态；没有记录时返回零值而不是报错。
func loadShortFeedInteraction(ref ShortFeedMediaRef) (shortFeedInteractionState, error) {
	if ref.Kind == ShortFeedMediaImage {
		var row models.ShortFeedImageInteraction
		err := database.DB.Where("image_id = ?", ref.ID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return shortFeedInteractionState{}, nil
		}
		return stateFromImageInteraction(row), err
	}
	row, err := interactionForVideo(ref.ID)
	return stateFromVideoInteraction(row), err
}

// upsertShortFeedInteractionFor 按媒体类型落到对应的并行表；行锁语义与视频侧一致。
func upsertShortFeedInteractionFor(tx *gorm.DB, ref ShortFeedMediaRef, mutate func(*shortFeedInteractionState)) (shortFeedInteractionState, error) {
	var state shortFeedInteractionState
	if ref.Kind == ShortFeedMediaImage {
		var row models.ShortFeedImageInteraction
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("image_id = ?", ref.ID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = models.ShortFeedImageInteraction{ImageID: ref.ID}
		} else if err != nil {
			return state, err
		}
		state = stateFromImageInteraction(row)
		mutate(&state)
		applyStateToImageInteraction(&row, state)
		if row.ID == 0 {
			return state, tx.Create(&row).Error
		}
		return state, tx.Save(&row).Error
	}
	err := upsertShortFeedInteraction(tx, ref.ID, func(row *models.ShortFeedInteraction) {
		state = stateFromVideoInteraction(*row)
		mutate(&state)
		row.Liked = state.Liked
		row.Favorited = state.Favorited
		row.FavoriteSyncedToLibrary = state.FavoriteSyncedToLibrary
		row.ViewCount = state.ViewCount
		row.LastViewedAt = state.LastViewedAt
		row.LikedAt = state.LikedAt
		row.FavoritedAt = state.FavoritedAt
	})
	return state, err
}

func stateInteractionDTO(ref ShortFeedMediaRef, state shortFeedInteractionState) *ShortFeedInteractionDTO {
	return &ShortFeedInteractionDTO{
		MediaKind:    ref.Kind,
		MediaID:      ref.ID,
		Liked:        state.Liked,
		Favorited:    state.Favorited,
		ViewCount:    state.ViewCount,
		LastViewedAt: state.LastViewedAt,
		LikedAt:      state.LikedAt,
		FavoritedAt:  state.FavoritedAt,
	}
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
		MediaKind:    ShortFeedMediaVideo,
		MediaID:      interaction.VideoID,
		Liked:        interaction.Liked,
		Favorited:    interaction.Favorited,
		ViewCount:    interaction.ViewCount,
		LastViewedAt: interaction.LastViewedAt,
		LikedAt:      interaction.LikedAt,
		FavoritedAt:  interaction.FavoritedAt,
	}
}
