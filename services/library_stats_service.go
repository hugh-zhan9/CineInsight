package services

import (
	"fmt"
	"sort"
	"time"
	"video-master/database"
	"video-master/models"
)

type LibraryStatsSummary struct {
	// Viewed counts distinct non-deleted videos with playback/progress evidence or
	// an explicit watched mark. Watched remains the completion/manual mark only.
	ViewedCount    int64   `json:"viewed_count"`
	ViewedPercent  float64 `json:"viewed_percent"`
	VideoCount     int64   `json:"video_count"`
	TotalDuration  float64 `json:"total_duration"`
	TotalSize      int64   `json:"total_size"`
	WatchedCount   int64   `json:"watched_count"`
	WatchedPercent float64 `json:"watched_percent"`
	// RecentAddedCount 是最近 30 天新入库的条数，供摘要卡的副行显示。
	// 其余副行指标（平均单片时长、观看总次数、最长连续天数、已评分数、
	// 评分中位数）都能从本结构其他字段推导，不再各开一条查询。
	RecentAddedCount int64 `json:"recent_added_count"`
}

type LibraryStatsBucket struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
	Bytes int64  `json:"bytes"`
}

type LibraryStatsWatchDay struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

type LibraryStatsRatingBucket struct {
	Rating float64 `json:"rating"`
	Count  int64   `json:"count"`
}

type LibraryStats struct {
	GeneratedAt         time.Time                  `json:"generated_at" ts_type:"string"`
	Summary             LibraryStatsSummary        `json:"summary"`
	StorageByTag        []LibraryStatsBucket       `json:"storage_by_tag"`
	StorageByDirectory  []LibraryStatsBucket       `json:"storage_by_directory"`
	StorageByResolution []LibraryStatsBucket       `json:"storage_by_resolution"`
	WatchHeatmap        []LibraryStatsWatchDay     `json:"watch_heatmap"`
	RatingDistribution  []LibraryStatsRatingBucket `json:"rating_distribution"`
	TopAITags           []LibraryStatsBucket       `json:"top_ai_tags"`
	// TotalPlayEvents 是播放账本里的全部流水条数（不限一年窗口），
	// PlaysBySource 是同一批流水按来源的拆分（desktop_play / desktop_random /
	// mobile_feed / legacy）。两者都只读账本，不碰 videos 上的计数列。
	TotalPlayEvents int64            `json:"total_play_events"`
	PlaysBySource   map[string]int64 `json:"plays_by_source"`
}

type LibraryStatsService struct {
	now func() time.Time
}

func NewLibraryStatsService() *LibraryStatsService {
	return &LibraryStatsService{now: time.Now}
}

func (s *LibraryStatsService) GetStats() (*LibraryStats, error) {
	if database.DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	now := s.now()
	stats := &LibraryStats{GeneratedAt: now}
	if err := database.DB.Model(&models.Video{}).Select(`
		COUNT(*) AS video_count,
		COALESCE(SUM(duration), 0) AS total_duration,
		COALESCE(SUM(size), 0) AS total_size,
		COALESCE(SUM(CASE WHEN is_watched THEN 1 ELSE 0 END), 0) AS watched_count,
		COALESCE(SUM(CASE WHEN is_watched OR play_count > 0 OR random_play_count > 0
			OR last_played_at IS NOT NULL OR watch_position_seconds > 0
			OR EXISTS (SELECT 1 FROM play_events WHERE play_events.video_id = videos.id)
			THEN 1 ELSE 0 END), 0) AS viewed_count
	`).Scan(&stats.Summary).Error; err != nil {
		return nil, err
	}
	if stats.Summary.VideoCount > 0 {
		stats.Summary.WatchedPercent = float64(stats.Summary.WatchedCount) * 100 / float64(stats.Summary.VideoCount)
		stats.Summary.ViewedPercent = float64(stats.Summary.ViewedCount) * 100 / float64(stats.Summary.VideoCount)
	}
	if err := database.DB.Model(&models.Video{}).
		Where("created_at >= ?", now.AddDate(0, 0, -30)).
		Count(&stats.Summary.RecentAddedCount).Error; err != nil {
		return nil, err
	}

	var err error
	if stats.StorageByDirectory, err = libraryStorageByDirectory(); err != nil {
		return nil, err
	}
	if stats.StorageByTag, err = libraryStorageByTag(false); err != nil {
		return nil, err
	}
	if stats.TopAITags, err = libraryStorageByTag(true); err != nil {
		return nil, err
	}
	if stats.StorageByResolution, err = libraryStorageByResolution(); err != nil {
		return nil, err
	}
	heatmap, err := libraryWatchHeatmap(now)
	if err != nil {
		return nil, err
	}
	stats.WatchHeatmap = heatmap
	if stats.TotalPlayEvents, stats.PlaysBySource, err = libraryPlayEventTotals(); err != nil {
		return nil, err
	}
	if err := database.DB.Model(&models.Video{}).
		Select("personal_rating AS rating, COUNT(*) AS count").
		Where("personal_rating IS NOT NULL").Group("personal_rating").Order("personal_rating ASC").
		Scan(&stats.RatingDistribution).Error; err != nil {
		return nil, err
	}
	return stats, nil
}

