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
// 一处一份：豆瓣来源（source_name = douban 且 source_item_id 非空）的已关联存在 movie_video_links
// （榜单的已看同步依赖它，origin 空串 = 手动、auto = 自动匹配）；其他来源存在 watchlist_video_links
// （auto / manual）；所有来源的「解除过」都存在 watchlist_video_links（dismissed）。读一个条目的关联
// = 对应那张表的已关联行（只算活跃视频）减去它的 dismissed 行。两张表不互相同步。
//
// 自动匹配：条目片名规范化（normalizeLibraryTitle）后与视频 display_title / original_title / 去扩展名
// name 的规范化结果**完全相等**、视频 year 等于条目年份（两边都有）、命中的视频按版本组合并后**恰好一部**，
// 才关联到那一部的代表视频；解除过的那一对跳过。触发：条目新增、改名、在线补全成功（只算这一条，同步）；
// 扫描入库新视频后与应用启动后（全部未关联条目，后台）。同一时刻只跑一轮，后台轮进行中再触发记一次「再跑」；
// 写入用唯一索引兜底，冲突即已关联。
//
// 原有 LinkMovieToVideo / UnlinkMovieVideo（榜单页）不变，后者不写 dismissed。

const watchlistLibraryStateMaxEntries = 200

const (
	watchlistInLibraryDefaultLimit = 12
	watchlistInLibraryMaxLimit     = 50
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
	runMu   sync.Mutex // 一轮匹配（单条或全量）同一时刻只有一个
	mu      sync.Mutex
	running bool // 后台全量轮的 goroutine 在跑
	rerun   bool // 后台轮进行中又被触发：跑完再来一轮
	wg      sync.WaitGroup
	rounds  atomic.Int64 // 跑过的轮数（测试观察单飞用）
}

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

func watchlistEntryIsDouban(entry models.WatchlistEntry) bool {
	return entry.SourceName == WatchlistMetadataSourceDouban && entry.SourceItemID != ""
}

func watchlistPairKey(entryID, videoID uint) string {
	return strconv.FormatUint(uint64(entryID), 10) + ":" + strconv.FormatUint(uint64(videoID), 10)
}

