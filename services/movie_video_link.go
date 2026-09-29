package services

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 榜单条目与片库视频的关联（D-PC52）：关联表、片库匹配建议、已看状态的双向同步。
//
// 已看同步的环路防线有三道，缺一都会出问题：
//   - 视频 → 榜单：VideoService 在 is_watched 翻转提交后调 OnVideoWatchedChanged，
//     这里走 markEntry / clearMark 的内核，**不再**向视频回写；
//   - 榜单 → 视频：MarkEntry / ClearMark 在释放 markMu 之后调
//     LinkedVideoWatchSetter.SetVideoWatchedFromLink，该方法的合同是不触发观察者；
//   - 即使某个回写方违约把状态再通知回来，这里的写入是幂等的（标记已经是目标值
//     时只刷新 marked_at），不会继续往外发。

var (
	// ErrMovieLinkVideoNotFound 表示要关联的视频不存在或已被移除。
	ErrMovieLinkVideoNotFound = errors.New("要关联的视频不存在或已被移除")
	// ErrMovieLinkInvalidDoubanID 表示豆瓣 ID 不合规。
	ErrMovieLinkInvalidDoubanID = errors.New("豆瓣 ID 无效")
)

// LibraryMatchSuggestion 是一条「片库中可能已有」的建议。
type LibraryMatchSuggestion struct {
	VideoID      uint   `json:"video_id"`
	Name         string `json:"name"`
	DisplayTitle string `json:"display_title"`
	Score        int    `json:"score"`
}

// LinkedVideoView 是某个豆瓣 ID 已关联的一个视频（只含活跃视频）。
type LinkedVideoView struct {
	VideoID      uint   `json:"video_id"`
	Name         string `json:"name"`
	DisplayTitle string `json:"display_title"`
	IsWatched    bool   `json:"is_watched"`
}

const (
	librarySuggestLimit = 5
	// 打分：完全相等 > 前缀；文件名含年份再加分。分值只用于排序与展示强弱。
	libraryScoreExact = 100
	libraryScorePrefx = 60
	libraryScoreYear  = 10
)

// 编译期断言：榜单服务同时是视频已看观察者与片单来源观察者，接口改名会在这里报错。
var (
	_ WatchStateObserver          = (*MovieChartService)(nil)
	_ watchlistDoubanBindObserver = (*MovieChartService)(nil)
)

// SetLinkedVideoWatchSetter 注入「榜单侧改已看之后回写关联视频」的实现（接线项）。
// 传 nil 表示不回写。
func (s *MovieChartService) SetLinkedVideoWatchSetter(setter LinkedVideoWatchSetter) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linkedWatch = setter
}

func (s *MovieChartService) linkedVideoWatchSetter() LinkedVideoWatchSetter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.linkedWatch
}

// LinkMovieToVideo 建立榜单条目与视频的关联。同一对重复关联是幂等的空操作。
//
// 不要求榜单缓存里有这个豆瓣 ID：关联、标记与缓存三者互不依赖（标记表同样靠豆瓣 ID
// 自立门户，D-MC05）。视频必须是活跃的，软删的不能关联。
func (s *MovieChartService) LinkMovieToVideo(doubanID string, videoID uint) error {
	if s == nil {
		return errors.New("年度榜单服务不可用")
	}
	if !doubanSubjectID.MatchString(doubanID) {
		return fmt.Errorf("%w：%q", ErrMovieLinkInvalidDoubanID, doubanID)
	}
	var count int64
	if err := s.db.Model(&models.Video{}).Where("id = ?", videoID).Count(&count).Error; err != nil {
		return fmt.Errorf("读取视频失败: %w", err)
	}
	if count == 0 {
		return ErrMovieLinkVideoNotFound
	}
	// DoNothing 生成 ON CONFLICT DO NOTHING，两个后端行为一致；冲突目标是
	// (douban_id, video_id) 唯一索引，批内没有重复行（单行插入），不会触发 21000。
	link := models.MovieVideoLink{DoubanID: doubanID, VideoID: videoID}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; err != nil {
		return fmt.Errorf("关联视频失败: %w", err)
	}
	return nil
}

