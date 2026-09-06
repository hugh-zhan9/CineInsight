package services

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
)

// 手机端「播放范围」的五个可选值。范围只收窄候选池，不改变加权抽取本身。
const (
	ShortFeedScopeAll       = "all"
	ShortFeedScopeUnwatched = "unwatched"
	ShortFeedScopeFavorites = "favorites"
	ShortFeedScopeRecent    = "recent"
	ShortFeedScopeUntagged  = "untagged"
)

// shortFeedRecentWindow 是「最近添加」的口径，与片库智能视图保持一致。
const shortFeedRecentWindow = 30 * 24 * time.Hour

// ShortFeedScopeCount 是某个播放范围当前的候选条数。
type ShortFeedScopeCount struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

var shortFeedScopeNames = map[string]string{
	ShortFeedScopeAll:       "全部短视频",
	ShortFeedScopeUnwatched: "未看",
	ShortFeedScopeFavorites: "收藏",
	ShortFeedScopeRecent:    "最近添加",
	ShortFeedScopeUntagged:  "未打标签",
}

var shortFeedScopeOrder = []string{
	ShortFeedScopeAll,
	ShortFeedScopeUnwatched,
	ShortFeedScopeFavorites,
	ShortFeedScopeRecent,
	ShortFeedScopeUntagged,
}

// 资源类型筛选：与播放范围正交，只按媒体种类收窄候选池。
const (
	ShortFeedMediaFilterAll   = "all"
	ShortFeedMediaFilterVideo = "video"
	ShortFeedMediaFilterImage = "image"
)

func normalizeShortFeedMediaFilter(kind string) (string, error) {
	switch kind {
	case "", ShortFeedMediaFilterAll:
		return ShortFeedMediaFilterAll, nil
	case ShortFeedMediaFilterVideo, ShortFeedMediaFilterImage:
		return kind, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrShortFeedInvalidMediaFilter, kind)
	}
}

// shortFeedTagNameMaxRunes 与桌面端标签名的常规长度一致；再长的名字在徽标上根本显示不下。
const shortFeedTagNameMaxRunes = 64

func normalizeShortFeedScope(scope string) (string, error) {
	if scope == "" {
		return ShortFeedScopeAll, nil
	}
	if _, ok := shortFeedScopeNames[scope]; !ok {
		return "", fmt.Errorf("不支持的播放范围: %s", scope)
	}
	return scope, nil
}

// shortFeedScopeFacts 是判定范围所需的、候选行上没有的那部分事实。
// 一次性批量取，避免逐条查询。
type shortFeedScopeFacts struct {
	favoritedVideos map[uint]bool
	favoritedImages map[uint]bool
	taggedVideos    map[uint]bool
	taggedImages    map[uint]bool
}

// loadShortFeedScopeFacts 只在需要收藏或未打标签这两个范围时才查。
func (s *ShortFeedService) loadShortFeedScopeFacts(needFavorites, needTags bool) (*shortFeedScopeFacts, error) {
	facts := &shortFeedScopeFacts{
		favoritedVideos: map[uint]bool{},
		favoritedImages: map[uint]bool{},
		taggedVideos:    map[uint]bool{},
		taggedImages:    map[uint]bool{},
	}
	if needFavorites {
		// 手机端的收藏状态以互动表为准：它才是 Feed 自己的收藏，主片库那份是
		// 投影过去的结果。视频与图片各有一张并行表。
		var videoIDs []uint
		if err := database.DB.Model(&models.ShortFeedInteraction{}).
			Where("favorited = ?", true).Pluck("video_id", &videoIDs).Error; err != nil {
			return nil, err
		}
		for _, id := range videoIDs {
			facts.favoritedVideos[id] = true
		}
		var imageIDs []uint
		if err := database.DB.Model(&models.ShortFeedImageInteraction{}).
			Where("favorited = ?", true).Pluck("image_id", &imageIDs).Error; err != nil {
			return nil, err
		}
		for _, id := range imageIDs {
			facts.favoritedImages[id] = true
		}
	}
	if needTags {
		var videoIDs []uint
		if err := database.DB.Table("video_tags").
			Joins("JOIN tags ON tags.id = video_tags.tag_id AND tags.deleted_at IS NULL").
			Distinct("video_tags.video_id").Pluck("video_tags.video_id", &videoIDs).Error; err != nil {
			return nil, err
		}
		for _, id := range videoIDs {
			facts.taggedVideos[id] = true
		}
		var imageIDs []uint
		if err := database.DB.Table("image_tags").
			Joins("JOIN tags ON tags.id = image_tags.tag_id AND tags.deleted_at IS NULL").
			Distinct("image_tags.image_id").Pluck("image_tags.image_id", &imageIDs).Error; err != nil {
			return nil, err
		}
		for _, id := range imageIDs {
			facts.taggedImages[id] = true
		}
	}
	return facts, nil
}

