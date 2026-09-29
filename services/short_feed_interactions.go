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

	"gorm.io/gorm"
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
	// UnplayableCount 是该范围里被格式门排除的视频数（手机播不了、也没有代理），面板据此提示。
	UnplayableCount int `json:"unplayable_count"`
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
		// 收藏的唯一数据是 videos/images.is_favorite（D-PC40），与桌面端同源；
		// 互动表里的 favorited 列不再读写。
		var videoIDs []uint
		if err := database.DB.Model(&models.Video{}).
			Where("is_favorite = ?", true).Pluck("id", &videoIDs).Error; err != nil {
			return nil, err
		}
		for _, id := range videoIDs {
			facts.favoritedVideos[id] = true
		}
		var imageIDs []uint
		if err := database.DB.Model(&models.Image{}).
			Where("is_favorite = ?", true).Pluck("id", &imageIDs).Error; err != nil {
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
	snapshot, err := s.cachedSnapshot()
	if err != nil {
		return nil, err
	}
	all := filterCandidatesByMedia(snapshot.candidates, normalizedMedia)
	unplayable := snapshot.unplayable
	if normalizedMedia == ShortFeedMediaFilterImage {
		unplayable = nil
	}
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
		unplayableCount := 0
		for _, candidate := range unplayable {
			if scope == ShortFeedScopeAll || facts.matches(scope, candidate, now) {
				unplayableCount++
			}
		}
		counts = append(counts, ShortFeedScopeCount{Scope: scope, Name: shortFeedScopeNames[scope], Count: count, UnplayableCount: unplayableCount})
	}
	return counts, nil
}

// SetRating 设置个人评分（0–10 半分制，nil 表示清空），视频与图片共用同一套校验。
func (s *ShortFeedService) SetRating(ref ShortFeedMediaRef, rating *float64) (*ShortFeedItemDTO, error) {
	// 先过可见边界：不可见（黑名单、扫描根之外、失效）一律按不存在处理，什么都不写。
	if err := s.ensureItemVisible(ref); err != nil {
		return nil, err
	}
	if err := validateRatingValue(rating); err != nil {
		return nil, fmt.Errorf("%w: %v", errShortFeedInvalidRating, err)
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
	if err := s.ensureItemVisible(ref); err != nil {
		return nil, err
	}
	if _, err := s.videoService.SetVideoWatched(ref.ID, watched); err != nil {
		return nil, err
	}
	return s.reloadItem(ref)
}

// SetItemTag 挂上或摘掉一个标签。自动标签由应用维护，底层服务会拒绝。
func (s *ShortFeedService) SetItemTag(ref ShortFeedMediaRef, tagID uint, attached bool) (*ShortFeedItemDTO, error) {
	if err := s.ensureItemVisible(ref); err != nil {
		return nil, err
	}
	if tagID == 0 {
		return nil, errShortFeedInvalidTag
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
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			// 自动标签由应用维护，底层用不带类型的错误拒绝；这里把「拒绝」和「数据库故障」分开。
			var tag models.Tag
			if lookupErr := database.DB.First(&tag, tagID).Error; lookupErr == nil && tag.AutomaticKind != "" {
				return nil, fmt.Errorf("%w: %v", errShortFeedInvalidTag, err)
			}
		}
		return nil, err
	}
	return s.reloadItem(ref)
}

// ListFeedTags 返回手机端标签面板的标签；keyword 非空时按名字子串（不区分大小写）实时筛选。
// 自动标签也返回但标记 Automatic：面板只读展示、不给手动增删，这样手机端与桌面端看到的
// 标签集合一致。automatic_kind 是后加的可空列，老标签这一列是 NULL，扫成 Go 零值即视为手工，
// 不再用 automatic_kind = ” 过滤（那会把整批老标签漏掉）。
func (s *ShortFeedService) ListFeedTags(keyword string) ([]ShortFeedTagDTO, error) {
	query := database.DB.Order("name ASC")
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		query = query.Where(`LOWER(name) LIKE ? ESCAPE '\'`, "%"+strings.ToLower(escapeSQLLike(keyword))+"%")
	}
	var tags []models.Tag
	if err := query.Find(&tags).Error; err != nil {
		return nil, err
	}
	result := make([]ShortFeedTagDTO, 0, len(tags))
	for _, tag := range tags {
		result = append(result, ShortFeedTagDTO{ID: tag.ID, Name: tag.Name, Color: tag.Color, Automatic: tag.AutomaticKind != ""})
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

// RestoreDeleted 撤销手机端刚才的删除。
//
// 回收站条目按 id 一一对应，但手机端不是回收站的管理入口：只能撤销**本服务通过手机端
// 删除的**条目，而且原条目此刻仍在可见边界内（扫描根之内、不在黑名单）。其余一律按
// 不存在处理，不能借「撤销」去恢复桌面端或扫描器放进回收站的东西。
func (s *ShortFeedService) RestoreDeleted(ref ShortFeedMediaRef) error {
	entryID, ok := s.mobileDeletedEntry(ref)
	if !ok {
		return gorm.ErrRecordNotFound
	}
	switch ref.Kind {
	case ShortFeedMediaVideo:
		var entry models.VideoTrashEntry
		if err := database.DB.Where("id = ? AND video_id = ?", entryID, ref.ID).First(&entry).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				s.forgetMobileDeleted(ref)
			}
			return err
		}
		scope, err := loadCleanupPathScope()
		if err != nil {
			return err
		}
		if !scope.contains(entry.OriginalPath) {
			return gorm.ErrRecordNotFound
		}
		if _, err := s.videoService.RestoreTrashEntry(entry.ID); err != nil {
			return err
		}
	case ShortFeedMediaImage:
		var entry models.ImageTrashEntry
		if err := database.DB.Where("id = ? AND image_id = ?", entryID, ref.ID).First(&entry).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				s.forgetMobileDeleted(ref)
			}
			return err
		}
		excluded, err := imageScanExcludedPaths(database.DB)
		if err != nil {
			return err
		}
		if isScanPathExcluded(entry.OriginalPath, excluded) {
			return gorm.ErrRecordNotFound
		}
		if _, err := NewImageService().RestoreImageTrashEntry(entry.ID); err != nil {
			return err
		}
	default:
		return ErrShortFeedUnsupportedMedia
	}
	s.forgetMobileDeleted(ref)
	s.invalidateCandidates()
	return nil
}

