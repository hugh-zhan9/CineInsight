package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
)

const (
	LibrarySearchModeFile     = "file"
	LibrarySearchModeSubtitle = "subtitle"
	LibrarySortBalanced       = "balanced"
	LibrarySortRatingDesc     = "rating_desc"
	LibrarySortRatingAsc      = "rating_asc"

	LibraryViewAll              = ""
	LibraryViewFavorites        = "favorites"
	LibraryViewLiked            = "liked"
	LibraryViewContinueWatching = "continue_watching"
	LibraryViewUnwatched        = "unwatched"
	LibraryViewWatched          = "watched"
	LibraryViewRecentlyAdded    = "recently_added"
	LibraryViewRecentlyPlayed   = "recently_played"
	LibraryViewUntagged         = "untagged"
	LibraryViewNoSubtitle       = "no_subtitle"
	LibraryViewStale            = "stale"
)

const recentlyAddedWindow = 30 * 24 * time.Hour

// LibraryFilter 描述主片库和随机播放共享的筛选边界。
type LibraryFilter struct {
	SearchMode string   `json:"search_mode"`
	Keyword    string   `json:"keyword"`
	PathPrefix string   `json:"path_prefix"`
	SmartView  string   `json:"smart_view"`
	TagIDs     []uint   `json:"tag_ids"`
	MinSize    int64    `json:"min_size"`
	MaxSize    int64    `json:"max_size"`
	MinHeight  int      `json:"min_height"`
	MaxHeight  int      `json:"max_height"`
	MinRating  *float64 `json:"min_rating"`
	MaxRating  *float64 `json:"max_rating"`
	SortMode   string   `json:"sort_mode"`
}

// LibraryVideoCursor is an opaque stable cursor for SearchLibraryVideoPage.
type LibraryVideoCursor struct {
	SortMode     string   `json:"sort_mode"`
	Score        float64  `json:"score"`
	Size         int64    `json:"size"`
	Rating       *float64 `json:"rating,omitempty"`
	RatingIsNull bool     `json:"rating_is_null"`
	ID           uint     `json:"id"`
}

type LibraryVideoPage struct {
	Videos     []models.Video      `json:"videos"`
	NextCursor *LibraryVideoCursor `json:"next_cursor,omitempty"`
}

// LibraryVideoPageRequest keeps the optional cursor inside a generated DTO so
// frontend callers can omit it instead of passing an untyped null argument.
type LibraryVideoPageRequest struct {
	Filter LibraryFilter       `json:"filter"`
	Cursor *LibraryVideoCursor `json:"cursor,omitempty"`
	Limit  int                 `json:"limit"`
}

// SavedLibraryViewInput 是创建保存视图的输入。
type SavedLibraryViewInput struct {
	Name string `json:"name"`
	LibraryFilter
}

// LibrarySubtitleHit 是当前片库页内某个视频的首个字幕命中。
type LibrarySubtitleHit struct {
	VideoID uint                   `json:"video_id"`
	Segment subtitleparser.Segment `json:"segment"`
}

var validLibraryViews = map[string]struct{}{
	LibraryViewAll: {}, LibraryViewFavorites: {}, LibraryViewLiked: {}, LibraryViewContinueWatching: {},
	LibraryViewUnwatched: {}, LibraryViewWatched: {}, LibraryViewRecentlyAdded: {},
	LibraryViewRecentlyPlayed: {}, LibraryViewUntagged: {}, LibraryViewNoSubtitle: {},
	LibraryViewStale: {},
}

