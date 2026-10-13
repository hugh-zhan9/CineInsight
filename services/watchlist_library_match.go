package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 想看条目与片库视频的关联（UI 二期 D-P2-03，详细设计 §2.2）。
//
// 唯一归处是 watchlist_video_links：按条目记、与条目来源无关，origin = auto / manual / dismissed。
// 读一个条目的关联 = 它的 auto / manual 行 ∪（条目此刻是豆瓣来源时）movie_video_links 里它那个豆瓣 ID 的行
// （榜单页建的，读作 manual），再按「作品」减去 dismissed：同一版本组任一成员解除过，整部作品都不算。
//
//   - 自动匹配只写 watchlist_video_links(origin=auto)，从不写 movie_video_links，所以不牵动榜单的已看双向同步；
//     插入用 (条目, 视频) 唯一键 ON CONFLICT DO NOTHING，已有的 dismissed / manual 行总是赢。
//   - 片单页手动关联写 manual（关联时间刷新为此刻），并清掉这部作品的 dismissed；豆瓣条目另外调榜单的
//     LinkMovieToVideo，保持「片单确认的关联榜单也看得到」的既有行为。
//   - 片单页解除把这一对写成 dismissed；豆瓣条目另外调榜单的 UnlinkMovieVideo。
//   - 榜单页解除（UnlinkMovieVideo）给此刻以这个豆瓣 ID 为来源的每个条目记 dismissed，自动匹配不会再连回来。
//
// 自动匹配：条目片名规范化（normalizeLibraryTitle）后与视频 display_title / original_title / 去扩展名 name 的
// 规范化结果**完全相等**、视频 year 等于条目年份（两边都有）、命中的视频按版本组合并后**恰好一部**，且这部作品
// 没有被这个条目解除过，才关联到那一部的代表视频。触发：条目新增、改名、在线补全成功（只算这一条，同步，不等
// 后台轮）；扫描入库新视频后、应用启动后（全部未关联条目，后台）；视频片名 / 原始片名 / 文件名 / 年份变了、
// 回收站恢复后（防抖约 5 秒的后台轮）。后台轮同一时刻只有一个，进行中再触发记一次「再跑」。

const watchlistLibraryStateMaxEntries = 200

const (
	watchlistInLibraryDefaultLimit = 12
	watchlistInLibraryMaxLimit     = 50
	// watchlistMatchDebounce 是视频元数据变化后到后台匹配轮开跑的防抖时间。
	watchlistMatchDebounce = 5 * time.Second
)

// WatchlistLinkedVideo 是条目已关联的一个视频。
type WatchlistLinkedVideo struct {
	VideoID   uint   `json:"video_id"`
	Title     string `json:"title"`
	Origin    string `json:"origin"` // auto | manual
	IsWatched bool   `json:"is_watched"`
}

// WatchlistLibraryState 是一个想看条目的片库关联状态。
type WatchlistLibraryState struct {
	Linked []WatchlistLinkedVideo `json:"linked"`
	// Dismissed 是这个条目解除过的视频 ID（含解除过的作品的全部版本），升序；「片库中可能已有」的建议据此滤掉。
	Dismissed []uint `json:"dismissed"`
}

// WatchlistInLibraryItem 是首页「想看的片片库里已有」的一项。
type WatchlistInLibraryItem struct {
	EntryID    uint   `json:"entry_id"`
	EntryTitle string `json:"entry_title"`
	Kind       string `json:"kind"`
	Year       int    `json:"year"`
	// HasPoster：条目自己有海报（/preview/watchlist-poster/<entry_id>）。
	HasPoster  bool      `json:"has_poster"`
	VideoID    uint      `json:"video_id"` // 要播的视频：最近一次关联的视频所在版本组的代表（不在组里就是它自己）
	VideoTitle string    `json:"video_title"`
	Origin     string    `json:"origin"` // auto | manual
	LinkedAt   time.Time `json:"linked_at" ts_type:"string"`
}

// watchlistLibraryMatch 是自动匹配的并发状态，零值可用。
type watchlistLibraryMatch struct {
	runMu    sync.Mutex // 全量轮同一时刻只有一个；单条匹配不拿它（DO NOTHING 兜底）
	mu       sync.Mutex
	running  bool // 后台全量轮的 goroutine 在跑
	rerun    bool // 后台轮进行中又被触发：跑完再来一轮
	stopped  bool // 应用退出：不再起新的后台轮
	debounce *time.Timer
	delay    time.Duration // 防抖时间，0 = watchlistMatchDebounce（测试改短）
	wg       sync.WaitGroup
	rounds   atomic.Int64 // 跑过的全量轮数（测试观察单飞用）
	// beforeAutoWrite 只给测试用：写一条自动关联之前调用，用来把解除插进一轮的中间。
	beforeAutoWrite func(entryID, videoID uint)
}