// UnlinkMovieVideo 解除关联，没有这条关联时是幂等的空操作。
func (s *MovieChartService) UnlinkMovieVideo(doubanID string, videoID uint) error {
	if s == nil {
		return errors.New("年度榜单服务不可用")
	}
	err := s.db.Where("douban_id = ? AND video_id = ?", doubanID, videoID).
		Delete(&models.MovieVideoLink{}).Error
	if err != nil {
		return fmt.Errorf("解除关联失败: %w", err)
	}
	return nil
}

// ListMovieVideoLinks 返回某个豆瓣 ID 已关联的活跃视频，按视频 ID 升序。
func (s *MovieChartService) ListMovieVideoLinks(doubanID string) ([]LinkedVideoView, error) {
	if s == nil {
		return nil, errors.New("年度榜单服务不可用")
	}
	views := make([]LinkedVideoView, 0)
	err := s.db.Table("movie_video_links AS l").
		Select("v.id AS video_id, v.name AS name, v.display_title AS display_title, v.is_watched AS is_watched").
		Joins("JOIN videos v ON v.id = l.video_id AND v.deleted_at IS NULL").
		Where("l.douban_id = ?", doubanID).
		Order("v.id ASC").
		Scan(&views).Error
	if err != nil {
		return nil, fmt.Errorf("读取关联视频失败: %w", err)
	}
	return views, nil
}

// linkedVideoIDsByDouban 返回全部「豆瓣 ID → 活跃关联视频 ID 列表」，供已看页整页标注。
// 关联表只有用户手点出来的几行，整表读一次比给每页拼一个 IN 列表更稳。
func (s *MovieChartService) linkedVideoIDsByDouban() (map[string][]uint, error) {
	var rows []struct {
		DoubanID string
		VideoID  uint
	}
	err := s.db.Table("movie_video_links AS l").
		Select("l.douban_id AS douban_id, l.video_id AS video_id").
		Joins("JOIN videos v ON v.id = l.video_id AND v.deleted_at IS NULL").
		Order("l.video_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("读取关联视频失败: %w", err)
	}
	links := make(map[string][]uint)
	for _, row := range rows {
		links[row.DoubanID] = append(links[row.DoubanID], row.VideoID)
	}
	return links, nil
}

// normalizeLibraryTitle 是匹配用的规范化：去空白、转小写、去标点与符号。
func normalizeLibraryTitle(text string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// LibraryMatchQuery 是批量建议里的一个查询。
type LibraryMatchQuery struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
}

// LibraryMatchKey 是批量结果的键：原样的片名加年份，前端用同样的拼法取回自己的那一份。
func LibraryMatchKey(title string, year int) string {
	return title + "|" + strconv.Itoa(year)
}

// SuggestLibraryMatches 给出片库里可能就是这部片的视频，最多 5 条，只读。
// 是批量接口的单条特例，规则见 SuggestLibraryMatchesBatch。
func (s *MovieChartService) SuggestLibraryMatches(title string, year int) ([]LibraryMatchSuggestion, error) {
	result, err := s.SuggestLibraryMatchesBatch([]LibraryMatchQuery{{Title: title, Year: year}})
	if err != nil {
		return nil, err
	}
	return result[LibraryMatchKey(title, year)], nil
}