func normalizeLibraryFilter(filter LibraryFilter) (LibraryFilter, error) {
	filter.SearchMode = strings.TrimSpace(filter.SearchMode)
	if filter.SearchMode == "" {
		filter.SearchMode = LibrarySearchModeFile
	}
	if filter.SearchMode != LibrarySearchModeFile && filter.SearchMode != LibrarySearchModeSubtitle {
		return LibraryFilter{}, fmt.Errorf("不支持的搜索模式: %s", filter.SearchMode)
	}
	filter.Keyword = strings.TrimSpace(filter.Keyword)
	filter.PathPrefix = strings.TrimSpace(filter.PathPrefix)
	if filter.PathPrefix != "" {
		filter.PathPrefix = filepath.Clean(filter.PathPrefix)
	}
	filter.SmartView = strings.TrimSpace(filter.SmartView)
	if _, ok := validLibraryViews[filter.SmartView]; !ok {
		return LibraryFilter{}, fmt.Errorf("不支持的智能视图: %s", filter.SmartView)
	}
	filter.TagIDs = uniqueUintIDs(filter.TagIDs)
	sort.Slice(filter.TagIDs, func(i, j int) bool { return filter.TagIDs[i] < filter.TagIDs[j] })
	if filter.MinSize < 0 || filter.MaxSize < 0 || filter.MinHeight < 0 || filter.MaxHeight < 0 {
		return LibraryFilter{}, fmt.Errorf("筛选范围不能为负数")
	}
	if filter.MaxSize > 0 && filter.MinSize >= filter.MaxSize {
		return LibraryFilter{}, fmt.Errorf("体积筛选上限必须大于下限")
	}
	if filter.MaxHeight > 0 && filter.MinHeight > filter.MaxHeight {
		return LibraryFilter{}, fmt.Errorf("分辨率筛选上限不能小于下限")
	}
	filter.SortMode = strings.TrimSpace(filter.SortMode)
	if filter.SortMode == "" {
		filter.SortMode = LibrarySortBalanced
	}
	if filter.SortMode != LibrarySortBalanced && filter.SortMode != LibrarySortRatingDesc && filter.SortMode != LibrarySortRatingAsc {
		return LibraryFilter{}, fmt.Errorf("不支持的排序模式: %s", filter.SortMode)
	}
	if err := validateRatingValue(filter.MinRating); err != nil {
		return LibraryFilter{}, fmt.Errorf("最低评分无效: %w", err)
	}
	if err := validateRatingValue(filter.MaxRating); err != nil {
		return LibraryFilter{}, fmt.Errorf("最高评分无效: %w", err)
	}
	if filter.MinRating != nil && filter.MaxRating != nil && *filter.MinRating > *filter.MaxRating {
		return LibraryFilter{}, fmt.Errorf("评分筛选上限不能小于下限")
	}
	return filter, nil
}

func libraryFilterNeedsSubtitleSync(filter LibraryFilter) bool {
	return strings.TrimSpace(filter.SmartView) == LibraryViewNoSubtitle ||
		(strings.TrimSpace(filter.SearchMode) == LibrarySearchModeSubtitle && strings.TrimSpace(filter.Keyword) != "")
}

// applyScanRootScope 把查询限制在当前配置的扫描根之内。改窄或删掉扫描目录后，
// 落在范围外的旧记录不再进入任何片库视图；记录本身留着，路径重新被纳入某个根
// 就会自动重新出现。一个扫描目录都没配置时不做裁剪——此时没有"范围"可言。
// loadScanRootScope 读出当前配置的扫描根。空集合表示"没有范围"，调用方一律
// 理解成不裁剪——此时把整库藏起来比留着更糟。
func loadScanRootScope() ([]string, error) {
	var dirs []models.ScanDirectory
	if err := database.DB.Find(&dirs).Error; err != nil {
		return nil, fmt.Errorf("加载扫描目录失败: %w", err)
	}
	return cleanScanRoots(dirs), nil
}

// pathWithinScanRoots 是 applyScanRootScope 的 Go 侧对照实现，给那些不是直接查
// videos 表的入口用（清理分析的感知哈希表、同源关系表）。roots 为空同样表示不裁剪。
func pathWithinScanRoots(path string, roots []string) bool {
	if len(roots) == 0 {
		return true
	}
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, scanRootChildPrefix(root)) {
			return true
		}
	}
	return false
}

