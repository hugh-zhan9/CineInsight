package models

import "time"

// ShortFeedInteraction stores long-lived per-video feed state without changing canonical tags.
type ShortFeedInteraction struct {
	ID           uint       `gorm:"primarykey" json:"id"`
	VideoID      uint       `gorm:"uniqueIndex;not null" json:"video_id"`
	Video        Video      `gorm:"constraint:OnDelete:CASCADE;" json:"video"`
	Liked        bool       `gorm:"default:false;index" json:"liked"`
	Favorited    bool       `gorm:"default:false;index" json:"favorited"`
	ViewCount    int        `gorm:"default:0" json:"view_count"`
	LastViewedAt *time.Time `json:"last_viewed_at,omitempty" ts_type:"string"`
	LikedAt      *time.Time `json:"liked_at,omitempty" ts_type:"string"`
	FavoritedAt  *time.Time `json:"favorited_at,omitempty" ts_type:"string"`
	CreatedAt    time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt    time.Time  `json:"updated_at" ts_type:"string"`
}

// ShortFeedTagPreference stores weak recommendation weights for existing tags only.
type ShortFeedTagPreference struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	TagID     uint      `gorm:"uniqueIndex;not null" json:"tag_id"`
	Tag       Tag       `gorm:"constraint:OnDelete:CASCADE;" json:"tag"`
	Score     float64   `gorm:"default:0" json:"score"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time `json:"updated_at" ts_type:"string"`
}