// libraryWatchHeatmap 按播放事件统计近一年的每日播放次数。
//
// 读的是账本而不是 videos.last_played_at：后者每部片子只留最后一次，同一天播两部
// 不同的片能看出来，同一部片播两次却只算一格，热力图长期低报。账本一次播放一行，
// 数出来的才是真正的播放次数。
//
// 分组刻意放在 Go 侧而不是下推成 SQL，与 ListImageTimelineBuckets 同一个理由，
// 外加一条更硬的：SQLite 没有 DATE 类型，CAST(played_at AS DATE) 走 NUMERIC
// 亲和性，实测返回 "2026" 而不是 "2026-09-01"——一整年的播放会塞进热力图的同一个
// 格子，而且不报错。这类静默错误数据比崩溃难发现得多。
//
// 按 time.Local 归并也让口径与前端对上：heatmapDays 本来就是按本地日期构建坐标轴的，
// 而 Postgres 的 timestamptz 是按会话时区取值的。
func libraryWatchHeatmap(now time.Time) ([]LibraryStatsWatchDay, error) {
	var playedAt []time.Time
	if err := database.DB.Model(&models.PlayEvent{}).
		Where("played_at >= ?", now.AddDate(-1, 0, 0)).
		Order("played_at ASC").
		Pluck("played_at", &playedAt).Error; err != nil {
		return nil, err
	}

	counts := make(map[string]int64, len(playedAt))
	order := make([]string, 0, len(playedAt))
	for _, at := range playedAt {
		day := at.In(time.Local).Format("2006-01-02")
		if _, seen := counts[day]; !seen {
			order = append(order, day)
		}
		counts[day]++
	}
	sort.Strings(order)

	result := make([]LibraryStatsWatchDay, 0, len(order))
	for _, day := range order {
		result = append(result, LibraryStatsWatchDay{Date: day, Count: counts[day]})
	}
	return result, nil
}

// libraryPlayEventTotals 数账本的总条数与按来源的拆分。
//
// 不限时间窗：这两个数字回答的是"这个库一共被播过多少次、从哪来"，
// 与只看近一年的热力图是两个问题。
func libraryPlayEventTotals() (int64, map[string]int64, error) {
	var rows []struct {
		Source string
		Count  int64
	}
	if err := database.DB.Model(&models.PlayEvent{}).
		Select("source, COUNT(*) AS count").
		Group("source").Order("source ASC").Scan(&rows).Error; err != nil {
		return 0, nil, err
	}
	total := int64(0)
	bySource := make(map[string]int64, len(rows))
	for _, row := range rows {
		total += row.Count
		bySource[row.Source] = row.Count
	}
	return total, bySource, nil
}

func libraryStorageByDirectory() ([]LibraryStatsBucket, error) {
	var buckets []LibraryStatsBucket
	err := database.DB.Model(&models.Video{}).
		Select("directory AS label, COUNT(*) AS count, COALESCE(SUM(size), 0) AS bytes").
		Group("directory").Order("bytes DESC, label ASC").Limit(50).Scan(&buckets).Error
	return buckets, err
}

func libraryStorageByTag(aiOnly bool) ([]LibraryStatsBucket, error) {
	var buckets []LibraryStatsBucket
	query := database.DB.Table("tags").
		Select("tags.name AS label, COUNT(DISTINCT videos.id) AS count, COALESCE(SUM(videos.size), 0) AS bytes").
		Joins("JOIN video_tags ON video_tags.tag_id = tags.id").
		Joins("JOIN videos ON videos.id = video_tags.video_id AND videos.deleted_at IS NULL").
		Where("tags.deleted_at IS NULL")
	// AI 标签榜按出现次数排序；普通标签面板展示的是存储字节，排序与
	// 展示口径一致，避免 top-N 截断漏掉占用最大的标签。
	order := "bytes DESC, tags.name ASC"
	if aiOnly {
		query = query.Where("tags.is_system = ?", true).Limit(20)
		order = "count DESC, tags.name ASC"
	} else {
		query = query.Limit(50)
	}
	err := query.Group("tags.id, tags.name").Order(order).Scan(&buckets).Error
	return buckets, err
}

func libraryStorageByResolution() ([]LibraryStatsBucket, error) {
	var rows []struct {
		Height int
		Count  int64
		Bytes  int64
	}
	if err := database.DB.Model(&models.Video{}).
		Select("height, COUNT(*) AS count, COALESCE(SUM(size), 0) AS bytes").
		Group("height").Scan(&rows).Error; err != nil {
		return nil, err
	}
	labels := []string{"未知", "SD", "720p", "1080p", "2K", "4K+"}
	buckets := make(map[string]*LibraryStatsBucket, len(labels))
	for _, label := range labels {
		buckets[label] = &LibraryStatsBucket{Label: label}
	}
	for _, row := range rows {
		label := resolutionStatsLabel(row.Height)
		buckets[label].Count += row.Count
		buckets[label].Bytes += row.Bytes
	}
	result := make([]LibraryStatsBucket, 0, len(labels))
	for _, label := range labels {
		if buckets[label].Count > 0 {
			result = append(result, *buckets[label])
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Bytes > result[j].Bytes })
	return result, nil
}

func resolutionStatsLabel(height int) string {
	switch {
	case height <= 0:
		return "未知"
	case height < 720:
		return "SD"
	case height < 1080:
		return "720p"
	case height < 1440:
		return "1080p"
	case height < 2160:
		return "2K"
	default:
		return "4K+"
	}
}