// cleanupPathScope 是清理候选统一的路径口径：扫描根之内、且不在扫描黑名单里。
// 黑名单目录里的旧记录在片库里可能还看得见（扫描只是不再进去），但拿它们来做
// "留哪个删哪个"的决定没有意义——用户已经声明不管这一片了。
type cleanupPathScope struct {
	roots    []string
	excluded []string
}

func loadCleanupPathScope() (cleanupPathScope, error) {
	roots, err := loadScanRootScope()
	if err != nil {
		return cleanupPathScope{}, err
	}
	var settings models.Settings
	err = database.DB.Select("scan_exclude_paths").First(&settings).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return cleanupPathScope{}, fmt.Errorf("加载扫描黑名单失败: %w", err)
	}
	return cleanupPathScope{roots: roots, excluded: parseScanExcludePaths(settings.ScanExcludePaths)}, nil
}

func (scope cleanupPathScope) contains(path string) bool {
	return pathWithinScanRoots(path, scope.roots) && !isScanPathExcluded(path, scope.excluded)
}

func applyScanRootScope(query *gorm.DB) (*gorm.DB, error) {
	roots, err := loadScanRootScope()
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return query, nil
	}
	conditions := database.DB.Session(&gorm.Session{NewDB: true})
	for index, root := range roots {
		prefix := escapeSQLLikePrefix(scanRootChildPrefix(root)) + "%"
		clause := database.DB.Session(&gorm.Session{NewDB: true}).
			Where("videos.directory = ?", root).
			Or(`videos.directory LIKE ? ESCAPE '\'`, prefix).
			Or(`videos.path LIKE ? ESCAPE '\'`, prefix)
		if index == 0 {
			conditions = conditions.Where(clause)
			continue
		}
		conditions = conditions.Or(clause)
	}
	return query.Where(conditions), nil
}

