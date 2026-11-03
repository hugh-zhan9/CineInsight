package models

import "time"

// ChapterSet retains user-authored content after its logical video is removed.
type ChapterSet struct {
	ID              uint      `gorm:"primarykey" json:"id"`
	VideoID         uint      `gorm:"not null;uniqueIndex:idx_chapter_set_video" json:"video_id"`
	TitleSnapshot   string    `gorm:"type:text;not null" json:"title_snapshot"`
	SourceSize      int64     `gorm:"not null" json:"-"`
	SourceModTimeNS int64     `gorm:"not null" json:"-"`
	Revision        uint64    `gorm:"not null" json:"revision"`
	CreatedAt       time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt       time.Time `json:"updated_at" ts_type:"string"`
}
type VideoChapter struct {
	ID          uint       `gorm:"primarykey" json:"id"`
	SetID       uint       `gorm:"not null;uniqueIndex:idx_chapter_position,priority:1" json:"set_id"`
	Set         ChapterSet `gorm:"foreignKey:SetID;constraint:OnDelete:CASCADE;" json:"-"`
	Position    int        `gorm:"not null;uniqueIndex:idx_chapter_position,priority:2" json:"position"`
	StartMS     int64      `gorm:"not null;check:chk_chapter_range,start_ms >= 0 AND end_ms > start_ms" json:"start_ms"`
	EndMS       int64      `gorm:"not null" json:"end_ms"`
	Title       string     `gorm:"type:text;not null" json:"title"`
	Summary     string     `gorm:"type:text;not null;default:''" json:"summary"`
	OriginJobID *uint      `json:"origin_job_id"`
}

// ChapterDraftJob's request identity survives compaction and backend migration.
// The subtitle input is never persisted; DraftJSON contains only output/evidence positions.
type ChapterDraftJob struct {
	ID                      uint      `gorm:"primarykey" json:"id"`
	RequestID               string    `gorm:"not null;uniqueIndex:idx_chapter_request" json:"-"`
	RequestDigest           string    `gorm:"not null" json:"-"`
	VideoID                 *uint     `gorm:"index:idx_chapter_job_video,priority:1" json:"video_id"`
	TitleSnapshot           string    `gorm:"type:text;not null;default:''" json:"title_snapshot"`
	Status                  string    `gorm:"not null;index:idx_chapter_job_state" json:"status"`
	Revision                uint64    `gorm:"not null" json:"revision"`
	Attempt                 uint64    `gorm:"not null;default:0" json:"attempt"`
	SourceSize              int64     `gorm:"not null;default:0" json:"-"`
	SourceModTimeNS         int64     `gorm:"not null;default:0" json:"-"`
	SubtitleFingerprint     string    `gorm:"not null;default:''" json:"-"`
	ExpectedChapterRevision uint64    `gorm:"not null;default:0" json:"-"`
	ProviderRevision        string    `gorm:"not null;default:''" json:"-"`
	Model                   string    `gorm:"type:text;not null;default:''" json:"model"`
	ScopeStartMS            int64     `gorm:"not null;default:0" json:"scope_start_ms"`
	ScopeEndMS              int64     `gorm:"not null;default:0" json:"scope_end_ms"`
	Whole                   bool      `gorm:"not null;default:false" json:"whole"`
	CompletedChunks         int       `gorm:"not null;default:0" json:"completed_chunks"`
	TotalChunks             int       `gorm:"not null;default:0" json:"total_chunks"`
	DraftJSON               string    `gorm:"type:text;not null;default:''" json:"-"`
	ErrorCode               string    `gorm:"not null;default:''" json:"error_code"`
	Compacted               bool      `gorm:"not null;default:false" json:"compacted"`
	CreatedAt               time.Time `gorm:"index:idx_chapter_job_video,priority:2" json:"created_at" ts_type:"string"`
	UpdatedAt               time.Time `json:"updated_at" ts_type:"string"`
}