// SuggestLibraryMatchesBatch 一次扫描片库匹配多个查询（榜单一页几十部片，逐条扫会
// 把片库扫几十遍），返回 LibraryMatchKey(title, year) → 建议列表；每个查询都有键，
// 没有命中或片名规范化后为空时是空列表。只读、只看活跃且未失效的视频。
//
// 匹配 display_title / original_title / 去扩展名的 name 三个字段，规范化之后**完全
// 相等或以查询词为前缀**才算命中；同一视频取三个字段里最高的分，最多 5 条。年份已知
// （> 0）且文件名含该年份时加分。
func (s *MovieChartService) SuggestLibraryMatchesBatch(queries []LibraryMatchQuery) (map[string][]LibraryMatchSuggestion, error) {
	if s == nil {
		return nil, errors.New("年度榜单服务不可用")
	}
	type compiled struct {
		key      string
		query    string
		yearText string
	}
	result := make(map[string][]LibraryMatchSuggestion, len(queries))
	active := make([]compiled, 0, len(queries))
	for _, q := range queries {
		key := LibraryMatchKey(q.Title, q.Year)
		if _, seen := result[key]; seen {
			continue
		}
		result[key] = make([]LibraryMatchSuggestion, 0, librarySuggestLimit)
		normalized := normalizeLibraryTitle(q.Title)
		if normalized == "" {
			continue
		}
		c := compiled{key: key, query: normalized}
		if q.Year > 0 {
			c.yearText = strconv.Itoa(q.Year)
		}
		active = append(active, c)
	}
	if len(active) == 0 {
		return result, nil
	}

	var batch []models.Video
	err := s.db.Model(&models.Video{}).
		Select("id", "name", "display_title", "original_title").
		Where("is_stale = ?", false).
		FindInBatches(&batch, 1000, func(_ *gorm.DB, _ int) error {
			for _, video := range batch {
				fields := libraryMatchFields(video)
				for _, c := range active {
					score := libraryMatchScoreFields(c.query, fields)
					if score == 0 {
						continue
					}
					if c.yearText != "" && strings.Contains(video.Name, c.yearText) {
						score += libraryScoreYear
					}
					result[c.key] = append(result[c.key], LibraryMatchSuggestion{
						VideoID:      video.ID,
						Name:         video.Name,
						DisplayTitle: video.DisplayTitle,
						Score:        score,
					})
				}
			}
			return nil
		}).Error
	if err != nil {
		return nil, fmt.Errorf("匹配片库失败: %w", err)
	}
	for key, suggestions := range result {
		sort.SliceStable(suggestions, func(i, j int) bool {
			if suggestions[i].Score != suggestions[j].Score {
				return suggestions[i].Score > suggestions[j].Score
			}
			return suggestions[i].VideoID < suggestions[j].VideoID
		})
		if len(suggestions) > librarySuggestLimit {
			suggestions = suggestions[:librarySuggestLimit]
		}
		result[key] = suggestions
	}
	return result, nil
}

// libraryMatchFields 返回视频三个可比字段的规范化形式（空的已剔除）。
func libraryMatchFields(video models.Video) []string {
	fields := make([]string, 0, 3)
	for _, candidate := range []string{
		video.DisplayTitle,
		video.OriginalTitle,
		strings.TrimSuffix(video.Name, filepath.Ext(video.Name)),
	} {
		if normalized := normalizeLibraryTitle(candidate); normalized != "" {
			fields = append(fields, normalized)
		}
	}
	return fields
}

// libraryMatchScoreFields 返回规范化字段里的最高匹配分，没有命中返回 0。
func libraryMatchScoreFields(query string, fields []string) int {
	best := 0
	for _, normalized := range fields {
		switch {
		case normalized == query:
			best = max(best, libraryScoreExact)
		case strings.HasPrefix(normalized, query):
			best = max(best, libraryScorePrefx)
		}
	}
	return best
}

// OnVideoWatchedChanged 实现 WatchStateObserver：视频的已看状态翻转后，同步关联的
// 榜单条目。已看 → 标记 watched；取消已看 → 撤销 watched 标记（其他标记不动，且
// 仍有别的关联视频是已看时也不撤）。
//
// 这条写入不触发任何回调：走的是 markEntry / clearMark 的内核，不经过 MarkEntry /
// ClearMark 的回写。缓存里没有该豆瓣条目、标不成 watched 时只记日志——标记的快照
// 只能从缓存行取（ErrMovieChartEntryNotFound），这里不编一个。
func (s *MovieChartService) OnVideoWatchedChanged(videoID uint, watched bool) {
	if s == nil {
		return
	}
	var doubanIDs []string
	err := s.db.Model(&models.MovieVideoLink{}).Where("video_id = ?", videoID).
		Order("douban_id ASC").Pluck("douban_id", &doubanIDs).Error
	if err != nil {
		log.Printf("[MovieChart] video watched sync video=%d 读取关联失败 err=%v", videoID, err)
		return
	}
	for _, doubanID := range doubanIDs {
		if err := s.applyVideoWatched(doubanID, videoID, watched); err != nil {
			log.Printf("[MovieChart] video watched sync video=%d douban=%s watched=%t err=%v", videoID, doubanID, watched, err)
		}
	}
}