// watchlistChartLinker 是榜单一侧的关联读写口（MovieChartService 实现）。依赖方向仍是榜单 → 片单：
// 榜单服务构造时把自己挂到片单服务上，片单一侧只认接口。
type watchlistChartLinker interface {
	LinkMovieToVideo(doubanID string, videoID uint) error
	UnlinkMovieVideo(doubanID string, videoID uint) error
}

type watchlistChartLinks struct {
	mu     sync.RWMutex
	linker watchlistChartLinker
}

// SetChartLinker 挂上（或用 nil 摘掉）榜单关联读写口。
func (s *WatchlistService) SetChartLinker(linker watchlistChartLinker) {
	if s == nil {
		return
	}
	s.chartLinks.mu.Lock()
	defer s.chartLinks.mu.Unlock()
	s.chartLinks.linker = linker
}

func (s *WatchlistService) chartLinker() watchlistChartLinker {
	s.chartLinks.mu.RLock()
	defer s.chartLinks.mu.RUnlock()
	return s.chartLinks.linker
}

// --- 视频元数据变化 → 防抖后台匹配 ---

var libraryMatchInputs struct {
	mu       sync.RWMutex
	listener func(reason string)
}

// SetLibraryMatchInputListener 挂上「想看自动匹配的输入变了」的监听（App 接到 TriggerLibraryMatchSoon）；nil 摘掉。
func SetLibraryMatchInputListener(listener func(reason string)) {
	libraryMatchInputs.mu.Lock()
	defer libraryMatchInputs.mu.Unlock()
	libraryMatchInputs.listener = listener
}

// notifyLibraryMatchInputsChanged 在视频片名、原始片名、文件名、年份变化或视频恢复之后调用。
func notifyLibraryMatchInputsChanged(reason string) {
	libraryMatchInputs.mu.RLock()
	listener := libraryMatchInputs.listener
	libraryMatchInputs.mu.RUnlock()
	if listener != nil {
		listener(reason)
	}
}

// --- 读 ---

type watchlistLinkRow struct {
	EntryID  uint
	VideoID  uint
	Origin   string // auto | manual
	LinkedAt time.Time
}

type watchlistLinkVideo struct {
	ID           uint
	DisplayTitle string
	Name         string
	IsWatched    bool
	IsStale      bool
}

// watchlistLinkSet 是一批条目的关联读取结果。
type watchlistLinkSet struct {
	links           map[uint][]watchlistLinkRow // 条目 → 可见关联（活跃视频、作品没被解除），按关联时间升序
	videos          map[uint]watchlistLinkVideo // 可见关联涉及的活跃视频
	dismissedVideos map[uint][]uint             // 条目 → dismissed 行的视频 ID
	dismissedWorks  map[uint]map[int64]struct{} // 条目 → 解除过的作品
	groups          map[uint]uint               // 视频 → 版本组（涉及到的视频）
}

func watchlistEntryIsDouban(entry models.WatchlistEntry) bool {
	return entry.SourceName == WatchlistMetadataSourceDouban && entry.SourceItemID != ""
}

// workKeyOf 是视频所属「作品」的键：在版本组里就是组 ID，否则是负的视频 ID。
func workKeyOf(videoID uint, groups map[uint]uint) int64 {
	if group, ok := groups[videoID]; ok {
		return int64(group)
	}
	return -int64(videoID)
}

// videoGroupsOf 读这些视频所在的版本组（视频 → 组），不看视频状态。
func videoGroupsOf(db *gorm.DB, videoIDs []uint) (map[uint]uint, error) {
	groups := map[uint]uint{}
	ids := uniqueUintIDs(videoIDs)
	if len(ids) == 0 {
		return groups, nil
	}
	for _, chunk := range chunkUintIDs(ids, 500) {
		var rows []struct {
			VideoID uint
			GroupID uint
		}
		if err := db.Table("video_version_members").Select("video_id", "group_id").
			Where("video_id IN ?", chunk).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取版本组成员失败: %w", err)
		}
		for _, row := range rows {
			groups[row.VideoID] = row.GroupID
		}
	}
	return groups, nil
}

