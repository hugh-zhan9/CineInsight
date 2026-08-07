package services

import (
	"fmt"
	"time"
	"video-master/database"
	"video-master/models"
)

// ImageStatsSummary 图片库摘要统计（AC-11 / D-014）。
type ImageStatsSummary struct {
	ImageCount    int64 `json:"image_count"`
	TotalSize     int64 `json:"total_size"`
	FavoriteCount int64 `json:"favorite_count"`
}

// ImageStatsBucket 单维度存储聚合桶，供前端 BucketChart 消费。
type ImageStatsBucket struct {
	Label     string `json:"label"`
	TotalSize int64  `json:"total_size"`
}

// ImageStats 图片洞察聚合结果，只统计活跃（未软删除）图片。
type ImageStats struct {
	GeneratedAt        time.Time          `json:"generated_at" ts_type:"string"`
	Summary            ImageStatsSummary  `json:"summary"`
	StorageByDirectory []ImageStatsBucket `json:"storage_by_directory"`
	StorageByFormat    []ImageStatsBucket `json:"storage_by_format"`
}

// ImageStatsService 提供图片维度的只读洞察聚合，镜像 LibraryStatsService。
type ImageStatsService struct {
	now func() time.Time
}

func NewImageStatsService() *ImageStatsService {
	return &ImageStatsService{now: time.Now}
}

// GetImageInsights 汇总图片库摘要与目录/格式两个存储分布，只查活跃行。
func (s *ImageStatsService) GetImageInsights() (*ImageStats, error) {
	if database.DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	stats := &ImageStats{GeneratedAt: s.now()}
	if err := database.DB.Model(&models.Image{}).Select(`
		COUNT(*) AS image_count,
		COALESCE(SUM(size), 0) AS total_size,
		COALESCE(SUM(CASE WHEN is_favorite THEN 1 ELSE 0 END), 0) AS favorite_count
	`).Scan(&stats.Summary).Error; err != nil {
		return nil, err
	}

	var err error
	if stats.StorageByDirectory, err = imageStorageByColumn("directory"); err != nil {
		return nil, err
	}
	if stats.StorageByFormat, err = imageStorageByColumn("format"); err != nil {
		return nil, err
	}
	return stats, nil
}

// imageStorageByColumn 按单列聚合活跃图片的存储字节；column 只允许代码内
// 白名单列名（directory/format），不接收外部输入。
func imageStorageByColumn(column string) ([]ImageStatsBucket, error) {
	var buckets []ImageStatsBucket
	err := database.DB.Model(&models.Image{}).
		Select(column + " AS label, COALESCE(SUM(size), 0) AS total_size").
		Group(column).Order("total_size DESC, label ASC").Limit(50).Scan(&buckets).Error
	return buckets, err
}