func applyLibraryFilter(query *gorm.DB, filter LibraryFilter, now time.Time) (*gorm.DB, error) {
	normalized, err := normalizeLibraryFilter(filter)
	if err != nil {
		return nil, err
	}
	filter = normalized

	query, err = applyScanRootScope(query)
	if err != nil {
		return nil, err
	}

	if filter.Keyword != "" {
		pattern := "%" + strings.ToLower(escapeSQLLike(filter.Keyword)) + "%"
		if filter.SearchMode == LibrarySearchModeSubtitle {
			query = query.Where(`EXISTS (
				SELECT 1 FROM subtitle_segments
				WHERE subtitle_segments.video_id = videos.id
				  AND LOWER(subtitle_segments.text) LIKE ? ESCAPE '\'
			)`, pattern)
		} else {
			query = query.Where("(LOWER(videos.display_title) LIKE ? ESCAPE '\\' OR LOWER(videos.original_title) LIKE ? ESCAPE '\\' OR LOWER(videos.name) LIKE ? ESCAPE '\\' OR LOWER(videos.path) LIKE ? ESCAPE '\\')", pattern, pattern, pattern, pattern)
		}
	}
	if filter.PathPrefix != "" {
		prefix := strings.ToLower(filter.PathPrefix)
		childPattern := strings.ToLower(escapeSQLLike(prefix+string(os.PathSeparator))) + "%"
		query = query.Where("(LOWER(videos.path) = ? OR LOWER(videos.path) LIKE ? ESCAPE '\\')", prefix, childPattern)
	}
	if filter.MinSize > 0 {
		query = query.Where("videos.size >= ?", filter.MinSize)
	}
	if filter.MaxSize > 0 {
		query = query.Where("videos.size < ?", filter.MaxSize)
	}
	if filter.MinHeight > 0 {
		query = query.Where("videos.height >= ?", filter.MinHeight)
	}
	if filter.MaxHeight > 0 {
		query = query.Where("videos.height <= ?", filter.MaxHeight)
	}
	if filter.MinRating != nil {
		query = query.Where("videos.personal_rating >= ?", *filter.MinRating)
	}
	if filter.MaxRating != nil {
		query = query.Where("videos.personal_rating <= ?", *filter.MaxRating)
	}
	if len(filter.TagIDs) > 0 {
		subquery := database.DB.Table("video_tags").Select("video_id").
			Where("tag_id IN ?", filter.TagIDs).
			Group("video_id").
			Having("COUNT(DISTINCT tag_id) = ?", len(filter.TagIDs))
		query = query.Where("videos.id IN (?)", subquery)
	}

	switch filter.SmartView {
	case LibraryViewFavorites:
		query = query.Where("videos.is_favorite = ?", true)
	case LibraryViewLiked:
		query = query.Where("videos.is_liked = ?", true)
	case LibraryViewContinueWatching:
		query = query.Where("videos.is_watched = ? AND videos.watch_position_seconds > 0", false)
	case LibraryViewUnwatched:
		query = query.Where("videos.is_watched = ?", false)
	case LibraryViewWatched:
		query = query.Where("videos.is_watched = ?", true)
	case LibraryViewRecentlyAdded:
		query = query.Where("videos.created_at >= ?", now.Add(-recentlyAddedWindow))
	case LibraryViewRecentlyPlayed:
		query = query.Where("videos.last_played_at IS NOT NULL")
	case LibraryViewUntagged:
		query = query.Where("NOT EXISTS (SELECT 1 FROM video_tags WHERE video_tags.video_id = videos.id)")
	case LibraryViewNoSubtitle:
		query = query.Where("NOT EXISTS (SELECT 1 FROM subtitle_index_states WHERE subtitle_index_states.video_id = videos.id AND subtitle_index_states.segment_count > 0)")
	case LibraryViewStale:
		query = query.Where("videos.is_stale = ?", true)
	}

	// 失效记录只在「路径失效」视图里露面（D-S02）。它们当前指不到文件：留在默认
	// 列表里只会让人对着一条点开就失败的记录发愣，而随机播放共用这条筛选边界，
	// 抽中它等于白抽一次。删掉扫描目录后那批记录也是靠这一条从列表里消失的。
	if filter.SmartView != LibraryViewStale {
		query = query.Where("videos.is_stale = ?", false)
	}
	return query, nil
}

// SetVideoFavorite 更新主片库收藏状态。
func (s *VideoService) SetVideoFavorite(videoID uint, favorite bool) (*models.Video, error) {
	if videoID == 0 {
		return nil, fmt.Errorf("视频 ID 不能为空")
	}
	result := database.DB.Model(&models.Video{}).Where("id = ?", videoID).Update("is_favorite", favorite)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	return s.getVideoWithTags(videoID)
}

// getVideoWithTags 给状态切换接口回传完整行。前端拿返回值整行覆盖列表项，
// 不带 tags 的话一次收藏就会把行上的标签"清空"——库里其实还在，只是被 null 盖掉了。
func (s *VideoService) getVideoWithTags(videoID uint) (*models.Video, error) {
	var video models.Video
	if err := database.DB.Preload("Tags").First(&video, videoID).Error; err != nil {
		return nil, err
	}
	return &video, nil
}

// SetVideoWatched 更新主片库已看状态。
func (s *VideoService) SetVideoWatched(videoID uint, watched bool) (*models.Video, error) {
	if videoID == 0 {
		return nil, fmt.Errorf("视频 ID 不能为空")
	}
	updates := map[string]interface{}{"is_watched": watched}
	if watched {
		now := time.Now()
		updates["watched_at"] = &now
	} else {
		updates["watched_at"] = nil
	}
	result := database.DB.Model(&models.Video{}).Where("id = ?", videoID).Updates(updates)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	return s.getVideoWithTags(videoID)
}

