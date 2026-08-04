package services

import (
	"fmt"
	"sort"
	"time"
	"video-master/database"
	"video-master/models"
)

type LibraryStatsSummary struct {
	VideoCount     int64   `json:"video_count"`
	TotalDuration  float64 `json:"total_duration"`
	TotalSize      int64   `json:"total_size"`
	WatchedCount   int64   `json:"watched_count"`
	WatchedPercent float64 `json:"watched_percent"`
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
		COALESCE(SUM(CASE WHEN is_watched THEN 1 ELSE 0 END), 0) AS watched_count
	`).Scan(&stats.Summary).Error; err != nil {
		return nil, err
	}
	if stats.Summary.VideoCount > 0 {
		stats.Summary.WatchedPercent = float64(stats.Summary.WatchedCount) * 100 / float64(stats.Summary.VideoCount)
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
	if err := database.DB.Model(&models.Video{}).
		Select("CAST(last_played_at AS DATE) AS date, COUNT(*) AS count").
		Where("last_played_at >= ?", now.AddDate(-1, 0, 0)).
		Group("CAST(last_played_at AS DATE)").Order("date ASC").
		Scan(&stats.WatchHeatmap).Error; err != nil {
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