// workMembersOf 返回视频所在作品的全部成员 ID（版本组的所有成员，不看状态；不在组里就是它自己）。
func workMembersOf(db *gorm.DB, videoID uint) ([]uint, error) {
	groups, err := videoGroupsOf(db, []uint{videoID})
	if err != nil {
		return nil, err
	}
	group, ok := groups[videoID]
	if !ok {
		return []uint{videoID}, nil
	}
	var members []uint
	if err := db.Table("video_version_members").Where("group_id = ?", group).
		Order("video_id ASC").Pluck("video_id", &members).Error; err != nil {
		return nil, fmt.Errorf("读取版本组成员失败: %w", err)
	}
	return members, nil
}

// loadWatchlistLinks 读出这些条目的关联（规则见文件头）。
func loadWatchlistLinks(db *gorm.DB, entries []models.WatchlistEntry) (*watchlistLinkSet, error) {
	set := &watchlistLinkSet{
		links: map[uint][]watchlistLinkRow{}, videos: map[uint]watchlistLinkVideo{},
		dismissedVideos: map[uint][]uint{}, dismissedWorks: map[uint]map[int64]struct{}{}, groups: map[uint]uint{},
	}
	if len(entries) == 0 {
		return set, nil
	}
	type pair struct{ entry, video uint }
	merged := map[pair]watchlistLinkRow{}
	add := func(row watchlistLinkRow) {
		key := pair{row.EntryID, row.VideoID}
		current, ok := merged[key]
		if !ok {
			merged[key] = row
			return
		}
		if row.Origin == models.WatchlistLinkOriginManual {
			current.Origin = models.WatchlistLinkOriginManual
		}
		if row.LinkedAt.After(current.LinkedAt) {
			current.LinkedAt = row.LinkedAt
		}
		merged[key] = current
	}
	entryIDs := make([]uint, 0, len(entries))
	byDouban := map[string][]uint{}
	for _, entry := range entries {
		entryIDs = append(entryIDs, entry.ID)
		if watchlistEntryIsDouban(entry) {
			byDouban[entry.SourceItemID] = append(byDouban[entry.SourceItemID], entry.ID)
		}
	}
	dismissedIDs := []uint{}
	for _, chunk := range chunkUintIDs(entryIDs, 500) {
		var rows []models.WatchlistVideoLink
		if err := db.Select("watchlist_entry_id", "video_id", "origin", "updated_at").
			Where("watchlist_entry_id IN ?", chunk).Order("id ASC").Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取想看关联失败: %w", err)
		}
		for _, row := range rows {
			if row.Origin == models.WatchlistLinkOriginDismissed {
				set.dismissedVideos[row.WatchlistEntryID] = append(set.dismissedVideos[row.WatchlistEntryID], row.VideoID)
				dismissedIDs = append(dismissedIDs, row.VideoID)
				continue
			}
			add(watchlistLinkRow{EntryID: row.WatchlistEntryID, VideoID: row.VideoID, Origin: row.Origin, LinkedAt: row.UpdatedAt})
		}
	}
	if len(byDouban) > 0 {
		doubanIDs := make([]string, 0, len(byDouban))
		for id := range byDouban {
			doubanIDs = append(doubanIDs, id)
		}
		sort.Strings(doubanIDs)
		var rows []models.MovieVideoLink
		if err := db.Select("douban_id", "video_id", "created_at").
			Where("douban_id IN ?", doubanIDs).Order("id ASC").Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取榜单关联失败: %w", err)
		}
		for _, row := range rows {
			for _, entryID := range byDouban[row.DoubanID] {
				add(watchlistLinkRow{EntryID: entryID, VideoID: row.VideoID, Origin: models.WatchlistLinkOriginManual, LinkedAt: row.CreatedAt})
			}
		}
	}
	linkIDs := make([]uint, 0, len(merged))
	for key := range merged {
		linkIDs = append(linkIDs, key.video)
	}
	groups, err := videoGroupsOf(db, append(append([]uint{}, linkIDs...), dismissedIDs...))
	if err != nil {
		return nil, err
	}
	set.groups = groups
	for entryID, videoIDs := range set.dismissedVideos {
		works := map[int64]struct{}{}
		for _, videoID := range videoIDs {
			works[workKeyOf(videoID, groups)] = struct{}{}
		}
		set.dismissedWorks[entryID] = works
	}
	for _, chunk := range chunkUintIDs(uniqueUintIDs(linkIDs), 500) {
		var rows []watchlistLinkVideo
		if err := db.Model(&models.Video{}).
			Select("id", "display_title", "name", "is_watched", "is_stale").
			Where("id IN ?", chunk).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取关联视频失败: %w", err)
		}
		for _, row := range rows {
			set.videos[row.ID] = row
		}
	}
	for key, row := range merged {
		if _, active := set.videos[key.video]; !active {
			continue
		}
		if _, dismissed := set.dismissedWorks[key.entry][workKeyOf(key.video, groups)]; dismissed {
			continue
		}
		set.links[key.entry] = append(set.links[key.entry], row)
	}
	for entryID := range set.links {
		rows := set.links[entryID]
		sort.Slice(rows, func(i, j int) bool {
			if !rows[i].LinkedAt.Equal(rows[j].LinkedAt) {
				return rows[i].LinkedAt.Before(rows[j].LinkedAt)
			}
			return rows[i].VideoID < rows[j].VideoID
		})
	}
	return set, nil
}

