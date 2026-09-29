package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	// LibraryViewLocalMetadataUpdated：本地 NFO/元数据有更新待应用（D-PC39）。
	LibraryViewLocalMetadataUpdated = "local_metadata_updated"

	// StaleReasonUnknown 是 stale_reason 筛选里「原因未记录」（空原因）的取值（D-PC06）。
	StaleReasonUnknown = "unknown"
)

// hasEmbeddedSubtitleSQL 是「探测到内嵌字幕流」的唯一判定式；内嵌字幕流的类型字面值全仓只在此出现。
// 注意：设计文档写的是 media_streams.codec_type，实际列是 stream_type。
const hasEmbeddedSubtitleSQL = `EXISTS (SELECT 1 FROM media_streams ms WHERE ms.video_id = videos.id AND ms.stream_type = 'subtitle')`

// hasAnySubtitleSQL 是「有字幕」的唯一判定式（D-PC17）：字幕索引有片段、同目录有旁挂字幕，
// 或探测到内嵌字幕流。「无字幕」视图取它的反面，Jellyfin 的 HasSubtitles 也应复用它，
// 不得各自再写一份。列名全部限定，可以直接嵌进任何以 videos 为外层的查询（PG 的 42702）。
// 旁挂字幕的文件系统判定见 subtitle_sidecar.go，has_sidecar 列由它写入。
const hasAnySubtitleSQL = `(EXISTS (SELECT 1 FROM subtitle_index_states sis WHERE sis.video_id = videos.id AND (sis.segment_count > 0 OR sis.has_sidecar))
	OR ` + hasEmbeddedSubtitleSQL + `)`

const recentlyAddedWindow = 30 * 24 * time.Hour

// LibraryFilter 描述主片库和随机播放共享的筛选边界。
type LibraryFilter struct {
	SearchMode string `json:"search_mode"`
	Keyword    string `json:"keyword"`
	PathPrefix string `json:"path_prefix"`
	SmartView  string `json:"smart_view"`
	TagIDs     []uint `json:"tag_ids"`
	// PersonIDs 是人物筛选（D-PC33）：AND 语义，空表示不筛。
	PersonIDs []uint `json:"person_ids"`
	// StaleReason 只在「路径失效」视图里生效，精确匹配 models.StaleReason*；
	// StaleReasonUnknown 表示空原因（历史失效行）。
	StaleReason string   `json:"stale_reason"`
	MinSize     int64    `json:"min_size"`
	MaxSize     int64    `json:"max_size"`
	MinHeight   int      `json:"min_height"`
	MaxHeight   int      `json:"max_height"`
	MinRating   *float64 `json:"min_rating"`
	MaxRating   *float64 `json:"max_rating"`
	SortMode    string   `json:"sort_mode"`
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
	// AutomaticOverrideKinds：本页视频中被人工「加上」的自动标签 kind（D-PC36 行标签上的
	// 「手动」角标），按视频 ID 索引，只含有覆盖的视频。整页一次批量查询，不逐行查。
	AutomaticOverrideKinds map[uint][]string `json:"automatic_override_kinds"`
}

// loadAutomaticOverrideKinds 一次查出这些视频上 present=true 的自动标签覆盖（D-PC36），
// 按视频 ID 分组、kind 升序。present=false（手动去掉）的覆盖没有标签可挂角标，不返回。
func loadAutomaticOverrideKinds(videoIDs []uint) (map[uint][]string, error) {
	kinds := map[uint][]string{}
	ids := uniqueUintIDs(videoIDs)
	if len(ids) == 0 {
		return kinds, nil
	}
	var rows []models.VideoAutomaticTagOverride
	if err := database.DB.Select("video_id", "automatic_kind").
		Where("video_id IN ? AND present = ?", ids, true).
		Order("video_id ASC, automatic_kind ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("加载自动标签覆盖失败: %w", err)
	}
	for _, row := range rows {
		kinds[row.VideoID] = append(kinds[row.VideoID], row.AutomaticKind)
	}
	return kinds, nil
}

// automaticOverrideKindsBatchLimit 限制一次查询的视频数：调用方是按页展示的列表，远用不到这么多。
const automaticOverrideKindsBatchLimit = 1000

// GetAutomaticOverrideKinds 批量返回这些视频的「手动」角标数据（D-PC36），口径与
// LibraryVideoPage.automatic_override_kinds 相同；供最近播放、语义搜索等返回 []models.Video 的页面补齐。
func (s *VideoService) GetAutomaticOverrideKinds(videoIDs []uint) (map[uint][]string, error) {
	ids := uniqueUintIDs(videoIDs)
	if len(ids) > automaticOverrideKindsBatchLimit {
		return nil, fmt.Errorf("一次最多查询 %d 个视频的标签覆盖", automaticOverrideKindsBatchLimit)
	}
	return loadAutomaticOverrideKinds(ids)
}

