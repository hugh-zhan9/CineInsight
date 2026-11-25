package models

import "time"

// JellyfinRecommendationHistory 记录 Jellyfin 随机推荐（如 Fileball 搜索建议）已推送过的视频。
type JellyfinRecommendationHistory struct {
	ID            uint      `gorm:"primaryKey"`
	VideoID       uint      `gorm:"not null;uniqueIndex"`
	RecommendedAt time.Time `gorm:"not null"`
}