// ListLibraryState 返回这些条目的片库关联状态（最多 200 个）；不存在的条目不出现在结果里。
func (s *WatchlistService) ListLibraryState(entryIDs []uint) (map[uint]WatchlistLibraryState, error) {
	ids := uniqueUintIDs(entryIDs)
	if len(ids) > watchlistLibraryStateMaxEntries {
		return nil, fmt.Errorf("一次最多查询 %d 条想看记录的片库状态", watchlistLibraryStateMaxEntries)
	}
	result := make(map[uint]WatchlistLibraryState, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var entries []models.WatchlistEntry
	if err := database.DB.Select("id", "source_name", "source_item_id").Where("id IN ?", ids).Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("读取想看片单失败: %w", err)
	}
	set, err := loadWatchlistLinks(database.DB, entries)
	if err != nil {
		return nil, err
	}
	dismissedGroups := []uint{}
	for _, videoIDs := range set.dismissedVideos {
		for _, videoID := range videoIDs {
			if group, ok := set.groups[videoID]; ok {
				dismissedGroups = append(dismissedGroups, group)
			}
		}
	}
	groupMembers := map[uint][]uint{}
	if len(dismissedGroups) > 0 {
		var rows []struct {
			GroupID uint
			VideoID uint
		}
		if err := database.DB.Table("video_version_members").Select("group_id", "video_id").
			Where("group_id IN ?", uniqueUintIDs(dismissedGroups)).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取版本组成员失败: %w", err)
		}
		for _, row := range rows {
			groupMembers[row.GroupID] = append(groupMembers[row.GroupID], row.VideoID)
		}
	}
	for _, entry := range entries {
		state := WatchlistLibraryState{Linked: make([]WatchlistLinkedVideo, 0, len(set.links[entry.ID])), Dismissed: []uint{}}
		for _, link := range set.links[entry.ID] {
			video := set.videos[link.VideoID]
			state.Linked = append(state.Linked, WatchlistLinkedVideo{
				VideoID: link.VideoID, Title: recapVideoTitle(video.DisplayTitle, video.Name),
				Origin: link.Origin, IsWatched: video.IsWatched,
			})
		}
		dismissed := []uint{}
		for _, videoID := range set.dismissedVideos[entry.ID] {
			dismissed = append(dismissed, videoID)
			if group, ok := set.groups[videoID]; ok {
				dismissed = append(dismissed, groupMembers[group]...)
			}
		}
		state.Dismissed = uniqueUintIDs(dismissed)
		sort.Slice(state.Dismissed, func(i, j int) bool { return state.Dismissed[i] < state.Dismissed[j] })
		result[entry.ID] = state
	}
	return result, nil
}

// --- 手动关联 / 解除 ---

// loadLinkTarget 读条目与视频，供手动关联 / 解除使用。activeOnly 时视频必须是活跃的。
func loadLinkTarget(tx *gorm.DB, entryID, videoID uint, activeOnly bool) (models.WatchlistEntry, error) {
	var entry models.WatchlistEntry
	if err := tx.Select("id", "source_name", "source_item_id").First(&entry, entryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entry, ErrWatchlistEntryNotFound
		}
		return entry, fmt.Errorf("读取想看记录失败: %w", err)
	}
	query := tx.Model(&models.Video{}).Where("id = ?", videoID)
	if !activeOnly {
		query = query.Unscoped()
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return entry, fmt.Errorf("读取视频失败: %w", err)
	}
	if count == 0 {
		return entry, ErrMovieLinkVideoNotFound
	}
	return entry, nil
}