// newLibraryVideoPage 组装一页结果并批量带出自动标签覆盖。
func newLibraryVideoPage(videos []models.Video, next *LibraryVideoCursor) (*LibraryVideoPage, error) {
	ids := make([]uint, 0, len(videos))
	for _, video := range videos {
		ids = append(ids, video.ID)
	}
	kinds, err := loadAutomaticOverrideKinds(ids)
	if err != nil {
		return nil, err
	}
	return &LibraryVideoPage{Videos: videos, NextCursor: next, AutomaticOverrideKinds: kinds}, nil
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
	LibraryViewStale: {}, LibraryViewLocalMetadataUpdated: {},
}

var validStaleReasons = map[string]struct{}{
	StaleReasonUnknown: {}, models.StaleReasonOfflineRoot: {}, models.StaleReasonMissingFile: {},
	models.StaleReasonRemovedRoot: {}, models.StaleReasonOutsideRoots: {}, models.StaleReasonPlayFailed: {},
	models.StaleReasonReadError: {}, models.StaleReasonWatcherMissing: {},
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
	filter.PersonIDs = uniqueUintIDs(filter.PersonIDs)
	sort.Slice(filter.PersonIDs, func(i, j int) bool { return filter.PersonIDs[i] < filter.PersonIDs[j] })
	filter.StaleReason = strings.TrimSpace(filter.StaleReason)
	if filter.SmartView != LibraryViewStale {
		filter.StaleReason = ""
	} else if filter.StaleReason != "" {
		if _, ok := validStaleReasons[filter.StaleReason]; !ok {
			return LibraryFilter{}, fmt.Errorf("不支持的失效原因: %s", filter.StaleReason)
		}
	}
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
// 主片库与清理候选共用黑名单过滤，已有记录不删除，取消排除后重新可见。
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
	return applyScanScope(query, true)
}