func (f *shortFeedScopeFacts) matches(scope string, candidate shortFeedCandidate, now time.Time) bool {
	switch scope {
	case ShortFeedScopeUnwatched:
		// 图片没有"已看"这个概念，因此不参与未看范围，而不是被当成永远未看。
		return candidate.video != nil && !candidate.video.IsWatched
	case ShortFeedScopeFavorites:
		if candidate.video != nil {
			return f.favoritedVideos[candidate.video.ID]
		}
		return candidate.image != nil && f.favoritedImages[candidate.image.ID]
	case ShortFeedScopeRecent:
		var createdAt time.Time
		if candidate.video != nil {
			createdAt = candidate.video.CreatedAt
		} else if candidate.image != nil {
			createdAt = candidate.image.CreatedAt
		}
		return !createdAt.IsZero() && now.Sub(createdAt) <= shortFeedRecentWindow
	case ShortFeedScopeUntagged:
		if candidate.video != nil {
			return !f.taggedVideos[candidate.video.ID]
		}
		return candidate.image != nil && !f.taggedImages[candidate.image.ID]
	default:
		return true
	}
}

func (s *ShortFeedService) filterCandidatesByScope(all []shortFeedCandidate, scope string) ([]shortFeedCandidate, error) {
	if scope == ShortFeedScopeAll {
		return all, nil
	}
	facts, err := s.loadShortFeedScopeFacts(scope == ShortFeedScopeFavorites, scope == ShortFeedScopeUntagged)
	if err != nil {
		return nil, err
	}
	now := s.now()
	filtered := make([]shortFeedCandidate, 0, len(all))
	for _, candidate := range all {
		if facts.matches(scope, candidate, now) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered, nil
}

// ScopeCounts 返回五个播放范围各自的候选条数，供手机端的范围面板显示。
func (s *ShortFeedService) ScopeCounts() ([]ShortFeedScopeCount, error) {
	return s.ScopeCountsFiltered(ShortFeedMediaFilterAll)
}

// ScopeCountsFiltered 按当前资源类型统计各范围条数：选了「仅图片」还把视频算进去，数字就对不上流。
func (s *ShortFeedService) ScopeCountsFiltered(mediaFilter string) ([]ShortFeedScopeCount, error) {
	normalizedMedia, err := normalizeShortFeedMediaFilter(mediaFilter)
	if err != nil {
		return nil, err
	}
	all, _, err := s.cachedCandidates()
	if err != nil {
		return nil, err
	}
	all = filterCandidatesByMedia(all, normalizedMedia)
	facts, err := s.loadShortFeedScopeFacts(true, true)
	if err != nil {
		return nil, err
	}
	now := s.now()
	counts := make([]ShortFeedScopeCount, 0, len(shortFeedScopeOrder))
	for _, scope := range shortFeedScopeOrder {
		count := 0
		for _, candidate := range all {
			if scope == ShortFeedScopeAll || facts.matches(scope, candidate, now) {
				count++
			}
		}
		counts = append(counts, ShortFeedScopeCount{Scope: scope, Name: shortFeedScopeNames[scope], Count: count})
	}
	return counts, nil
}

// SetRating 设置个人评分（0–10 半分制，nil 表示清空），视频与图片共用同一套校验。
func (s *ShortFeedService) SetRating(ref ShortFeedMediaRef, rating *float64) (*ShortFeedItemDTO, error) {
	if err := validateRatingValue(rating); err != nil {
		return nil, err
	}
	switch ref.Kind {
	case ShortFeedMediaVideo:
		if _, err := s.videoService.SetVideoRating(ref.ID, rating); err != nil {
			return nil, err
		}
	case ShortFeedMediaImage:
		if _, err := NewImageLibraryService().SetImageRating(ref.ID, rating); err != nil {
			return nil, err
		}
	default:
		return nil, ErrShortFeedUnsupportedMedia
	}
	return s.reloadItem(ref)
}

// SetWatched 标记已看/未看。图片没有观看状态，明确拒绝而不是假装成功。
func (s *ShortFeedService) SetWatched(ref ShortFeedMediaRef, watched bool) (*ShortFeedItemDTO, error) {
	if ref.Kind != ShortFeedMediaVideo {
		return nil, ErrShortFeedUnsupportedMedia
	}
	if _, err := s.videoService.SetVideoWatched(ref.ID, watched); err != nil {
		return nil, err
	}
	return s.reloadItem(ref)
}

// SetItemTag 挂上或摘掉一个标签。自动标签由应用维护，底层服务会拒绝。
func (s *ShortFeedService) SetItemTag(ref ShortFeedMediaRef, tagID uint, attached bool) (*ShortFeedItemDTO, error) {
	if tagID == 0 {
		return nil, fmt.Errorf("标签 ID 不能为空")
	}
	var err error
	switch ref.Kind {
	case ShortFeedMediaVideo:
		if attached {
			err = s.videoService.AddTagToVideo(ref.ID, tagID)
		} else {
			err = s.videoService.RemoveTagFromVideo(ref.ID, tagID)
		}
	case ShortFeedMediaImage:
		library := NewImageLibraryService()
		if attached {
			err = library.AddTagToImage(ref.ID, tagID)
		} else {
			err = library.RemoveTagFromImage(ref.ID, tagID)
		}
	default:
		return nil, ErrShortFeedUnsupportedMedia
	}
	if err != nil {
		return nil, err
	}
	return s.reloadItem(ref)
}

// ListFeedTags 返回手机端标签面板可选的标签。自动标签不给手动增删，直接排除。
func (s *ShortFeedService) ListFeedTags() ([]ShortFeedTagDTO, error) {
	var tags []models.Tag
	if err := database.DB.Where("automatic_kind = ?", "").Order("name ASC").Find(&tags).Error; err != nil {
		return nil, err
	}
	result := make([]ShortFeedTagDTO, 0, len(tags))
	for _, tag := range tags {
		result = append(result, ShortFeedTagDTO{ID: tag.ID, Name: tag.Name, Color: tag.Color})
	}
	return result, nil
}

// CreateFeedTag 在手机端新建一个手工标签。同名标签已存在时直接返回那一条：手机上
// 打字容易重名，报错只会逼用户回去搜一遍。名字撞上自动标签则拒绝，与 ListFeedTags 的
// 口径一致。
func (s *ShortFeedService) CreateFeedTag(name string) (ShortFeedTagDTO, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ShortFeedTagDTO{}, ErrShortFeedTagNameRequired
	}
	if utf8.RuneCountInString(name) > shortFeedTagNameMaxRunes {
		return ShortFeedTagDTO{}, ErrShortFeedTagNameTooLong
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return ShortFeedTagDTO{}, ErrShortFeedTagNameInvalid
		}
	}
	tag, err := (&TagService{}).CreateTag(name, "")
	if err != nil && !errors.Is(err, ErrTagExists) {
		return ShortFeedTagDTO{}, err
	}
	if tag.ID == 0 {
		// 两个客户端同时建同名标签时，输掉唯一约束的那一方拿到的是没入库的空壳；按名字把赢家读回来。
		var existing models.Tag
		if err := database.DB.Where("name = ?", name).First(&existing).Error; err != nil {
			return ShortFeedTagDTO{}, err
		}
		tag = &existing
	}
	if tag.AutomaticKind != "" {
		return ShortFeedTagDTO{}, ErrShortFeedAutomaticTag
	}
	return ShortFeedTagDTO{ID: tag.ID, Name: tag.Name, Color: tag.Color}, nil
}