func (s *MovieChartService) applyVideoWatched(doubanID string, videoID uint, watched bool) error {
	s.markMu.Lock()
	defer s.markMu.Unlock()
	if watched {
		_, err := s.markEntry(doubanID, models.MovieChartMarkWatched, true)
		return err
	}
	existing, err := s.loadChartMark(doubanID)
	if err != nil || existing == nil || existing.Mark != models.MovieChartMarkWatched {
		return err
	}
	// 同一部片可能关联了不止一个视频（不同版本）：还有别的已看视频就保留标记。
	var others int64
	err = s.db.Table("movie_video_links AS l").
		Joins("JOIN videos v ON v.id = l.video_id AND v.deleted_at IS NULL").
		Where("l.douban_id = ? AND l.video_id <> ? AND v.is_watched = ?", doubanID, videoID, true).
		Count(&others).Error
	if err != nil {
		return fmt.Errorf("读取其他关联视频失败: %w", err)
	}
	if others > 0 {
		return nil
	}
	return s.clearMark(doubanID)
}

// syncLinkedVideosWatched 把榜单侧的已看变化回写到全部关联视频。回写方法的合同是
// 不触发观察者。没注入回写方时什么都不做；单个视频失败只记日志，标记已经落库。
func (s *MovieChartService) syncLinkedVideosWatched(doubanID string, watched bool) {
	setter := s.linkedVideoWatchSetter()
	if setter == nil {
		return
	}
	var videoIDs []uint
	err := s.db.Table("movie_video_links AS l").
		Joins("JOIN videos v ON v.id = l.video_id AND v.deleted_at IS NULL").
		Where("l.douban_id = ?", doubanID).
		Order("l.video_id ASC").
		Pluck("l.video_id", &videoIDs).Error
	if err != nil {
		log.Printf("[MovieChart] linked video sync douban=%s 读取关联失败 err=%v", doubanID, err)
		return
	}
	for _, videoID := range videoIDs {
		if err := setter.SetVideoWatchedFromLink(videoID, watched); err != nil {
			log.Printf("[MovieChart] linked video sync douban=%s video=%d watched=%t err=%v", doubanID, videoID, watched, err)
		}
	}
}

// OnWatchlistDoubanBound 实现 watchlistDoubanBindObserver：手动条目补全写回了豆瓣 ID，
// 而该 ID 还没有标记时，补一个 want 标记（快照片名与年份，watchlist_entry_id 指向该条目），
// 让榜单上显示「想看」，也让之后撤销 want 与删除条目两个方向都能联动（D-PC52）。
//
// 该 ID 已有非空标记（想看 / 不想看 / 已看）时不动它：用户在榜单上的表态优先。
func (s *MovieChartService) OnWatchlistDoubanBound(entryID uint, doubanID, title string, year int) {
	if s == nil {
		return
	}
	if err := s.bindWatchlistEntry(entryID, doubanID, title, year); err != nil {
		log.Printf("[MovieChart] watchlist bound douban=%s entry=%d err=%v", doubanID, entryID, err)
	}
}

// OnWatchlistSourceChanged 实现 watchlistDoubanBindObserver：条目的来源 ID 变了，仍认领着
// 该条目、但豆瓣 ID 与新来源不一致的 want 标记必须释放，否则旧标记（如误匹配的 1984 版）之后
// 被取消想看时会把这条已改选成 2021 版的条目删掉。newDoubanID 为空表示改选到了非豆瓣来源，
// 此时该条目名下所有 want 都释放。
//
// 释放分两种（APP-07 M-2）：以 reuse 认领的是用户在榜单上点的「想看」复用了他自己的条目——
// 只释放认领（认领归零、来源 unclaimed），mark 保留 want，用户在榜单上的明确表态不被片单一侧
// 的改选清掉；chart / enrichment 认领的 want 是随这条条目的豆瓣 ID 而来的，照旧 mark 清空。
func (s *MovieChartService) OnWatchlistSourceChanged(entryID uint, newDoubanID string) {
	if s == nil {
		return
	}
	if err := s.releaseWantMarksOfEntry(entryID, newDoubanID); err != nil {
		log.Printf("[MovieChart] watchlist source changed entry=%d err=%v", entryID, err)
	}
}