// applyScanScope 是 applyScanRootScope 的实现。includeRoots=false 时只保留黑名单排除、
// 跳过扫描根裁剪：「路径失效」视图要显示根被移除后落到范围外的记录（D-PC06）。
func applyScanScope(query *gorm.DB, includeRoots bool) (*gorm.DB, error) {
	scope, err := loadCleanupPathScope()
	if err != nil {
		return nil, err
	}
	for _, excluded := range scope.excluded {
		query = query.Where(`NOT (videos.path = ? OR videos.path LIKE ? ESCAPE '\')`, excluded, escapeSQLLikePrefix(scanRootChildPrefix(excluded))+"%")
	}
	roots := scope.roots
	if len(roots) == 0 || !includeRoots {
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

	// 失效视图跳过根裁剪（黑名单仍排除）：根被移除后落到范围外的记录正是要在这里找回。
	query, err = applyScanScope(query, filter.SmartView != LibraryViewStale)
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
	for _, personID := range filter.PersonIDs {
		query = query.Where("EXISTS (SELECT 1 FROM video_people vp WHERE vp.video_id = videos.id AND vp.person_id = ?)", personID)
	}

	switch filter.SmartView {
	case LibraryViewFavorites:
		query = query.Where("videos.is_favorite = ?", true)
	case LibraryViewLiked:
		query = query.Where("videos.is_liked = ?", true)
	case LibraryViewContinueWatching:
		// 「继续观看」= 断点可续（D-PC42）：包括重看中的已看片；已看后留下的旧断点不算。
		query = query.Where(resumableSQL)
	case LibraryViewUnwatched:
		query = query.Where("videos.is_watched = ?", false)
	case LibraryViewWatched:
		query = query.Where("videos.is_watched = ?", true)
	case LibraryViewRecentlyAdded:
		query = query.Where("videos.created_at >= ?", now.Add(-recentlyAddedWindow))
	case LibraryViewRecentlyPlayed:
		query = query.Where("videos.last_played_at IS NOT NULL")
	case LibraryViewUntagged:
		// 带 automatic_kind 的自动分类标签不算「已打标签」（D-PC33）；人物已是独立实体（people 表），
		// 本来就不在 video_tags 里，与这条判定无关。
		query = query.Where("NOT EXISTS (SELECT 1 FROM video_tags vt JOIN tags t ON t.id = vt.tag_id WHERE vt.video_id = videos.id AND COALESCE(t.automatic_kind, '') = '')")
	case LibraryViewNoSubtitle:
		query = query.Where("NOT " + hasAnySubtitleSQL)
	case LibraryViewLocalMetadataUpdated:
		query = query.Where("EXISTS (SELECT 1 FROM video_local_metadata_states lms WHERE lms.video_id = videos.id AND lms.status = ?)", LocalMetadataStateUpdateAvailable)
	case LibraryViewStale:
		query = query.Where("videos.is_stale = ?", true)
		switch filter.StaleReason {
		case "":
		case StaleReasonUnknown:
			query = query.Where("COALESCE(videos.stale_reason, '') = ''")
		default:
			query = query.Where("videos.stale_reason = ?", filter.StaleReason)
		}
	}

	// 失效记录只在「路径失效」视图里露面（D-S02）。它们当前指不到文件：留在默认
	// 列表里只会让人对着一条点开就失败的记录发愣，而随机播放共用这条筛选边界，
	// 抽中它等于白抽一次。删掉扫描目录后那批记录也是靠这一条从列表里消失的。
	if filter.SmartView != LibraryViewStale {
		query = query.Where("videos.is_stale = ?", false)
	}
	return query, nil
}

// favoriteColumns 是收藏切换要写的列（D-PC40）：置 true 时写 favorited_at，置 false 时清空。
// 置 true 用 COALESCE 保留已有时间，这样对已收藏的项重复置 true（双击、重复回流）不会
// 把它顶到「最近收藏」最前面；取消再收藏才会重新计时。
func favoriteColumns(favorite bool, now time.Time) map[string]interface{} {
	if favorite {
		return map[string]interface{}{
			"is_favorite":  true,
			"favorited_at": gorm.Expr("COALESCE(favorited_at, ?)", now),
		}
	}
	return map[string]interface{}{"is_favorite": false, "favorited_at": nil}
}

// SetVideoFavorite 更新主片库收藏状态，并维护 favorited_at。
func (s *VideoService) SetVideoFavorite(videoID uint, favorite bool) (*models.Video, error) {
	if videoID == 0 {
		return nil, fmt.Errorf("视频 ID 不能为空")
	}
	result := database.DB.Model(&models.Video{}).Where("id = ?", videoID).Updates(favoriteColumns(favorite, time.Now()))
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	return s.getVideoWithTags(videoID)
}

// SetVideoLiked 更新点赞状态（D-PC40）。与 SetVideoFavorite 同构：点赞的唯一数据是
// videos.is_liked，桌面与手机端都直接读写它；返回带标签的完整行，前端整行覆盖列表项。
func (s *VideoService) SetVideoLiked(videoID uint, liked bool) (*models.Video, error) {
	if videoID == 0 {
		return nil, fmt.Errorf("视频 ID 不能为空")
	}
	result := database.DB.Model(&models.Video{}).Where("id = ?", videoID).Update("is_liked", liked)
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

// watchStateNotifier 保存已看状态观察者（D-PC52）。观察者由 App 在启动与重建榜单服务后注入，
// 读写都可能与请求并发，所以单独加锁。
type watchStateNotifier struct {
	mu       sync.RWMutex
	observer WatchStateObserver
}

// SetWatchStateObserver 注入已看状态观察者（接线项）；传 nil 表示不通知。
func (s *VideoService) SetWatchStateObserver(observer WatchStateObserver) {
	s.watchState.mu.Lock()
	s.watchState.observer = observer
	s.watchState.mu.Unlock()
}

// NotifyWatchStateChanged 在 is_watched 实际翻转、写入已提交之后通知观察者。
// VideoService 自己的翻转路径都经过它；IINA 断点同步这类直接写库的服务也经它转发，
// 保证观察者只有 VideoService 这一个持有者。观察者的失败只记日志，不影响调用方：
// 已看状态已经落库，关联记录没跟上是次要问题。
func (s *VideoService) NotifyWatchStateChanged(videoID uint, watched bool) {
	if s == nil || videoID == 0 {
		return
	}
	s.watchState.mu.RLock()
	observer := s.watchState.observer
	s.watchState.mu.RUnlock()
	if observer == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("已看状态观察者失败 video_id=%d watched=%v err=%v", videoID, watched, recovered)
		}
	}()
	observer.OnVideoWatchedChanged(videoID, watched)
}

// 编译期断言：VideoService 是榜单一侧回写关联视频的入口（D-PC52）。
var _ LinkedVideoWatchSetter = (*VideoService)(nil)

// setVideoWatchedState 把 is_watched 写成目标值，返回是否真的翻转了。
//
// 翻转用条件更新（WHERE is_watched = 旧值）判定，并发下只有一方拿到 flipped=true，
// 观察者因此只会被通知一次。rewriteSame=true 时，已处于目标状态也照样重写一遍
// （SetVideoWatched 的既有语义：显式标已看会刷新 watched_at）；false 时视为幂等的空操作。
func setVideoWatchedState(videoID uint, watched bool, rewriteSame bool) (bool, error) {
	if videoID == 0 {
		return false, fmt.Errorf("视频 ID 不能为空")
	}
	updates := map[string]interface{}{"is_watched": watched}
	if watched {
		now := time.Now()
		updates["watched_at"] = &now
		// 有意不动 watch_position_seconds：手动标已看可能只是误点，销毁断点撤不回来
		// （看到 40 分钟的两小时电影点一下就没了）。「已看就从头播」由 resumable 保证：
		// 断点早于这次的 watched_at，就不再算可续播。
	} else {
		updates["watched_at"] = nil
	}
	result := database.DB.Model(&models.Video{}).Where("id = ? AND is_watched = ?", videoID, !watched).Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	if !rewriteSame {
		var count int64
		if err := database.DB.Model(&models.Video{}).Where("id = ?", videoID).Count(&count).Error; err != nil {
			return false, err
		}
		if count != 1 {
			return false, gorm.ErrRecordNotFound
		}
		return false, nil
	}
	result = database.DB.Model(&models.Video{}).Where("id = ?", videoID).Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, gorm.ErrRecordNotFound
	}
	return false, nil
}

