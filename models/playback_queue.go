package models

import "time"

// The singleton stores queue choices, not authoritative media/watch state.
// Logical IDs deliberately survive deletion; reimport cannot reuse consumed IDs.
type PlaybackQueueState struct {
	ID               uint      `gorm:"primaryKey;autoIncrement:false" json:"id"`
	Revision         uint64    `gorm:"not null" json:"revision"`
	Autoplay         bool      `gorm:"not null" json:"autoplay"`
	Player           string    `gorm:"size:16;not null" json:"player"`
	CurrentEntryID   *uint     `json:"current_entry_id"`
	Status           string    `gorm:"size:16;not null" json:"status"`
	ActiveToken      string    `gorm:"size:32;not null" json:"active_token"`
	LastErrorCode    string    `gorm:"size:64;not null" json:"last_error_code"`
	LastErrorMessage string    `gorm:"type:text;not null" json:"last_error_message"`
	UpdatedAt        time.Time `json:"updated_at" ts_type:"string"`
}

type PlaybackQueueEntry struct {
	ID        uint      `gorm:"primaryKey;index:idx_queue_order,priority:2" json:"id"`
	VideoID   uint      `gorm:"not null" json:"video_id"`
	Title     string    `gorm:"type:text;not null" json:"title"`
	Position  int64     `gorm:"not null;index:idx_queue_order,priority:1" json:"position"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
}