// UpdateVideoWatchProgress 保存内嵌播放器观看位置。
func (s *VideoService) UpdateVideoWatchProgress(videoID uint, positionSeconds float64, completed bool) (*models.Video, error) {
	if videoID == 0 {
		return nil, fmt.Errorf("视频 ID 不能为空")
	}
	if math.IsNaN(positionSeconds) || math.IsInf(positionSeconds, 0) || positionSeconds < 0 {
		return nil, fmt.Errorf("观看位置无效")
	}
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return nil, err
	}
	if video.Duration > 0 && positionSeconds > video.Duration {
		positionSeconds = video.Duration
	}
	now := time.Now()
	updates := map[string]interface{}{
		"watch_position_seconds":    positionSeconds,
		"watch_progress_updated_at": &now,
	}
	if completed {
		updates["is_watched"] = true
		updates["watched_at"] = &now
		if video.Duration > 0 {
			updates["watch_position_seconds"] = video.Duration
		}
	}
	if err := database.DB.Model(&video).Updates(updates).Error; err != nil {
		return nil, err
	}
	return s.getVideoWithTags(videoID)
}

// ListSavedLibraryViews 返回所有活跃保存视图。
func (s *VideoService) ListSavedLibraryViews() ([]models.SavedLibraryView, error) {
	var views []models.SavedLibraryView
	err := database.DB.Order("LOWER(name) ASC, id ASC").Find(&views).Error
	return views, err
}

// SaveLibraryView 创建命名保存视图。
func (s *VideoService) SaveLibraryView(input SavedLibraryViewInput) (*models.SavedLibraryView, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, fmt.Errorf("视图名称不能为空")
	}
	if len([]rune(name)) > 80 {
		return nil, fmt.Errorf("视图名称不能超过 80 个字符")
	}
	filter, err := normalizeLibraryFilter(input.LibraryFilter)
	if err != nil {
		return nil, err
	}
	tagJSON, err := json.Marshal(filter.TagIDs)
	if err != nil {
		return nil, fmt.Errorf("编码标签筛选失败: %w", err)
	}
	view := models.SavedLibraryView{
		Name: name, SearchMode: filter.SearchMode, Keyword: filter.Keyword, SmartView: filter.SmartView,
		TagIDsJSON: string(tagJSON), MinSize: filter.MinSize, MaxSize: filter.MaxSize,
		MinHeight: filter.MinHeight, MaxHeight: filter.MaxHeight,
		MinRating: filter.MinRating, MaxRating: filter.MaxRating, SortMode: filter.SortMode,
	}
	if err := database.DB.Create(&view).Error; err != nil {
		return nil, fmt.Errorf("保存视图失败: %w", err)
	}
	return &view, nil
}