// SetVideoWatched 更新主片库已看状态。已看状态实际翻转时，写入之后通知 WatchStateObserver。
func (s *VideoService) SetVideoWatched(videoID uint, watched bool) (*models.Video, error) {
	flipped, err := setVideoWatchedState(videoID, watched, true)
	if err != nil {
		return nil, err
	}
	if flipped {
		s.NotifyWatchStateChanged(videoID, watched)
	}
	return s.getVideoWithTags(videoID)
}

// SetVideoWatchedFromLink 实现 LinkedVideoWatchSetter：榜单一侧改了已看之后回写关联视频。
// 按合同**不**通知观察者，否则会回到榜单再写一遍；已处于目标状态时是幂等的空操作。
func (s *VideoService) SetVideoWatchedFromLink(videoID uint, watched bool) error {
	_, err := setVideoWatchedState(videoID, watched, false)
	return err
}

// watchCompletionTail 是片尾区间（D-PC41）：时长的 5%，最多 180 秒。
func watchCompletionTail(duration float64) float64 { return math.Min(duration*0.05, 180) }

// isWatchCompleted 是「算看完」的唯一判定（D-PC41）：内嵌播放器、Jellyfin、IINA 三条上报路径
// 都只调用它。位置必须真的往前走过，时长未知时一律不算看完。前端 utils/watchState.js 用同一组样例对齐。
func isWatchCompleted(position, duration float64) bool {
	return duration > 0 && position > 0 && position >= duration-watchCompletionTail(duration)
}

// resumable 是「断点有效、可以续播」的唯一判定（D-PC42）：有断点，且没看完，或者这次断点是
// 在最近一次标已看之后写的（重看）。resumableSQL 是它的 SQL 版本，两者必须同步修改。
func resumable(v *models.Video) bool {
	if v == nil || v.WatchPositionSeconds <= 0 {
		return false
	}
	if !v.IsWatched || v.WatchedAt == nil {
		return true
	}
	return v.WatchProgressUpdatedAt != nil && v.WatchProgressUpdatedAt.After(*v.WatchedAt)
}

// resumableSQL 是 resumable 的 SQL 版本，列名全部限定（PG 的 42702），可以嵌进任何以 videos
// 为外层的查询。它在任何上下文里都是二值结果（含 NOT (…)）：watch_position_seconds 与 is_watched
// 都是 NOT NULL 列，最后一项先判 watch_progress_updated_at IS NOT NULL 再比较——否则那一项在
// 「已看、watched_at 非空、进度时间为空」的行上是 NULL，整个式子跟着变成 NULL，
// NOT resumableSQL 也是 NULL，被当成 false 的一方就会漏掉这类行（PLAY-10）。
const resumableSQL = `(videos.watch_position_seconds > 0 AND (NOT videos.is_watched OR videos.watched_at IS NULL OR (videos.watch_progress_updated_at IS NOT NULL AND videos.watch_progress_updated_at > videos.watched_at)))`

// 内嵌播放器上报进度时的起播来源（D-PC42）。
const (
	// WatchProgressOriginResume：从库内断点起播。
	WatchProgressOriginResume = "resume"
	// WatchProgressOriginStart：从片头起播。
	WatchProgressOriginStart = "start"
	// WatchProgressOriginJump：从字幕命中或手动指定的时间起播，这类会话只允许把断点往前推。
	WatchProgressOriginJump = "jump"
)

// markWatchedFromCompletion 把一次「判为看完」落库：标已看、断点清零（2026-09-13 语义）；
// watched_at 只记第一次看完的时间。extra 里是调用方要一起写的列（进度更新时间）。
// scopes 是附加条件（IINA 同步用它挡住比库里更旧的断点文件）。
//
// 返回 flipped（本次写入把 is_watched 由 false 变为 true，调用方据此通知观察者）与 applied
// （库里满足 scopes 的这一行现在处于「已看、断点为 0」：本次写入的结果，或两次条件更新之间
// 别的写入者先一步做成的结果）。两者都按实际写入 / 实际状态报告，不会出现把 false 改成 true
// 却报告 flipped=false 的情况。
func markWatchedFromCompletion(db *gorm.DB, videoID uint, extra map[string]interface{}, now time.Time, scopes ...func(*gorm.DB) *gorm.DB) (flipped bool, applied bool, err error) {
	build := func(watchedAt interface{}) map[string]interface{} {
		updates := map[string]interface{}{
			"is_watched":             true,
			"watch_position_seconds": float64(0),
			"watched_at":             watchedAt,
		}
		for key, value := range extra {
			updates[key] = value
		}
		return updates
	}
	result := db.Model(&models.Video{}).Scopes(scopes...).
		Where("videos.id = ? AND videos.is_watched = ?", videoID, false).Updates(build(&now))
	if result.Error != nil {
		return false, false, result.Error
	}
	if result.RowsAffected == 1 {
		return true, true, nil
	}
	// 已经是已看：重看又播到片尾，照样清断点，但不改写第一次看完的时间。条件里带上
	// is_watched = true：两次更新之间有人把它改回了未看的话，这里不能顺手把它改成已看——
	// 那是一次 false → true 的翻转，却只能报告 flipped=false，观察者就收不到通知。
	result = db.Model(&models.Video{}).Scopes(scopes...).
		Where("videos.id = ? AND videos.is_watched = ?", videoID, true).
		Updates(build(gorm.Expr("COALESCE(watched_at, ?)", now)))
	if result.Error != nil {
		return false, false, result.Error
	}
	if result.RowsAffected == 1 {
		return false, true, nil
	}
	// 两次条件更新都落空：行已不在、被 scopes 挡住，或两次更新之间被改回了未看。重读一次，
	// 按库里的实际状态报告，不在这里再翻转。
	var current models.Video
	err = db.Model(&models.Video{}).Scopes(scopes...).
		Select("videos.id", "videos.is_watched", "videos.watch_position_seconds").
		Where("videos.id = ?", videoID).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return false, current.IsWatched && current.WatchPositionSeconds == 0, nil
}