// loadWatchlistLinks 读出这些条目的已关联行（各走自己那张表，减去 dismissed，只留活跃视频），以及
// 这些条目的全部 dismissed 对。videos 是涉及到的活跃视频。
func loadWatchlistLinks(db *gorm.DB, entries []models.WatchlistEntry) (map[uint][]watchlistLinkRow, map[string]bool, map[uint]watchlistLinkVideo, error) {
	links := map[uint][]watchlistLinkRow{}
	dismissed := map[string]bool{}
	videos := map[uint]watchlistLinkVideo{}
	if len(entries) == 0 {
		return links, dismissed, videos, nil
	}
	entryIDs := make([]uint, 0, len(entries))
	byDouban := map[string][]uint{}
	plainIDs := make([]uint, 0, len(entries))
	for _, entry := range entries {
		entryIDs = append(entryIDs, entry.ID)
		if watchlistEntryIsDouban(entry) {
			byDouban[entry.SourceItemID] = append(byDouban[entry.SourceItemID], entry.ID)
		} else {
			plainIDs = append(plainIDs, entry.ID)
		}
	}
	var raw []watchlistLinkRow
	for _, chunk := range chunkUintIDs(entryIDs, 500) {
		var rows []struct {
			WatchlistEntryID uint
			VideoID          uint
			Origin           string
			CreatedAt        time.Time
		}
		if err := db.Model(&models.WatchlistVideoLink{}).
			Select("watchlist_entry_id", "video_id", "origin", "created_at").
			Where("watchlist_entry_id IN ?", chunk).Order("id ASC").Scan(&rows).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("读取想看关联失败: %w", err)
		}
		for _, row := range rows {
			if row.Origin == models.WatchlistLinkOriginDismissed {
				dismissed[watchlistPairKey(row.WatchlistEntryID, row.VideoID)] = true
				continue
			}
			raw = append(raw, watchlistLinkRow{EntryID: row.WatchlistEntryID, VideoID: row.VideoID, Origin: row.Origin, LinkedAt: row.CreatedAt})
		}
	}
	// 非豆瓣条目只认 watchlist_video_links；豆瓣条目只认 movie_video_links。
	plain := idSet(plainIDs)
	filtered := raw[:0]
	for _, row := range raw {
		if _, ok := plain[row.EntryID]; ok {
			filtered = append(filtered, row)
		}
	}
	raw = filtered
	if len(byDouban) > 0 {
		doubanIDs := make([]string, 0, len(byDouban))
		for id := range byDouban {
			doubanIDs = append(doubanIDs, id)
		}
		sort.Strings(doubanIDs)
		var rows []struct {
			DoubanID  string
			VideoID   uint
			Origin    string
			CreatedAt time.Time
		}
		if err := db.Model(&models.MovieVideoLink{}).
			Select("douban_id", "video_id", "origin", "created_at").
			Where("douban_id IN ?", doubanIDs).Order("id ASC").Scan(&rows).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("读取榜单关联失败: %w", err)
		}
		for _, row := range rows {
			origin := models.WatchlistLinkOriginManual
			if row.Origin == models.WatchlistLinkOriginAuto {
				origin = models.WatchlistLinkOriginAuto
			}
			for _, entryID := range byDouban[row.DoubanID] {
				if dismissed[watchlistPairKey(entryID, row.VideoID)] {
					continue
				}
				raw = append(raw, watchlistLinkRow{EntryID: entryID, VideoID: row.VideoID, Origin: origin, LinkedAt: row.CreatedAt})
			}
		}
	}
	videoIDs := make([]uint, 0, len(raw))
	for _, row := range raw {
		videoIDs = append(videoIDs, row.VideoID)
	}
	for _, chunk := range chunkUintIDs(uniqueUintIDs(videoIDs), 500) {
		var rows []watchlistLinkVideo
		if err := db.Model(&models.Video{}).
			Select("id", "display_title", "name", "is_watched", "is_stale").
			Where("id IN ?", chunk).Scan(&rows).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("读取关联视频失败: %w", err)
		}
		for _, row := range rows {
			videos[row.ID] = row
		}
	}
	for _, row := range raw {
		if _, active := videos[row.VideoID]; !active {
			continue
		}
		links[row.EntryID] = append(links[row.EntryID], row)
	}
	for entryID := range links {
		rows := links[entryID]
		sort.SliceStable(rows, func(i, j int) bool {
			if !rows[i].LinkedAt.Equal(rows[j].LinkedAt) {
				return rows[i].LinkedAt.Before(rows[j].LinkedAt)
			}
			return rows[i].VideoID < rows[j].VideoID
		})
	}
	return links, dismissed, videos, nil
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
	links, _, videos, err := loadWatchlistLinks(database.DB, entries)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		state := WatchlistLibraryState{Linked: make([]WatchlistLinkedVideo, 0, len(links[entry.ID]))}
		for _, link := range links[entry.ID] {
			video := videos[link.VideoID]
			state.Linked = append(state.Linked, WatchlistLinkedVideo{
				VideoID: link.VideoID, Title: recapVideoTitle(video.DisplayTitle, video.Name),
				Origin: link.Origin, IsWatched: video.IsWatched,
			})
		}
		result[entry.ID] = state
	}
	return result, nil
}

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

func upsertWatchlistVideoLink(tx *gorm.DB, entryID, videoID uint, origin string) error {
	link := models.WatchlistVideoLink{WatchlistEntryID: entryID, VideoID: videoID, Origin: origin}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "watchlist_entry_id"}, {Name: "video_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"origin", "updated_at"}),
	}).Create(&link).Error
}

// LinkVideo 手动确认关联：豆瓣条目写 movie_video_links（origin 空串 = 手动，已有自动行改成手动），其余写
// watchlist_video_links（manual）；同时清掉这一对的 dismissed。视频必须是活跃的。
func (s *WatchlistService) LinkVideo(entryID, videoID uint) error {
	return database.Transaction(func(tx *gorm.DB) error {
		entry, err := loadLinkTarget(tx, entryID, videoID, true)
		if err != nil {
			return err
		}
		if !watchlistEntryIsDouban(entry) {
			if err := upsertWatchlistVideoLink(tx, entryID, videoID, models.WatchlistLinkOriginManual); err != nil {
				return fmt.Errorf("关联视频失败: %w", err)
			}
			return nil
		}
		if err := tx.Where("watchlist_entry_id = ? AND video_id = ?", entryID, videoID).
			Delete(&models.WatchlistVideoLink{}).Error; err != nil {
			return fmt.Errorf("清除解除记录失败: %w", err)
		}
		link := models.MovieVideoLink{DoubanID: entry.SourceItemID, VideoID: videoID, Origin: ""}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "douban_id"}, {Name: "video_id"}},
			DoUpdates: clause.Assignments(map[string]any{"origin": "", "updated_at": time.Now()}),
		}).Create(&link).Error; err != nil {
			return fmt.Errorf("关联视频失败: %w", err)
		}
		return nil
	})
}

