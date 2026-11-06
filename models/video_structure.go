package models

import "time"

// VideoStructureState owns resumable derived analysis, never user-authored chapters.
type VideoStructureState struct {
	ID                        uint      `gorm:"primarykey" json:"id"`
	VideoID                   uint      `gorm:"not null;uniqueIndex:idx_structure_video" json:"video_id"`
	Video                     Video     `gorm:"foreignKey:VideoID;constraint:OnDelete:CASCADE;" json:"-"`
	SourceSize                int64     `gorm:"not null;default:0" json:"-"`
	SourceModTimeNS           int64     `gorm:"not null;default:0" json:"-"`
	SourceVersion             string    `gorm:"not null;default:''" json:"source_version"`
	AlgorithmVersion          string    `gorm:"not null" json:"algorithm_version"`
	Revision                  uint64    `gorm:"not null" json:"revision"`
	Generation                uint64    `gorm:"not null" json:"generation"`
	Status                    string    `gorm:"not null;index:idx_structure_status,priority:1" json:"status"`
	Phase                     string    `gorm:"not null;default:'analyze'" json:"-"`
	DurationMS                int64     `gorm:"not null;default:0" json:"duration_ms"`
	CheckpointMS              int64     `gorm:"not null;default:0" json:"checkpoint_ms"`
	PendingFramePTS           int64     `gorm:"not null;default:0" json:"-"`
	PendingStartMS            int64     `gorm:"not null;default:0" json:"-"`
	PreviousSamplePTS         int64     `gorm:"not null;default:0" json:"-"`
	PreviousSample            []byte    `json:"-"`
	PublishedGeneration       *uint64   `json:"-"`
	PublishedSourceVersion    string    `gorm:"not null;default:''" json:"-"`
	PublishedAlgorithmVersion string    `gorm:"not null;default:''" json:"-"`
	RunToken                  string    `gorm:"not null;default:''" json:"-"`
	PauseReason               string    `gorm:"not null;default:''" json:"pause_reason"`
	ErrorCode                 string    `gorm:"not null;default:''" json:"error_code"`
	CreatedAt                 time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt                 time.Time `gorm:"index:idx_structure_status,priority:2" json:"updated_at" ts_type:"string"`
}

type VideoStructureSegment struct {
	ID                  uint                `gorm:"primarykey" json:"-"`
	StateID             uint                `gorm:"not null;uniqueIndex:idx_structure_segment,priority:1" json:"-"`
	State               VideoStructureState `gorm:"foreignKey:StateID;constraint:OnDelete:CASCADE;" json:"-"`
	Generation          uint64              `gorm:"not null;uniqueIndex:idx_structure_segment,priority:2" json:"-"`
	Position            int                 `gorm:"not null;uniqueIndex:idx_structure_segment,priority:3" json:"position"`
	StartMS             int64               `gorm:"not null;check:chk_structure_range,start_ms >= 0 AND end_ms > start_ms" json:"start_ms"`
	EndMS               int64               `gorm:"not null" json:"end_ms"`
	RepresentativeMS    int64               `gorm:"not null;check:chk_structure_representative,representative_ms >= start_ms AND representative_ms < end_ms" json:"representative_ms"`
	RepresentativeReady bool                `gorm:"not null;default:false" json:"-"`
	Basis               string              `gorm:"not null" json:"basis"`
	Score               float64             `gorm:"not null;default:0" json:"-"`
}