// UpdateVideoWatchProgress 保存内嵌播放器 / Jellyfin 上报的观看位置（D-PC41、D-PC42）。
//
// durationSeconds 是播放器报的时长，只在库里的时长未知（≤0）时用于夹紧与看完判定；0 表示不知道。
// origin 取 WatchProgressOrigin*：jump 会话只允许把断点往前推，位置小于库里的有效断点就不写；
// 看完判定成立时照常写（看完与断点方向无关）。
func (s *VideoService) UpdateVideoWatchProgress(videoID uint, positionSeconds float64, durationSeconds float64, completed bool, origin string) (*models.Video, error) {
	if videoID == 0 {
		return nil, fmt.Errorf("视频 ID 不能为空")
	}
	if math.IsNaN(positionSeconds) || math.IsInf(positionSeconds, 0) || positionSeconds < 0 {
		return nil, fmt.Errorf("观看位置无效")
	}
	if math.IsNaN(durationSeconds) || math.IsInf(durationSeconds, 0) || durationSeconds < 0 {
		return nil, fmt.Errorf("播放时长无效")
	}
	origin = strings.TrimSpace(origin)
	if origin != WatchProgressOriginResume && origin != WatchProgressOriginStart && origin != WatchProgressOriginJump {
		return nil, fmt.Errorf("不支持的起播来源: %s", origin)
	}
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return nil, err
	}
	duration := video.Duration
	if duration <= 0 {
		duration = durationSeconds
	}
	if duration > 0 && positionSeconds > duration {
		positionSeconds = duration
	}
	// 调用方给的 completed 只覆盖一部分情况（内嵌播放器的 ended），而暂停在片尾、
	// 跳过片尾字幕后关掉抽屉、Jellyfin 的停止上报同样是看完了。
	if !completed && isWatchCompleted(positionSeconds, duration) {
		completed = true
	}
	now := time.Now()
	if completed {
		flipped, applied, err := markWatchedFromCompletion(database.DB, videoID, map[string]interface{}{"watch_progress_updated_at": &now}, now)
		if err != nil {
			return nil, err
		}
		if !applied {
			// 行已不在（getVideoWithTags 报未找到），或并发的「取消已看」先落了库：按实际状态返回。
			return s.getVideoWithTags(videoID)
		}
		if flipped {
			s.NotifyWatchStateChanged(videoID, true)
		}
		return s.getVideoWithTags(videoID)
	}
	query := database.DB.Model(&models.Video{}).Where("videos.id = ?", videoID)
	if origin == WatchProgressOriginJump {
		// 只和有效断点比：已看之前留下的旧断点（resumable 为 false）不算「已存位置」，
		// 否则重看时从字幕命中起播的进度永远写不进去。条件更新而不是先读后写，
		// 并发的两次上报里更靠后的位置不会被更早的覆盖。
		query = query.Where("(NOT "+resumableSQL+" OR videos.watch_position_seconds <= ?)", positionSeconds)
	}
	result := query.Updates(map[string]interface{}{
		"watch_position_seconds":    positionSeconds,
		"watch_progress_updated_at": &now,
	})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 && origin != WatchProgressOriginJump {
		return nil, gorm.ErrRecordNotFound
	}
	return s.getVideoWithTags(videoID)
}

// ListSavedLibraryViews 返回所有活跃保存视图。
func (s *VideoService) ListSavedLibraryViews() ([]models.SavedLibraryView, error) {
	var views []models.SavedLibraryView
	err := database.DB.Order("LOWER(name) ASC, id ASC").Find(&views).Error
	return views, err
}

// ErrSavedViewNameTaken 是保存视图重名时的错误，文案即错误码 saved_view_name_taken，前端按它映射提示。
var ErrSavedViewNameTaken = errors.New("saved_view_name_taken")