// UnlinkVideo 解除关联并记下 dismissed（之后不再自动关联这一对）；豆瓣条目同时删掉 movie_video_links 那一行。
// 没有关联时同样记 dismissed。
func (s *WatchlistService) UnlinkVideo(entryID, videoID uint) error {
	return database.Transaction(func(tx *gorm.DB) error {
		entry, err := loadLinkTarget(tx, entryID, videoID, false)
		if err != nil {
			return err
		}
		if watchlistEntryIsDouban(entry) {
			if err := tx.Where("douban_id = ? AND video_id = ?", entry.SourceItemID, videoID).
				Delete(&models.MovieVideoLink{}).Error; err != nil {
				return fmt.Errorf("解除关联失败: %w", err)
			}
		}
		if err := upsertWatchlistVideoLink(tx, entryID, videoID, models.WatchlistLinkOriginDismissed); err != nil {
			return fmt.Errorf("记录解除失败: %w", err)
		}
		return nil
	})
}

// ListInLibrary 返回首页「想看的片片库里已有」：已关联、要播的视频（代表视频）没看过且未失效，按关联时间
// 倒序；limit 默认 12、最大 50。
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
	links, _, videos, err := loadWatchlistLinks(database.DB, entries)
	if err != nil {
		return nil, err
	}
	latest := map[uint]watchlistLinkRow{}
	targetIDs := make([]uint, 0, len(links))
	for entryID, rows := range links {
		var chosen *watchlistLinkRow
		for i := range rows {
			if videos[rows[i].VideoID].IsStale {
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
	representatives, err := versionRepresentatives(database.DB, targetIDs)
	if err != nil {
		return nil, err
	}
	needed := make([]uint, 0, len(representatives))
	for _, id := range representatives {
		if _, ok := videos[id]; !ok {
			needed = append(needed, id)
		}
	}
	if len(needed) > 0 {
		var rows []watchlistLinkVideo
		if err := database.DB.Model(&models.Video{}).Select("id", "display_title", "name", "is_watched", "is_stale").
			Where("id IN ?", uniqueUintIDs(needed)).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取代表视频失败: %w", err)
		}
		for _, row := range rows {
			videos[row.ID] = row
		}
	}
	items := make([]WatchlistInLibraryItem, 0, len(latest))
	for _, entry := range entries {
		link, ok := latest[entry.ID]
		if !ok {
			continue
		}
		videoID := link.VideoID
		if representative, ok := representatives[videoID]; ok {
			videoID = representative
		}
		video, ok := videos[videoID]
		if !ok || video.IsWatched || video.IsStale {
			continue
		}
		items = append(items, WatchlistInLibraryItem{
			EntryID: entry.ID, EntryTitle: entry.Title, Kind: entry.Kind, Year: entry.Year,
			HasPoster: entry.PosterPath != "", VideoID: videoID,
			VideoTitle: recapVideoTitle(video.DisplayTitle, video.Name),
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

// versionRepresentatives 把视频映射到所在版本组的代表：组内活跃、未失效的成员按 (版本顺序, 视频 ID) 取第一个
// （与 CollapseVersions 同一规则）。不在组里的视频不出现在结果里。
func versionRepresentatives(db *gorm.DB, videoIDs []uint) (map[uint]uint, error) {
	result := map[uint]uint{}
	ids := uniqueUintIDs(videoIDs)
	if len(ids) == 0 {
		return result, nil
	}
	var memberships []struct {
		VideoID uint
		GroupID uint
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
		memberships = append(memberships, rows...)
	}
	if len(memberships) == 0 {
		return result, nil
	}
	groupIDs := make([]uint, 0, len(memberships))
	for _, membership := range memberships {
		groupIDs = append(groupIDs, membership.GroupID)
	}
	var members []struct {
		GroupID uint
		VideoID uint
	}
	if err := db.Table("video_version_members AS vm").Select("vm.group_id AS group_id, vm.video_id AS video_id").
		Joins("JOIN videos v ON v.id = vm.video_id AND v.deleted_at IS NULL AND v.is_stale = ?", false).
		Where("vm.group_id IN ?", uniqueUintIDs(groupIDs)).
		Order("vm.group_id ASC").Order("vm.position ASC").Order("vm.video_id ASC").
		Scan(&members).Error; err != nil {
		return nil, fmt.Errorf("读取版本组代表失败: %w", err)
	}
	first := map[uint]uint{}
	for _, member := range members {
		if _, ok := first[member.GroupID]; !ok {
			first[member.GroupID] = member.VideoID
		}
	}
	for _, membership := range memberships {
		if representative, ok := first[membership.GroupID]; ok {
			result[membership.VideoID] = representative
		}
	}
	return result, nil
}

// --- 自动匹配 ---

// matchEntryToLibrary 是条目新增、改名、在线补全成功后的同步单条匹配；失败只记日志（条目本身已经写好）。
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

// TriggerLibraryMatch 在后台跑一轮全量自动匹配（扫描入库新视频后、应用启动后）。同一时刻只有一个后台轮，
// 进行中再触发只记一次「再跑」，跑完再来一轮。
func (s *WatchlistService) TriggerLibraryMatch(reason string) {
	if s == nil {
		return
	}
	s.match.mu.Lock()
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
			if !s.match.rerun {
				s.match.running = false
				s.match.mu.Unlock()
				return
			}
			s.match.rerun = false
			s.match.mu.Unlock()
		}
	}()
}

// WaitLibraryMatch 等后台匹配轮结束（应用退出与测试用）。
func (s *WatchlistService) WaitLibraryMatch() {
	if s == nil {
		return
	}
	s.match.wg.Wait()
}

type watchlistMatchCandidate struct {
	entry models.WatchlistEntry
	hits  map[uint]struct{}
}

// runLibraryMatchRound 跑一轮自动匹配，entryIDs 为 nil 表示全部条目。返回本轮新建的关联数。
func (s *WatchlistService) runLibraryMatchRound(ctx context.Context, entryIDs []uint) (int, error) {
	s.match.runMu.Lock()
	defer s.match.runMu.Unlock()
	s.match.rounds.Add(1)
	db := database.DB.WithContext(ctx)
	query := db.Select("id", "title", "year", "source_name", "source_item_id").
		Where("year > 0 AND title <> ''")
	if entryIDs != nil {
		if len(entryIDs) == 0 {
			return 0, nil
		}
		query = query.Where("id IN ?", entryIDs)
	}
	var entries []models.WatchlistEntry
	if err := query.Order("id ASC").Find(&entries).Error; err != nil {
		return 0, fmt.Errorf("读取想看片单失败: %w", err)
	}
	if len(entries) == 0 {
		return 0, nil
	}
	links, dismissed, _, err := loadWatchlistLinks(db, entries)
	if err != nil {
		return 0, err
	}
	byKey := map[string][]*watchlistMatchCandidate{}
	candidates := make([]*watchlistMatchCandidate, 0, len(entries))
	years := map[int]struct{}{}
	for _, entry := range entries {
		if len(links[entry.ID]) > 0 {
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
	representatives, err := versionRepresentatives(db, hitIDs)
	if err != nil {
		return 0, err
	}
	linked := 0
	for _, candidate := range candidates {
		works := map[uint]struct{}{}
		for id := range candidate.hits {
			target := id
			if representative, ok := representatives[id]; ok {
				target = representative
			}
			works[target] = struct{}{}
		}
		if len(works) != 1 {
			continue // 没命中，或命中不止一部作品：留给「片库中可能已有」由用户确认
		}
		var target uint
		for id := range works {
			target = id
		}
		if dismissed[watchlistPairKey(candidate.entry.ID, target)] {
			continue
		}
		created, err := writeAutoWatchlistLink(db, candidate.entry, target)
		if err != nil {
			return linked, err
		}
		if created {
			linked++
		}
	}
	return linked, nil
}

// writeAutoWatchlistLink 写一条自动关联；唯一索引冲突（并发的另一轮或用户刚手动关联 / 解除）视为已处理。
func writeAutoWatchlistLink(db *gorm.DB, entry models.WatchlistEntry, videoID uint) (bool, error) {
	var result *gorm.DB
	if watchlistEntryIsDouban(entry) {
		link := models.MovieVideoLink{DoubanID: entry.SourceItemID, VideoID: videoID, Origin: models.WatchlistLinkOriginAuto}
		result = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&link)
	} else {
		link := models.WatchlistVideoLink{WatchlistEntryID: entry.ID, VideoID: videoID, Origin: models.WatchlistLinkOriginAuto}
		result = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&link)
	}
	if result.Error != nil {
		return false, fmt.Errorf("写入自动关联失败: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}
