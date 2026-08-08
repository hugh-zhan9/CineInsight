package models

import "time"

// ShortFeedInteraction stores long-lived per-video feed state without changing canonical tags.
type ShortFeedInteraction struct {
	ID        uint  `gorm:"primarykey" json:"id"`
	VideoID   uint  `gorm:"uniqueIndex;not null" json:"video_id"`
	Video     Video `gorm:"constraint:OnDelete:CASCADE;" json:"video"`
	Liked     bool  `gorm:"default:false;index" json:"liked"`
	Favorited bool  `gorm:"default:false;index" json:"favorited"`
	// FavoriteSyncedToLibrary marks that the current favorited=true state has
	// already been projected into the main library once; it resets on every
	// favorited transition so each phone-side favorite action projects exactly
	// once and never overwrites a manual un-favorite in the main library.
	FavoriteSyncedToLibrary bool       `gorm:"default:false" json:"favorite_synced_to_library"`
	ViewCount               int        `gorm:"default:0" json:"view_count"`
	LastViewedAt            *time.Time `json:"last_viewed_at,omitempty" ts_type:"string"`
	LikedAt                 *time.Time `json:"liked_at,omitempty" ts_type:"string"`
	FavoritedAt             *time.Time `json:"favorited_at,omitempty" ts_type:"string"`
	CreatedAt               time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt               time.Time  `json:"updated_at" ts_type:"string"`
}

// ShortFeedImageInteraction 是图片侧的手机端互动状态。刻意与视频表并行而不是
// 合并成 (media_type, media_id) 复合键：合并需要迁移既有的喜欢/收藏数据并重建
// 唯一索引与外键级联，而并行表零迁移风险。两者形状一致，服务层用同一套读写收敛。
type ShortFeedImageInteraction struct {
	ID        uint  `gorm:"primarykey" json:"id"`
	ImageID   uint  `gorm:"uniqueIndex;not null" json:"image_id"`
	Image     Image `gorm:"constraint:OnDelete:CASCADE;" json:"image"`
	Liked     bool  `gorm:"default:false;index" json:"liked"`
	Favorited bool  `gorm:"default:false;index" json:"favorited"`
	// FavoriteSyncedToLibrary 语义与视频侧完全一致：每次手机端收藏动作只投影一次，
	// 不会覆盖用户随后在桌面端手动取消的收藏。
	FavoriteSyncedToLibrary bool       `gorm:"default:false" json:"favorite_synced_to_library"`
	ViewCount               int        `gorm:"default:0" json:"view_count"`
	LastViewedAt            *time.Time `json:"last_viewed_at,omitempty" ts_type:"string"`
	LikedAt                 *time.Time `json:"liked_at,omitempty" ts_type:"string"`
	FavoritedAt             *time.Time `json:"favorited_at,omitempty" ts_type:"string"`
	CreatedAt               time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt               time.Time  `json:"updated_at" ts_type:"string"`
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
