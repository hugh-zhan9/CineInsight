package services

import (
	"encoding/json"
	"fmt"
	"math"
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

	LibraryViewAll              = ""
	LibraryViewFavorites        = "favorites"
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
	SearchMode string `json:"search_mode"`
	Keyword    string `json:"keyword"`
	SmartView  string `json:"smart_view"`
	TagIDs     []uint `json:"tag_ids"`
	MinSize    int64  `json:"min_size"`
	MaxSize    int64  `json:"max_size"`
	MinHeight  int    `json:"min_height"`
	MaxHeight  int    `json:"max_height"`
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
	LibraryViewAll: {}, LibraryViewFavorites: {}, LibraryViewContinueWatching: {},
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
	return filter, nil
}

func libraryFilterNeedsSubtitleSync(filter LibraryFilter) bool {
	return strings.TrimSpace(filter.SmartView) == LibraryViewNoSubtitle ||
		(strings.TrimSpace(filter.SearchMode) == LibrarySearchModeSubtitle && strings.TrimSpace(filter.Keyword) != "")
}

func applyLibraryFilter(query *gorm.DB, filter LibraryFilter, now time.Time) (*gorm.DB, error) {
	normalized, err := normalizeLibraryFilter(filter)
	if err != nil {
		return nil, err
	}
	filter = normalized

	if filter.Keyword != "" {
		pattern := "%" + strings.ToLower(escapeSQLLike(filter.Keyword)) + "%"
		if filter.SearchMode == LibrarySearchModeSubtitle {
			query = query.Where(`EXISTS (
				SELECT 1 FROM subtitle_segments
				WHERE subtitle_segments.video_id = videos.id
				  AND LOWER(subtitle_segments.text) LIKE ? ESCAPE '\'
			)`, pattern)
		} else {
			query = query.Where("(LOWER(videos.name) LIKE ? ESCAPE '\\' OR LOWER(videos.path) LIKE ? ESCAPE '\\')", pattern, pattern)
		}
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
	return s.GetVideo(videoID)
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
	return s.GetVideo(videoID)
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
	return s.GetVideo(videoID)
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
	}
	if err := database.DB.Create(&view).Error; err != nil {
		return nil, fmt.Errorf("保存视图失败: %w", err)
	}
	return &view, nil
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
	if limit <= 0 || limit > 200 {
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