// RestoreDeleted 撤销刚才那一次删除。回收站里每个媒体最多一条记录（video_id /
// image_id 都是唯一索引），所以按 ref 找就是刚删掉的那一条。
func (s *ShortFeedService) RestoreDeleted(ref ShortFeedMediaRef) error {
	switch ref.Kind {
	case ShortFeedMediaVideo:
		var entry models.VideoTrashEntry
		if err := database.DB.Where("video_id = ?", ref.ID).First(&entry).Error; err != nil {
			return err
		}
		if _, err := s.videoService.RestoreTrashEntry(entry.ID); err != nil {
			return err
		}
	case ShortFeedMediaImage:
		var entry models.ImageTrashEntry
		if err := database.DB.Where("image_id = ?", ref.ID).First(&entry).Error; err != nil {
			return err
		}
		if _, err := NewImageService().RestoreImageTrashEntry(entry.ID); err != nil {
			return err
		}
	default:
		return ErrShortFeedUnsupportedMedia
	}
	s.invalidateCandidates()
	return nil
}

// reloadItem 把改动后的这一条重新组装成手机端的 DTO，让前端不必猜写入结果。
func (s *ShortFeedService) reloadItem(ref ShortFeedMediaRef) (*ShortFeedItemDTO, error) {
	switch ref.Kind {
	case ShortFeedMediaVideo:
		var video models.Video
		if err := database.DB.Preload("Tags").First(&video, ref.ID).Error; err != nil {
			return nil, err
		}
		sort.Slice(video.Tags, func(i, j int) bool { return video.Tags[i].Name < video.Tags[j].Name })
		return s.videoDTO(&video, "", "")
	case ShortFeedMediaImage:
		var image models.Image
		if err := database.DB.Preload("Tags").First(&image, ref.ID).Error; err != nil {
			return nil, err
		}
		sort.Slice(image.Tags, func(i, j int) bool { return image.Tags[i].Name < image.Tags[j].Name })
		return s.imageDTO(&image)
	default:
		return nil, ErrShortFeedUnsupportedMedia
	}
}
