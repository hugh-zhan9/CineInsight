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
	EntryKind        string  `gorm:"size:16;not null;default:video;check:chk_queue_entry_kind,(entry_kind = 'video' AND clip_start_ms IS NULL AND clip_end_ms IS NULL AND source_size IS NULL AND source_mtime_ns IS NULL AND origin_bookmark_id IS NULL AND preview_batch_id IS NULL AND preview_position IS NULL AND preview_count IS NULL) OR (entry_kind = 'clip' AND clip_start_ms IS NOT NULL AND clip_end_ms IS NOT NULL AND clip_start_ms >= 0 AND clip_end_ms > clip_start_ms AND clip_end_ms <= 9007199254740991 AND source_size IS NOT NULL AND source_size >= 0 AND source_mtime_ns IS NOT NULL AND preview_batch_id IS NOT NULL AND length(preview_batch_id) = 36 AND preview_position IS NOT NULL AND preview_count IS NOT NULL AND preview_position >= 1 AND preview_position <= preview_count AND preview_count <= 1000 AND (origin_bookmark_id IS NULL OR origin_bookmark_id > 0))" json:"entry_kind"`
	ClipStartMS      *int64  `json:"clip_start_ms"`
	ClipEndMS        *int64  `json:"clip_end_ms"`
	SourceSize       *int64  `json:"-"`
	SourceModTimeNS  *int64  `gorm:"column:source_mtime_ns" json:"-"`
	OriginBookmarkID *uint   `json:"origin_bookmark_id"`
	PreviewBatchID   *string `gorm:"size:36;uniqueIndex:idx_queue_preview_position,priority:1" json:"preview_batch_id"`
	PreviewPosition  *int    `gorm:"uniqueIndex:idx_queue_preview_position,priority:2" json:"preview_position"`
	PreviewCount     *int    `json:"preview_count"`

	ID        uint      `gorm:"primaryKey;index:idx_queue_order,priority:2" json:"id"`
	VideoID   uint      `gorm:"not null" json:"video_id"`
	Title     string    `gorm:"type:text;not null" json:"title"`
	Position  int64     `gorm:"not null;index:idx_queue_order,priority:1" json:"position"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
}