// upsertWatchlistVideoLink 把这一对写成 origin；已有行时改 origin 并把关联时间（updated_at）刷新为此刻。
func upsertWatchlistVideoLink(tx *gorm.DB, entryID, videoID uint, origin string) error {
	now := time.Now()
	link := models.WatchlistVideoLink{WatchlistEntryID: entryID, VideoID: videoID, Origin: origin, CreatedAt: now, UpdatedAt: now}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "watchlist_entry_id"}, {Name: "video_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"origin", "updated_at"}),
	}).Create(&link).Error
}

// LinkVideo 手动确认关联：写 manual（关联时间为此刻），清掉这部作品对这个条目的 dismissed；豆瓣条目另外经
// 榜单的 LinkMovieToVideo 写 movie_video_links。视频必须是活跃的。
func (s *WatchlistService) LinkVideo(entryID, videoID uint) error {
	var doubanID string
	err := database.Transaction(func(tx *gorm.DB) error {
		entry, err := loadLinkTarget(tx, entryID, videoID, true)
		if err != nil {
			return err
		}
		if err := upsertWatchlistVideoLink(tx, entryID, videoID, models.WatchlistLinkOriginManual); err != nil {
			return fmt.Errorf("关联视频失败: %w", err)
		}
		members, err := workMembersOf(tx, videoID)
		if err != nil {
			return err
		}
		if err := tx.Where("watchlist_entry_id = ? AND video_id IN ? AND origin = ?", entryID, members, models.WatchlistLinkOriginDismissed).
			Delete(&models.WatchlistVideoLink{}).Error; err != nil {
			return fmt.Errorf("清除解除记录失败: %w", err)
		}
		if watchlistEntryIsDouban(entry) {
			doubanID = entry.SourceItemID
		}
		return nil
	})
	if err != nil {
		return err
	}
	if linker := s.chartLinker(); doubanID != "" && linker != nil {
		if err := linker.LinkMovieToVideo(doubanID, videoID); err != nil {
			return fmt.Errorf("已在想看中关联，但同步到榜单失败: %w", err)
		}
	}
	return nil
}

// UnlinkVideo 解除关联：把这一对写成 dismissed（没有关联时同样记下），这部作品之后不再自动关联；豆瓣条目另外经
// 榜单的 UnlinkMovieVideo 删掉 movie_video_links 那一行。
func (s *WatchlistService) UnlinkVideo(entryID, videoID uint) error {
	var doubanID string
	err := database.Transaction(func(tx *gorm.DB) error {
		entry, err := loadLinkTarget(tx, entryID, videoID, false)
		if err != nil {
			return err
		}
		if err := upsertWatchlistVideoLink(tx, entryID, videoID, models.WatchlistLinkOriginDismissed); err != nil {
			return fmt.Errorf("记录解除失败: %w", err)
		}
		if watchlistEntryIsDouban(entry) {
			doubanID = entry.SourceItemID
		}
		return nil
	})
	if err != nil {
		return err
	}
	if linker := s.chartLinker(); doubanID != "" && linker != nil {
		if err := linker.UnlinkMovieVideo(doubanID, videoID); err != nil {
			return fmt.Errorf("已在想看中解除，但同步到榜单失败: %w", err)
		}
	}
	return nil
}

// recordChartUnlinkDismissals 是榜单页解除（UnlinkMovieVideo）的另一半：给此刻以这个豆瓣 ID 为来源的每个想看
// 条目记下这一对 dismissed（作品级生效），自动匹配不会再把它连回来。视频已经不在时没什么可记。
func recordChartUnlinkDismissals(tx *gorm.DB, doubanID string, videoID uint) error {
	var videos int64
	if err := tx.Unscoped().Model(&models.Video{}).Where("id = ?", videoID).Count(&videos).Error; err != nil {
		return fmt.Errorf("读取视频失败: %w", err)
	}
	if videos == 0 {
		return nil
	}
	var entryIDs []uint
	if err := tx.Model(&models.WatchlistEntry{}).
		Where("source_name = ? AND source_item_id = ?", WatchlistMetadataSourceDouban, doubanID).
		Order("id ASC").Pluck("id", &entryIDs).Error; err != nil {
		return fmt.Errorf("读取想看片单失败: %w", err)
	}
	for _, entryID := range entryIDs {
		if err := upsertWatchlistVideoLink(tx, entryID, videoID, models.WatchlistLinkOriginDismissed); err != nil {
			return fmt.Errorf("记录解除失败: %w", err)
		}
	}
	return nil
}

// --- 首页「想看的片片库里已有」 ---