// savedViewNameConflict 识别保存视图名称的唯一键冲突。两个后端报错形状不同：Postgres 带索引名
// idx_saved_library_views_name_active，SQLite 给出列 "saved_library_views.name"；
// 若有人全局打开 gorm TranslateError，则统一成 gorm.ErrDuplicatedKey。
func savedViewNameConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "idx_saved_library_views_name_active") ||
		strings.Contains(message, "UNIQUE constraint failed: saved_library_views.name")
}

// buildSavedLibraryView 校验名称并把筛选条件规范化成保存视图行（新建与更新共用）。
func buildSavedLibraryView(name string, input LibraryFilter) (models.SavedLibraryView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return models.SavedLibraryView{}, fmt.Errorf("视图名称不能为空")
	}
	if len([]rune(name)) > 80 {
		return models.SavedLibraryView{}, fmt.Errorf("视图名称不能超过 80 个字符")
	}
	filter, err := normalizeLibraryFilter(input)
	if err != nil {
		return models.SavedLibraryView{}, err
	}
	tagJSON, err := json.Marshal(filter.TagIDs)
	if err != nil {
		return models.SavedLibraryView{}, fmt.Errorf("编码标签筛选失败: %w", err)
	}
	personJSON, err := json.Marshal(filter.PersonIDs)
	if err != nil {
		return models.SavedLibraryView{}, fmt.Errorf("编码人物筛选失败: %w", err)
	}
	return models.SavedLibraryView{
		Name: name, SearchMode: filter.SearchMode, Keyword: filter.Keyword, SmartView: filter.SmartView,
		TagIDsJSON: string(tagJSON), PersonIDsJSON: string(personJSON), MinSize: filter.MinSize, MaxSize: filter.MaxSize,
		MinHeight: filter.MinHeight, MaxHeight: filter.MaxHeight,
		MinRating: filter.MinRating, MaxRating: filter.MaxRating, SortMode: filter.SortMode,
	}, nil
}

// SaveLibraryView 创建命名保存视图。
func (s *VideoService) SaveLibraryView(input SavedLibraryViewInput) (*models.SavedLibraryView, error) {
	view, err := buildSavedLibraryView(input.Name, input.LibraryFilter)
	if err != nil {
		return nil, err
	}
	if err := database.DB.Create(&view).Error; err != nil {
		if savedViewNameConflict(err) {
			return nil, ErrSavedViewNameTaken
		}
		return nil, fmt.Errorf("保存视图失败: %w", err)
	}
	return &view, nil
}

