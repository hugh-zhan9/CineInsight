package models

import "time"

// WatchlistEntry 是手工维护的想看片名，不依赖本地媒体文件。
type WatchlistEntry struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	Title     string    `gorm:"size:200;not null" json:"title"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time `json:"updated_at" ts_type:"string"`
}