// SearchLibraryVideoPage provides stable pagination for balanced and nullable rating sorts.
// CountLibraryVideos 返回当前筛选命中的视频条数，供片库结果条回显「筛选出 N」。
// 走的是与列表查询同一个 applyLibraryFilter，保证计数和翻完页数出来的条数一致；
// 排序与游标不影响计数，因此这里不复制那部分逻辑。
func (s *VideoService) CountLibraryVideos(filter LibraryFilter) (int64, error) {
	normalized, err := normalizeLibraryFilter(filter)
	if err != nil {
		return 0, err
	}
	if libraryFilterNeedsSubtitleSync(normalized) {
		if err := syncSubtitleIndexesFromFilesystem(); err != nil {
			return 0, err
		}
	}
	query := database.DB.Model(&models.Video{})
	query, err = applyLibraryFilter(query, normalized, time.Now())
	if err != nil {
		return 0, err
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (s *VideoService) SearchLibraryVideoPage(filter LibraryFilter, cursor *LibraryVideoCursor, limit int) (*LibraryVideoPage, error) {
	normalized, err := normalizeLibraryFilter(filter)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if err := validateLibraryVideoCursor(normalized.SortMode, cursor); err != nil {
		return nil, err
	}
	if cursor == nil && libraryFilterNeedsSubtitleSync(normalized) {
		if err := syncSubtitleIndexesFromFilesystem(); err != nil {
			return nil, err
		}
	}

	if normalized.SortMode == LibrarySortBalanced {
		var score float64
		var size int64
		var id uint
		if cursor != nil {
			score, size, id = cursor.Score, cursor.Size, cursor.ID
		}
		videos, err := s.searchLibraryVideos(normalized, score, size, id, limit+1)
		if err != nil {
			return nil, err
		}
		page := &LibraryVideoPage{Videos: videos}
		if len(videos) > limit {
			page.Videos = videos[:limit]
			last := page.Videos[len(page.Videos)-1]
			playWeight, err := s.getPlayWeight()
			if err != nil {
				return nil, err
			}
			page.NextCursor = &LibraryVideoCursor{
				SortMode: LibrarySortBalanced,
				Score:    float64(last.PlayCount)*playWeight + float64(last.RandomPlayCount),
				Size:     last.Size,
				ID:       last.ID,
			}
		}
		return page, nil
	}

	query := database.DB.Model(&models.Video{}).Preload("Tags")
	query, err = applyLibraryFilter(query, normalized, time.Now())
	if err != nil {
		return nil, err
	}
	if cursor != nil {
		if cursor.RatingIsNull {
			query = query.Where("videos.personal_rating IS NULL AND videos.id < ?", cursor.ID)
		} else if normalized.SortMode == LibrarySortRatingDesc {
			query = query.Where("(videos.personal_rating < ? OR (videos.personal_rating = ? AND videos.id < ?) OR videos.personal_rating IS NULL)", *cursor.Rating, *cursor.Rating, cursor.ID)
		} else {
			query = query.Where("(videos.personal_rating > ? OR (videos.personal_rating = ? AND videos.id < ?) OR videos.personal_rating IS NULL)", *cursor.Rating, *cursor.Rating, cursor.ID)
		}
	}
	if normalized.SortMode == LibrarySortRatingDesc {
		query = query.Order("videos.personal_rating DESC NULLS LAST")
	} else {
		query = query.Order("videos.personal_rating ASC NULLS LAST")
	}
	query = query.Order("videos.id DESC")
	var videos []models.Video
	if err := query.Limit(limit + 1).Find(&videos).Error; err != nil {
		return nil, err
	}
	page := &LibraryVideoPage{Videos: videos}
	if len(videos) > limit {
		page.Videos = videos[:limit]
		last := page.Videos[len(page.Videos)-1]
		page.NextCursor = &LibraryVideoCursor{SortMode: normalized.SortMode, RatingIsNull: last.PersonalRating == nil, ID: last.ID}
		if last.PersonalRating != nil {
			rating := *last.PersonalRating
			page.NextCursor.Rating = &rating
		}
	}
	return page, nil
}

func validateLibraryVideoCursor(sortMode string, cursor *LibraryVideoCursor) error {
	if cursor == nil {
		return nil
	}
	if cursor.SortMode != sortMode {
		return errors.New("片库游标排序模式不匹配")
	}
	if cursor.ID == 0 {
		return errors.New("片库游标 ID 无效")
	}
	if sortMode == LibrarySortBalanced {
		if cursor.Rating != nil || cursor.RatingIsNull {
			return errors.New("balanced 游标包含评分字段")
		}
		return nil
	}
	if cursor.RatingIsNull {
		if cursor.Rating != nil {
			return errors.New("NULL 评分游标不能包含评分值")
		}
		return nil
	}
	if cursor.Rating == nil {
		return errors.New("评分游标缺少评分值")
	}
	return validateRatingValue(cursor.Rating)
}

// DeleteSavedLibraryView 软删除命名保存视图。
func (s *VideoService) DeleteSavedLibraryView(viewID uint) error {
	if viewID == 0 {
		return fmt.Errorf("视图 ID 不能为空")
	}
	result := database.DB.Delete(&models.SavedLibraryView{}, viewID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SearchLibraryVideos 使用片库共享过滤器并保持现有游标排序。
func (s *VideoService) SearchLibraryVideos(filter LibraryFilter, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if cursorScore == 0 && cursorSize == 0 && cursorID == 0 && libraryFilterNeedsSubtitleSync(filter) {
		if err := syncSubtitleIndexesFromFilesystem(); err != nil {
			return nil, err
		}
	}
	return s.searchLibraryVideos(filter, cursorScore, cursorSize, cursorID, limit)
}

// ListRecentlyPlayed 返回按最近正式播放时间排序的去重视频历史。
func (s *VideoService) ListRecentlyPlayed(limit int) ([]models.Video, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var videos []models.Video
	err := database.DB.Model(&models.Video{}).Preload("Tags").
		Where("last_played_at IS NOT NULL").
		Order("last_played_at DESC, id DESC").Limit(limit).Find(&videos).Error
	return videos, err
}

// ListRecentlyPlayedWithFilter 按最近播放时间稳定分页，并复用主片库筛选边界。
func (s *VideoService) ListRecentlyPlayedWithFilter(filter LibraryFilter, cursorLastPlayedAt string, cursorID uint, limit int) ([]models.Video, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	cursorLastPlayedAt = strings.TrimSpace(cursorLastPlayedAt)
	if (cursorLastPlayedAt == "") != (cursorID == 0) {
		return nil, fmt.Errorf("最近播放游标不完整")
	}
	var cursorTime time.Time
	if cursorLastPlayedAt != "" {
		var err error
		cursorTime, err = time.Parse(time.RFC3339Nano, cursorLastPlayedAt)
		if err != nil {
			return nil, fmt.Errorf("最近播放时间游标无效: %w", err)
		}
	}
	if cursorLastPlayedAt == "" && libraryFilterNeedsSubtitleSync(filter) {
		if err := syncSubtitleIndexesFromFilesystem(); err != nil {
			return nil, err
		}
	}
	query := database.DB.Model(&models.Video{}).Preload("Tags").Where("videos.last_played_at IS NOT NULL")
	query, err := applyLibraryFilter(query, filter, time.Now())
	if err != nil {
		return nil, err
	}
	if cursorLastPlayedAt != "" {
		query = query.Where("videos.last_played_at < ? OR (videos.last_played_at = ? AND videos.id < ?)", cursorTime, cursorTime, cursorID)
	}
	var videos []models.Video
	err = query.Order("videos.last_played_at DESC, videos.id DESC").Limit(limit).Find(&videos).Error
	return videos, err
}

// GetLibrarySubtitleHits 返回指定当前页视频的首个字幕命中，不改变页面排序。
func (s *VideoService) GetLibrarySubtitleHits(keyword string, videoIDs []uint) ([]LibrarySubtitleHit, error) {
	keyword = strings.TrimSpace(keyword)
	videoIDs = uniqueUintIDs(videoIDs)
	if keyword == "" || len(videoIDs) == 0 {
		return []LibrarySubtitleHit{}, nil
	}
	if len(videoIDs) > 200 {
		return nil, fmt.Errorf("单次字幕命中补充不能超过 200 个视频")
	}
	type firstHit struct {
		VideoID      uint
		SegmentIndex int
	}
	pattern := "%" + strings.ToLower(escapeSQLLike(keyword)) + "%"
	var firstHits []firstHit
	err := database.DB.Model(&models.SubtitleSegment{}).
		Select("video_id, MIN(segment_index) AS segment_index").
		Where("video_id IN ?", videoIDs).
		Where("LOWER(text) LIKE ? ESCAPE '\\'", pattern).
		Group("video_id").Scan(&firstHits).Error
	if err != nil {
		return nil, err
	}
	indexByVideoID := make(map[uint]int, len(firstHits))
	for _, hit := range firstHits {
		indexByVideoID[hit.VideoID] = hit.SegmentIndex
	}
	hits := make([]LibrarySubtitleHit, 0, len(firstHits))
	for _, videoID := range videoIDs {
		segmentIndex, ok := indexByVideoID[videoID]
		if !ok {
			continue
		}
		var indexed models.SubtitleSegment
		if err := database.DB.Where("video_id = ? AND segment_index = ?", videoID, segmentIndex).First(&indexed).Error; err != nil {
			return nil, err
		}
		hits = append(hits, LibrarySubtitleHit{VideoID: videoID, Segment: subtitleparser.Segment{
			Index: indexed.SegmentIndex, StartTimeMs: indexed.StartTimeMs, EndTimeMs: indexed.EndTimeMs,
			Text: indexed.Text, Lines: splitSubtitleLines(indexed.Text),
		}})
	}
	return hits, nil
}

func (s *VideoService) searchLibraryVideos(filter LibraryFilter, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	playWeight, err := s.getPlayWeight()
	if err != nil {
		return nil, err
	}
	// SearchLibraryVideoPage asks for one lookahead row at the public maximum of 200.
	if limit <= 0 || limit > 201 {
		limit = 20
	}
	scoreSQL := scoreExprForTable("videos.", playWeight)
	query := database.DB.Model(&models.Video{}).Preload("Tags")
	query, err = applyLibraryFilter(query, filter, time.Now())
	if err != nil {
		return nil, err
	}
	query = query.Order(scoreSQL + " ASC").Order("videos.size DESC").Order("videos.id DESC")
	query = applyCursorCondition(query, scoreSQL, cursorScore, cursorSize, cursorID, "videos.")
	var videos []models.Video
	err = query.Limit(limit).Find(&videos).Error
	return videos, err
}

// LibraryCounts 是应用头部那行「库 N 视频 · M 图片」需要的两个总数。
// 单独走两条 COUNT，而不是复用洞察聚合——洞察要跑目录、标签、分辨率、热力图
// 等一整套聚合，只为两个数字在启动时跑一遍不划算。
type LibraryCounts struct {
	VideoCount int64 `json:"video_count"`
	ImageCount int64 `json:"image_count"`
}

// SetVideoRating 设置个人评分（0–10 半分制，nil 表示清空），镜像图片侧的
// SetImageRating：只改这一列，不走详情更新的整体覆盖路径。
func (s *VideoService) SetVideoRating(videoID uint, rating *float64) (*models.Video, error) {
	if videoID == 0 {
		return nil, fmt.Errorf("视频 ID 不能为空")
	}
	if err := validateRatingValue(rating); err != nil {
		return nil, err
	}
	result := database.DB.Model(&models.Video{}).Where("id = ?", videoID).Update("personal_rating", rating)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	var video models.Video
	if err := database.DB.Preload("Tags").First(&video, videoID).Error; err != nil {
		return nil, err
	}
	return &video, nil
}

// GetLibraryCounts 返回活跃视频与活跃图片的总数（软删除的不计）。
func (s *VideoService) GetLibraryCounts() (*LibraryCounts, error) {
	counts := &LibraryCounts{}
	// 顶栏这个数必须和片库列表用同一套扫描根裁剪，否则"库 N 视频"会比列表结果条多出
	// 一批范围外的旧记录，两个数字并排显示却对不上。
	videoQuery, err := applyScanRootScope(database.DB.Model(&models.Video{}))
	if err != nil {
		return nil, err
	}
	if err := videoQuery.Count(&counts.VideoCount).Error; err != nil {
		return nil, err
	}
	if err := database.DB.Model(&models.Image{}).Count(&counts.ImageCount).Error; err != nil {
		return nil, err
	}
	return counts, nil
}