// UpdateSavedLibraryView 用新的名称与筛选条件覆盖已有视图（D-PC35「用当前条件更新」「重命名」）。
// 名称与另一个活跃视图重名时返回 ErrSavedViewNameTaken；视图不存在返回 gorm.ErrRecordNotFound。
func (s *VideoService) UpdateSavedLibraryView(id uint, name string, filter LibraryFilter) (*models.SavedLibraryView, error) {
	if id == 0 {
		return nil, fmt.Errorf("视图 ID 不能为空")
	}
	next, err := buildSavedLibraryView(name, filter)
	if err != nil {
		return nil, err
	}
	var current models.SavedLibraryView
	if err := database.DB.First(&current, id).Error; err != nil {
		return nil, err
	}
	var taken int64
	if err := database.DB.Model(&models.SavedLibraryView{}).Where("name = ? AND id <> ?", next.Name, id).Count(&taken).Error; err != nil {
		return nil, err
	}
	if taken > 0 {
		return nil, ErrSavedViewNameTaken
	}
	updates := map[string]interface{}{
		"name": next.Name, "search_mode": next.SearchMode, "keyword": next.Keyword, "smart_view": next.SmartView,
		"tag_ids_json": next.TagIDsJSON, "person_ids_json": next.PersonIDsJSON,
		"min_size": next.MinSize, "max_size": next.MaxSize, "min_height": next.MinHeight, "max_height": next.MaxHeight,
		"min_rating": next.MinRating, "max_rating": next.MaxRating, "sort_mode": next.SortMode,
	}
	result := database.DB.Model(&models.SavedLibraryView{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		// 上面的重名预检与更新之间有竞态：并发改名撞上唯一键时同样映射成 saved_view_name_taken。
		if savedViewNameConflict(result.Error) {
			return nil, ErrSavedViewNameTaken
		}
		return nil, fmt.Errorf("更新视图失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	var updated models.SavedLibraryView
	if err := database.DB.First(&updated, id).Error; err != nil {
		return nil, err
	}
	return &updated, nil
}

// ActiveTagIDsResult 是 FilterActiveTagIDs 的返回：保留下来的标签 ID 与被剔除的数量。
type ActiveTagIDsResult struct {
	TagIDs  []uint `json:"tag_ids"`
	Dropped int    `json:"dropped"`
}

// activeTagIDs 过滤掉不存在或已软删的标签 ID（D-PC35），返回保留的 ID（去重、升序）与被剔除的数量。
// 目前唯一的调用方是桌面入口 FilterActiveTagIDs：前端保存或应用视图前经它剔除失效标签；
// 服务端的保存/应用路径本身不调用它（视图 JSON 保留原 ID，是否忽略由前端据返回值决定）。
func activeTagIDs(ids []uint) ([]uint, int, error) {
	wanted := uniqueUintIDs(ids)
	sort.Slice(wanted, func(i, j int) bool { return wanted[i] < wanted[j] })
	if len(wanted) == 0 {
		return []uint{}, 0, nil
	}
	active := make([]uint, 0, len(wanted))
	if err := database.DB.Model(&models.Tag{}).Where("id IN ?", wanted).Order("id ASC").Pluck("id", &active).Error; err != nil {
		return nil, 0, fmt.Errorf("加载标签失败: %w", err)
	}
	return active, len(wanted) - len(active), nil
}

// FilterActiveTagIDs 是 activeTagIDs 的桌面入口：前端保存或应用视图前调用，
// 用返回的 Dropped 提示「N 个条件已失效」。
func (s *VideoService) FilterActiveTagIDs(ids []uint) (*ActiveTagIDsResult, error) {
	active, dropped, err := activeTagIDs(ids)
	if err != nil {
		return nil, err
	}
	return &ActiveTagIDsResult{TagIDs: active, Dropped: dropped}, nil
}

// ActivePersonIDsResult 是 FilterActivePersonIDs 的返回：保留下来的人物 ID 与被剔除的数量。
type ActivePersonIDsResult struct {
	PersonIDs []uint `json:"person_ids"`
	Dropped   int    `json:"dropped"`
}

// activePersonIDs 与 activeTagIDs 同构：过滤掉已不存在的人物 ID，返回保留的 ID（去重、升序）与被剔除的数量。
// 删除人物后保存视图仍保留原 ID，应用视图前用它把失效条件忽略掉并提示数量。
func activePersonIDs(ids []uint) ([]uint, int, error) {
	wanted := uniqueUintIDs(ids)
	sort.Slice(wanted, func(i, j int) bool { return wanted[i] < wanted[j] })
	if len(wanted) == 0 {
		return []uint{}, 0, nil
	}
	active := make([]uint, 0, len(wanted))
	if err := database.DB.Model(&models.Person{}).Where("id IN ?", wanted).Order("id ASC").Pluck("id", &active).Error; err != nil {
		return nil, 0, fmt.Errorf("加载人物失败: %w", err)
	}
	return active, len(wanted) - len(active), nil
}

// FilterActivePersonIDs 是 activePersonIDs 的桌面入口：前端应用保存视图前调用，
// 用返回的 Dropped 提示「N 个条件已失效」。
func (s *VideoService) FilterActivePersonIDs(ids []uint) (*ActivePersonIDsResult, error) {
	active, dropped, err := activePersonIDs(ids)
	if err != nil {
		return nil, err
	}
	return &ActivePersonIDsResult{PersonIDs: active, Dropped: dropped}, nil
}

// ListStaleReasonCounts 返回失效记录按原因的计数（D-PC06），空原因归入 StaleReasonUnknown。
// 口径与「路径失效」视图一致：不做根裁剪，黑名单仍排除。
func (s *VideoService) ListStaleReasonCounts() (map[string]int, error) {
	query, err := applyScanScope(database.DB.Model(&models.Video{}).Where("videos.is_stale = ?", true), false)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Reason string
		Count  int
	}
	if err := query.Select("COALESCE(videos.stale_reason, '') AS reason, COUNT(*) AS count").
		Group("COALESCE(videos.stale_reason, '')").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		reason := row.Reason
		if reason == "" {
			reason = StaleReasonUnknown
		}
		counts[reason] += row.Count
	}
	return counts, nil
}

// visibleVideoQuery 是「默认视图」的可见口径：扫描根之内、不在黑名单、非失效（D-PC06）。
// 头部总数与洞察页总数共用它，保证与片库列表结果条对得上。
// 直接复用 applyLibraryFilter 的默认视图条件（空筛选），不再单独维护一份规则。
func visibleVideoQuery(query *gorm.DB) (*gorm.DB, error) {
	return applyLibraryFilter(query, LibraryFilter{}, time.Now())
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
		var next *LibraryVideoCursor
		if len(videos) > limit {
			videos = videos[:limit]
			last := videos[len(videos)-1]
			playWeight, err := s.getPlayWeight()
			if err != nil {
				return nil, err
			}
			next = &LibraryVideoCursor{
				SortMode: LibrarySortBalanced,
				Score:    float64(last.PlayCount)*playWeight + float64(last.RandomPlayCount),
				Size:     last.Size,
				ID:       last.ID,
			}
		}
		return newLibraryVideoPage(videos, next)
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
	var next *LibraryVideoCursor
	if len(videos) > limit {
		videos = videos[:limit]
		last := videos[len(videos)-1]
		next = &LibraryVideoCursor{SortMode: normalized.SortMode, RatingIsNull: last.PersonalRating == nil, ID: last.ID}
		if last.PersonalRating != nil {
			rating := *last.PersonalRating
			next.Rating = &rating
		}
	}
	return newLibraryVideoPage(videos, next)
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

// ListContinueWatchingWithFilter 是「继续观看」视图的默认排序（D-PC42、PLAY-09）：条件为
// resumableSQL，按 (watch_progress_updated_at DESC, id DESC) 键集分页，沿用最近播放的游标模式——
// 调用方把上一页最后一行的 watch_progress_updated_at（RFC3339 / RFC3339Nano，任意时区）与 id 传回来。
// 原样回传后端给的字符串最稳；换成 UTC（…Z）或截到毫秒也不漏行、不重复，见 continueWatchingCursorTime。
//
// 老数据里有断点却没有进度更新时间的行（旧版 IINA 同步只写位置），排在所有有时间的行之后、
// 按 id 倒序；游标落在这一段时 cursorProgressUpdatedAt 传空串、cursorID 非零。
// 两个后端对 NULL 的默认排序相反，这里用 CASE 显式统一。
func (s *VideoService) ListContinueWatchingWithFilter(filter LibraryFilter, cursorProgressUpdatedAt string, cursorID uint, limit int) (*LibraryVideoPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	cursorProgressUpdatedAt = strings.TrimSpace(cursorProgressUpdatedAt)
	if cursorProgressUpdatedAt != "" && cursorID == 0 {
		return nil, fmt.Errorf("继续观看游标不完整")
	}
	var cursorTime time.Time
	if cursorProgressUpdatedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, cursorProgressUpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("继续观看时间游标无效: %w", err)
		}
		if cursorTime, err = continueWatchingCursorTime(parsed, cursorID); err != nil {
			return nil, err
		}
	}
	filter.SmartView = LibraryViewContinueWatching
	if cursorID == 0 && libraryFilterNeedsSubtitleSync(filter) {
		if err := syncSubtitleIndexesFromFilesystem(); err != nil {
			return nil, err
		}
	}
	query, err := applyLibraryFilter(database.DB.Model(&models.Video{}).Preload("Tags"), filter, time.Now())
	if err != nil {
		return nil, err
	}
	switch {
	case cursorProgressUpdatedAt != "":
		query = query.Where("(videos.watch_progress_updated_at < ? OR (videos.watch_progress_updated_at = ? AND videos.id < ?) OR videos.watch_progress_updated_at IS NULL)", cursorTime, cursorTime, cursorID)
	case cursorID != 0:
		query = query.Where("videos.watch_progress_updated_at IS NULL AND videos.id < ?", cursorID)
	}
	var videos []models.Video
	err = query.Order("CASE WHEN videos.watch_progress_updated_at IS NULL THEN 1 ELSE 0 END ASC").
		Order("videos.watch_progress_updated_at DESC").Order("videos.id DESC").
		Limit(limit).Find(&videos).Error
	if err != nil {
		return nil, err
	}
	return newLibraryVideoPage(videos, nil)
}