// ListInLibrary 返回首页「想看的片片库里已有」：每个条目取最近一次关联的未失效视频，映射到它所在版本组的代表；
// 这部作品任一（活跃、未失效）版本看过就不列。按关联时间倒序，limit 默认 12、最大 50。
func (s *WatchlistService) ListInLibrary(limit int) ([]WatchlistInLibraryItem, error) {
	if limit <= 0 {
		limit = watchlistInLibraryDefaultLimit
	}
	if limit > watchlistInLibraryMaxLimit {
		limit = watchlistInLibraryMaxLimit
	}
	var entries []models.WatchlistEntry
	if err := database.DB.Select("id", "title", "kind", "year", "poster_path", "source_name", "source_item_id").
		Order("id ASC").Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("读取想看片单失败: %w", err)
	}
	set, err := loadWatchlistLinks(database.DB, entries)
	if err != nil {
		return nil, err
	}
	latest := map[uint]watchlistLinkRow{}
	targetIDs := make([]uint, 0, len(set.links))
	for entryID, rows := range set.links {
		var chosen *watchlistLinkRow
		for i := range rows {
			if set.videos[rows[i].VideoID].IsStale {
				continue
			}
			if chosen == nil || !rows[i].LinkedAt.Before(chosen.LinkedAt) {
				chosen = &rows[i]
			}
		}
		if chosen != nil {
			latest[entryID] = *chosen
			targetIDs = append(targetIDs, chosen.VideoID)
		}
	}
	works, err := loadPlayableWorks(database.DB, targetIDs)
	if err != nil {
		return nil, err
	}
	items := make([]WatchlistInLibraryItem, 0, len(latest))
	for _, entry := range entries {
		link, ok := latest[entry.ID]
		if !ok {
			continue
		}
		work, ok := works[link.VideoID]
		if !ok || work.watched {
			continue
		}
		items = append(items, WatchlistInLibraryItem{
			EntryID: entry.ID, EntryTitle: entry.Title, Kind: entry.Kind, Year: entry.Year,
			HasPoster: entry.PosterPath != "", VideoID: work.representative.ID,
			VideoTitle: recapVideoTitle(work.representative.DisplayTitle, work.representative.Name),
			Origin:     link.Origin, LinkedAt: link.LinkedAt,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].LinkedAt.Equal(items[j].LinkedAt) {
			return items[i].LinkedAt.After(items[j].LinkedAt)
		}
		return items[i].EntryID > items[j].EntryID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

type playableWork struct {
	representative watchlistLinkVideo
	watched        bool
}

// loadPlayableWorks 把视频映射到它所在作品：代表 = 版本组里活跃、未失效的成员按 (版本顺序, 视频 ID) 第一个
// （与 CollapseVersions 同一规则），不在组里就是它自己；watched = 这些成员里任一看过。没有可播成员的视频不在结果里。
func loadPlayableWorks(db *gorm.DB, videoIDs []uint) (map[uint]playableWork, error) {
	result := map[uint]playableWork{}
	ids := uniqueUintIDs(videoIDs)
	if len(ids) == 0 {
		return result, nil
	}
	groups, err := videoGroupsOf(db, ids)
	if err != nil {
		return nil, err
	}
	groupIDs := make([]uint, 0, len(groups))
	for _, group := range groups {
		groupIDs = append(groupIDs, group)
	}
	byGroup := map[uint]playableWork{}
	if len(groupIDs) > 0 {
		var members []struct {
			GroupID      uint
			ID           uint
			DisplayTitle string
			Name         string
			IsWatched    bool
		}
		if err := db.Table("video_version_members AS vm").
			Select("vm.group_id AS group_id, v.id AS id, v.display_title AS display_title, v.name AS name, v.is_watched AS is_watched").
			Joins("JOIN videos v ON v.id = vm.video_id AND v.deleted_at IS NULL AND v.is_stale = ?", false).
			Where("vm.group_id IN ?", uniqueUintIDs(groupIDs)).
			Order("vm.group_id ASC").Order("vm.position ASC").Order("vm.video_id ASC").
			Scan(&members).Error; err != nil {
			return nil, fmt.Errorf("读取版本组成员失败: %w", err)
		}
		for _, member := range members {
			work, seen := byGroup[member.GroupID]
			if !seen {
				work.representative = watchlistLinkVideo{ID: member.ID, DisplayTitle: member.DisplayTitle, Name: member.Name, IsWatched: member.IsWatched}
			}
			work.watched = work.watched || member.IsWatched
			byGroup[member.GroupID] = work
		}
	}
	var singles []watchlistLinkVideo
	ungrouped := make([]uint, 0, len(ids))
	for _, id := range ids {
		if _, ok := groups[id]; !ok {
			ungrouped = append(ungrouped, id)
		}
	}
	if len(ungrouped) > 0 {
		if err := db.Model(&models.Video{}).Select("id", "display_title", "name", "is_watched", "is_stale").
			Where("id IN ? AND is_stale = ?", ungrouped, false).Scan(&singles).Error; err != nil {
			return nil, fmt.Errorf("读取关联视频失败: %w", err)
		}
	}
	for _, single := range singles {
		result[single.ID] = playableWork{representative: single, watched: single.IsWatched}
	}
	for _, id := range ids {
		if group, ok := groups[id]; ok {
			if work, ok := byGroup[group]; ok {
				result[id] = work
			}
		}
	}
	return result, nil
}

// --- 自动匹配 ---

// matchEntryToLibrary 是条目新增、改名、在线补全成功后的同步单条匹配；不等后台全量轮（插入 DO NOTHING 兜底），
// 失败只记日志（条目本身已经写好）。
func (s *WatchlistService) matchEntryToLibrary(entryID uint, reason string) {
	if s == nil || entryID == 0 {
		return
	}
	linked, err := s.runLibraryMatchRound(context.Background(), []uint{entryID})
	if err != nil {
		log.Printf("[WatchlistLibrary] match entry=%d reason=%s err=%v", entryID, reason, err)
		return
	}
	if linked > 0 {
		log.Printf("[WatchlistLibrary] auto linked entry=%d reason=%s", entryID, reason)
	}
}

// TriggerLibraryMatch 在后台跑一轮全量自动匹配（扫描入库新视频后、应用启动后、防抖到点）。同一时刻只有一个
// 后台轮，进行中再触发只记一次「再跑」，跑完再来一轮。
func (s *WatchlistService) TriggerLibraryMatch(reason string) {
	if s == nil {
		return
	}
	s.match.mu.Lock()
	if s.match.stopped {
		s.match.mu.Unlock()
		return
	}
	if s.match.running {
		s.match.rerun = true
		s.match.mu.Unlock()
		return
	}
	s.match.running = true
	s.match.wg.Add(1)
	s.match.mu.Unlock()
	go func() {
		defer s.match.wg.Done()
		for {
			linked, err := s.runLibraryMatchRound(context.Background(), nil)
			if err != nil {
				log.Printf("[WatchlistLibrary] match all reason=%s err=%v", reason, err)
			} else if linked > 0 {
				log.Printf("[WatchlistLibrary] auto linked entries=%d reason=%s", linked, reason)
			}
			s.match.mu.Lock()
			if !s.match.rerun || s.match.stopped {
				s.match.running = false
				s.match.rerun = false
				s.match.mu.Unlock()
				return
			}
			s.match.rerun = false
			s.match.mu.Unlock()
		}
	}()
}

// TriggerLibraryMatchSoon 在视频元数据变化（片名、原始片名、文件名、年份）或视频恢复之后，防抖约 5 秒再起一个
// 后台全量轮：批量编辑之类的一连串变化只跑一轮。
func (s *WatchlistService) TriggerLibraryMatchSoon(reason string) {
	if s == nil {
		return
	}
	s.match.mu.Lock()
	defer s.match.mu.Unlock()
	if s.match.stopped {
		return
	}
	delay := s.match.delay
	if delay <= 0 {
		delay = watchlistMatchDebounce
	}
	if s.match.debounce != nil {
		s.match.debounce.Stop()
	}
	s.match.debounce = time.AfterFunc(delay, func() { s.TriggerLibraryMatch(reason) })
}

// WaitLibraryMatch 等当前的后台匹配轮结束（测试用；不取消防抖中的触发）。
func (s *WatchlistService) WaitLibraryMatch() {
	if s == nil {
		return
	}
	s.match.wg.Wait()
}

// StopLibraryMatch 在应用退出时调用：取消防抖中的触发，不再起新的后台轮，等进行中的那一轮结束。
func (s *WatchlistService) StopLibraryMatch() {
	if s == nil {
		return
	}
	s.match.mu.Lock()
	s.match.stopped = true
	if s.match.debounce != nil {
		s.match.debounce.Stop()
	}
	s.match.mu.Unlock()
	s.match.wg.Wait()
}

type watchlistMatchCandidate struct {
	entry models.WatchlistEntry
	hits  map[uint]struct{}
}

// runLibraryMatchRound 跑一轮自动匹配，entryIDs 为 nil 表示全部条目（全量轮，持轮次锁）。返回本轮新建的关联数。
func (s *WatchlistService) runLibraryMatchRound(ctx context.Context, entryIDs []uint) (int, error) {
	if entryIDs == nil {
		s.match.runMu.Lock()
		defer s.match.runMu.Unlock()
		s.match.rounds.Add(1)
	} else if len(entryIDs) == 0 {
		return 0, nil
	}
	db := database.DB.WithContext(ctx)
	query := db.Select("id", "title", "year", "source_name", "source_item_id").
		Where("year > 0 AND title <> ''")
	if entryIDs != nil {
		query = query.Where("id IN ?", entryIDs)
	}
	var entries []models.WatchlistEntry
	if err := query.Order("id ASC").Find(&entries).Error; err != nil {
		return 0, fmt.Errorf("读取想看片单失败: %w", err)
	}
	if len(entries) == 0 {
		return 0, nil
	}
	set, err := loadWatchlistLinks(db, entries)
	if err != nil {
		return 0, err
	}
	byKey := map[string][]*watchlistMatchCandidate{}
	candidates := make([]*watchlistMatchCandidate, 0, len(entries))
	years := map[int]struct{}{}
	for _, entry := range entries {
		if len(set.links[entry.ID]) > 0 {
			continue
		}
		normalized := normalizeLibraryTitle(entry.Title)
		if normalized == "" {
			continue
		}
		candidate := &watchlistMatchCandidate{entry: entry, hits: map[uint]struct{}{}}
		key := normalized + "|" + strconv.Itoa(entry.Year)
		byKey[key] = append(byKey[key], candidate)
		candidates = append(candidates, candidate)
		years[entry.Year] = struct{}{}
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	yearList := make([]int, 0, len(years))
	for year := range years {
		yearList = append(yearList, year)
	}
	sort.Ints(yearList)
	var batch []models.Video
	err = db.Model(&models.Video{}).
		Select("id", "name", "display_title", "original_title", "year").
		Where("is_stale = ? AND year IN ?", false, yearList).
		FindInBatches(&batch, 1000, func(_ *gorm.DB, _ int) error {
			for _, video := range batch {
				if video.Year == nil {
					continue
				}
				seen := map[string]bool{}
				for _, field := range libraryMatchFields(video) {
					if seen[field] {
						continue
					}
					seen[field] = true
					for _, candidate := range byKey[field+"|"+strconv.Itoa(*video.Year)] {
						candidate.hits[video.ID] = struct{}{}
					}
				}
			}
			return nil
		}).Error
	if err != nil {
		return 0, fmt.Errorf("匹配片库失败: %w", err)
	}
	hitIDs := make([]uint, 0)
	for _, candidate := range candidates {
		for id := range candidate.hits {
			hitIDs = append(hitIDs, id)
		}
	}
	works, err := loadPlayableWorks(db, hitIDs)
	if err != nil {
		return 0, err
	}
	hitGroups, err := videoGroupsOf(db, hitIDs)
	if err != nil {
		return 0, err
	}
	linked := 0
	for _, candidate := range candidates {
		targets := map[int64]uint{}
		for id := range candidate.hits {
			if work, ok := works[id]; ok {
				targets[workKeyOf(id, hitGroups)] = work.representative.ID
			}
		}
		if len(targets) != 1 {
			continue // 没命中，或命中不止一部作品：留给「片库中可能已有」由用户确认
		}
		for workKey, target := range targets {
			if _, dismissed := set.dismissedWorks[candidate.entry.ID][workKey]; dismissed {
				continue // 这部作品（任一版本）被这个条目解除过
			}
			if hook := s.match.beforeAutoWrite; hook != nil {
				hook(candidate.entry.ID, target)
			}
			created, err := writeAutoWatchlistLink(db, candidate.entry.ID, target)
			if err != nil {
				return linked, err
			}
			if created {
				linked++
			}
		}
	}
	return linked, nil
}

// writeAutoWatchlistLink 写一条自动关联，只写 watchlist_video_links。(条目, 视频) 唯一键冲突时什么都不做：
// 已有的 dismissed / manual 行（并发的解除、手动关联，或另一轮）总是赢。
func writeAutoWatchlistLink(db *gorm.DB, entryID, videoID uint) (bool, error) {
	now := time.Now()
	link := models.WatchlistVideoLink{WatchlistEntryID: entryID, VideoID: videoID, Origin: models.WatchlistLinkOriginAuto, CreatedAt: now, UpdatedAt: now}
	result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&link)
	if result.Error != nil {
		return false, fmt.Errorf("写入自动关联失败: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}
