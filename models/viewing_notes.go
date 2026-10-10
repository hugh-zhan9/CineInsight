package models

import "time"

// VideoBookmark is user-owned text and a position in a particular source
// version. VideoID is a logical reference: deleting media must not delete notes.
type VideoBookmark struct {
	ID              uint      `gorm:"primarykey;index:idx_bookmark_video_id,priority:2,sort:desc" json:"id"`
	VideoID         uint      `gorm:"not null;index:idx_bookmark_video_id,priority:1" json:"video_id"`
	VideoTitle      string    `gorm:"type:text;not null" json:"video_title"`
	StartMS         int64     `gorm:"not null" json:"start_ms"`
	EndMS           *int64    `json:"end_ms"`
	Title           string    `gorm:"type:text;not null" json:"title"`
	Note            string    `gorm:"type:text;not null;default:''" json:"note"`
	TagsJSON        string    `gorm:"type:text;not null;default:'[]'" json:"-"`
	SourceSize      int64     `gorm:"not null" json:"-"`
	SourceModTimeNS int64     `gorm:"not null" json:"-"`
	Revision        uint64    `gorm:"not null" json:"revision"`
	CreatedAt       time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt       time.Time `json:"updated_at" ts_type:"string"`
}

const (
	DiaryOriginAutomatic  = "automatic"
	DiaryOriginHistorical = "historical"
	DiaryOriginManual     = "manual"
	DiaryDateRecorded     = "recorded_local"
	DiaryDateImported     = "imported_local"
	DiaryDateManual       = "manual"
	DiaryDateOverride     = "manual_override"
)

// ViewingDiaryEntry has a separate lifetime from videos and play_events. The
// nullable unique keys retain deduplication even after automatic entries are
// removed; manual rows have neither key and may be deleted outright.
type ViewingDiaryEntry struct {
	ID                uint           `gorm:"primarykey;index:idx_diary_day,priority:3,sort:desc" json:"id"`
	VideoID           *uint          `gorm:"index" json:"video_id"`
	Title             string         `gorm:"type:text;not null" json:"title"`
	WatchedOn         string         `gorm:"size:10;not null;index:idx_diary_day,priority:2,sort:desc" json:"watched_on"`
	RecordedAt        *time.Time     `json:"recorded_at,omitempty" ts_type:"string"`
	DateBasis         string         `gorm:"size:24;not null;default:''" json:"date_basis"`
	UTCOffsetSeconds  *int           `json:"utc_offset_seconds"`
	Origin            string         `gorm:"size:16;not null;default:manual" json:"origin"`
	Source            string         `gorm:"size:16;not null;default:''" json:"source"`
	SourcePlayEventID *uint          `gorm:"uniqueIndex:idx_diary_play_event" json:"-"`
	SourceSessionKey  *string        `gorm:"size:64;uniqueIndex:idx_diary_view_session" json:"-"`
	Rating            *float64       `gorm:"type:numeric(3,1)" json:"rating"`
	Note              string         `gorm:"type:text;not null;default:''" json:"note"`
	Revision          uint64         `gorm:"not null" json:"revision"`
	CreatedAt         time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt         time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt         SoftDeleteTime `gorm:"index:idx_diary_day,priority:1" json:"-"`
}