func (s *MovieChartService) releaseWantMarksOfEntry(entryID uint, newDoubanID string) error {
	s.markMu.Lock()
	defer s.markMu.Unlock()
	now := s.now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.MovieChartMark{}).
			Where("mark = ? AND watchlist_entry_id = ? AND douban_id <> ? AND watchlist_entry_origin = ?",
				models.MovieChartMarkWant, entryID, newDoubanID, movieChartOriginReuse).
			Updates(map[string]any{
				"watchlist_entry_id":     0,
				"watchlist_entry_origin": movieChartOriginUnclaimed,
				"updated_at":             now,
			}).Error; err != nil {
			return fmt.Errorf("释放旧想看标记失败: %w", err)
		}
		if err := tx.Model(&models.MovieChartMark{}).
			Where("mark = ? AND watchlist_entry_id = ? AND douban_id <> ? AND watchlist_entry_origin <> ?",
				models.MovieChartMarkWant, entryID, newDoubanID, movieChartOriginReuse).
			Updates(map[string]any{
				"mark":                   "",
				"watchlist_entry_id":     0,
				"watchlist_entry_origin": "",
				"updated_at":             now,
			}).Error; err != nil {
			return fmt.Errorf("释放旧想看标记失败: %w", err)
		}
		return nil
	})
}

func (s *MovieChartService) bindWatchlistEntry(entryID uint, doubanID, title string, year int) error {
	s.markMu.Lock()
	defer s.markMu.Unlock()
	existing, err := s.loadChartMark(doubanID)
	if err != nil {
		return err
	}
	if existing != nil && existing.Mark != "" {
		return nil
	}
	now := s.now()
	row := models.MovieChartMark{
		DoubanID:             doubanID,
		Mark:                 models.MovieChartMarkWant,
		ReleaseYear:          year,
		Title:                movieChartTruncateTitle(title),
		WatchlistEntryID:     entryID,
		WatchlistEntryOrigin: models.MovieChartOriginEnrichment,
		MarkedAt:             now,
	}
	// 快照优先取榜单缓存（上映年份与海报以缓存为准），缓存里没有就用条目自己的。
	var cached models.MovieChartEntry
	if err := s.db.Where("douban_id = ?", doubanID).Take(&cached).Error; err == nil {
		row.Title = movieChartTruncateTitle(cached.Title)
		row.PosterURL = cached.PosterURL
		if cachedYear := movieChartReleaseYear(cached.ReleaseDate); cachedYear > 0 {
			row.ReleaseYear = cachedYear
		}
	}
	if existing == nil {
		// DoNothing：并发下别的写者抢先建了这一行就让它赢，不覆盖用户的表态。
		if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("写入想看标记失败: %w", err)
		}
		return nil
	}
	// 空标记行（撤销想看后留下的）：条件更新，读到之后被别人改过就不动。
	err = s.db.Model(&models.MovieChartMark{}).
		Where("id = ? AND mark = ?", existing.ID, "").
		Updates(map[string]any{
			"mark":                   row.Mark,
			"release_year":           row.ReleaseYear,
			"title":                  row.Title,
			"poster_url":             row.PosterURL,
			"watchlist_entry_id":     row.WatchlistEntryID,
			"watchlist_entry_origin": row.WatchlistEntryOrigin,
			"marked_at":              row.MarkedAt,
			"updated_at":             now,
		}).Error
	if err != nil {
		return fmt.Errorf("写入想看标记失败: %w", err)
	}
	return nil
}