// shortFeedMobileDeletedCap 是「本服务通过手机端删除」记录的容量：只为撤销服务，
// 超出后丢最旧的（更旧的删除只能回桌面端的回收站处理）。
const shortFeedMobileDeletedCap = 256

// rememberMobileDeleted 在手机端删除成功后记下对应的回收站条目。
func (s *ShortFeedService) rememberMobileDeleted(ref ShortFeedMediaRef) {
	var entryID uint
	switch ref.Kind {
	case ShortFeedMediaVideo:
		var entry models.VideoTrashEntry
		if err := database.DB.Select("id").Where("video_id = ?", ref.ID).First(&entry).Error; err != nil {
			return
		}
		entryID = entry.ID
	case ShortFeedMediaImage:
		var entry models.ImageTrashEntry
		if err := database.DB.Select("id").Where("image_id = ?", ref.ID).First(&entry).Error; err != nil {
			return
		}
		entryID = entry.ID
	default:
		return
	}
	s.mobileDeletedMu.Lock()
	defer s.mobileDeletedMu.Unlock()
	if s.mobileDeleted == nil {
		s.mobileDeleted = map[ShortFeedMediaRef]uint{}
	}
	if _, exists := s.mobileDeleted[ref]; !exists {
		s.mobileDeletedOrder = append(s.mobileDeletedOrder, ref)
	}
	s.mobileDeleted[ref] = entryID
	for len(s.mobileDeletedOrder) > shortFeedMobileDeletedCap {
		delete(s.mobileDeleted, s.mobileDeletedOrder[0])
		s.mobileDeletedOrder = s.mobileDeletedOrder[1:]
	}
}

func (s *ShortFeedService) mobileDeletedEntry(ref ShortFeedMediaRef) (uint, bool) {
	s.mobileDeletedMu.Lock()
	defer s.mobileDeletedMu.Unlock()
	id, ok := s.mobileDeleted[ref]
	return id, ok
}

func (s *ShortFeedService) forgetMobileDeleted(ref ShortFeedMediaRef) {
	s.mobileDeletedMu.Lock()
	defer s.mobileDeletedMu.Unlock()
	if _, ok := s.mobileDeleted[ref]; !ok {
		return
	}
	delete(s.mobileDeleted, ref)
	for i, queued := range s.mobileDeletedOrder {
		if queued == ref {
			s.mobileDeletedOrder = append(s.mobileDeletedOrder[:i:i], s.mobileDeletedOrder[i+1:]...)
			break
		}
	}
}

// ensureItemVisible 是所有「按 ID 写」入口的公共门：条目必须在手机端可见边界内
// （视频：扫描根 + 黑名单 + 未失效 + 时长门槛；图片：未失效 + 黑名单 + 有解码器）。
// 不可见与不存在同样返回 gorm.ErrRecordNotFound，响应因此不会带出隐藏条目的任何字段。
func (s *ShortFeedService) ensureItemVisible(ref ShortFeedMediaRef) error {
	var err error
	switch ref.Kind {
	case ShortFeedMediaVideo:
		_, err = s.loadEligibleVideo(ref.ID)
	case ShortFeedMediaImage:
		_, err = s.loadEligibleImage(database.DB, ref.ID)
	default:
		return ErrShortFeedUnsupportedMedia
	}
	if errors.Is(err, ErrShortFeedNoEligibleVideos) {
		return gorm.ErrRecordNotFound
	}
	return err
}

// reloadItem 把改动后的这一条重新组装成手机端的 DTO，让前端不必猜写入结果。
// 组装前同样过可见边界：写入之后条目被隐藏的话，返回的是 404 而不是它的字段。
func (s *ShortFeedService) reloadItem(ref ShortFeedMediaRef) (*ShortFeedItemDTO, error) {
	if err := s.ensureItemVisible(ref); err != nil {
		return nil, err
	}
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

var (
	// errShortFeedInvalidRating / errShortFeedInvalidTag 让 HTTP 层给出固定文案的 400，
	// 而不是回传底层错误原文。
	errShortFeedInvalidRating = errors.New("invalid rating")
	errShortFeedInvalidTag    = errors.New("invalid tag")
)