// continueWatchingCursorTime 把回传的进度时间游标换成与存储同一时区、同一精度的值再参与比较（A-m-3）。
//
//   - 时区：SQLite 把时间存成带偏移的文本（驱动按值自己的时区格式化），比较按文本进行；写入路径
//     （time.Now()、断点文件的修改时间）都是本地时区。UTC（…Z）的游标直接绑定，就是拿「+00:00」
//     的文本去比「+08:00」的文本，整页错位。所以回传值统一换成 time.Local。
//   - 精度：库里存到微秒（PG）或纳秒（SQLite），前端的 Date 只有毫秒。游标行（cursorID）现存的
//     进度时间与回传值相差不到 1 毫秒时，改用库里的实际值（连同它存储时的偏移，A-m7）：否则与游标行同一毫秒、排在它后面的行
//     会被 `<` 漏掉，回传值进位时游标行自己又会再出现一次。游标行已不在、或进度时间已经变了
//     （相差 ≥1 毫秒）时照用回传值，与最近播放的游标同一语义。
func continueWatchingCursorTime(cursor time.Time, cursorID uint) (time.Time, error) {
	var row models.Video
	err := database.DB.Unscoped().Model(&models.Video{}).
		Select("videos.id", "videos.watch_progress_updated_at").
		Where("videos.id = ?", cursorID).Take(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, fmt.Errorf("读取继续观看游标失败: %w", err)
	}
	if err == nil && row.WatchProgressUpdatedAt != nil {
		if diff := row.WatchProgressUpdatedAt.Sub(cursor); diff > -time.Millisecond && diff < time.Millisecond {
			// 原样返回库里读出的值（A-m7）：它带着写入时的偏移，绑定回去与游标行存储的文本逐字相同。
			// 换成 time.Local 的话，偏移与当地不同的行（写入时在别的时区、夏令时切换前后）在 SQLite 的
			// 文本比较里对不上自己，游标行会再出现一次。
			return *row.WatchProgressUpdatedAt, nil
		}
	}
	return cursor.In(time.Local), nil
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
	videoQuery, err := visibleVideoQuery(database.DB.Model(&models.Video{}))
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
