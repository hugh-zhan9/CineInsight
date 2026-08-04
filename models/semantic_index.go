package models

import "time"

const (
	SemanticIndexAttemptPending    = "pending"
	SemanticIndexAttemptProcessing = "processing"
	SemanticIndexAttemptCompleted  = "completed"
	SemanticIndexAttemptFailed     = "failed"
	SemanticIndexAttemptCancelled  = "cancelled"
)

// SemanticIndexProfile records the only model and dimension allowed for active search.
type SemanticIndexProfile struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	ActiveModel    string     `gorm:"size:255;not null;default:'';index" json:"active_model"`
	Dimension      int        `gorm:"not null;default:0" json:"dimension"`
	Generation     int        `gorm:"not null;default:1" json:"generation"`
	NeedsRebuild   bool       `gorm:"not null;default:false" json:"needs_rebuild"`
	LastError      string     `gorm:"type:text;not null;default:''" json:"last_error"`
	DimensionSetAt *time.Time `json:"dimension_set_at,omitempty" ts_type:"string"`
	CreatedAt      time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt      time.Time  `json:"updated_at" ts_type:"string"`
}

// VideoSemanticIndex tracks successful vector persistence isolated by model and dimension.
type VideoSemanticIndex struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	VideoID            uint      `gorm:"uniqueIndex:idx_video_semantic_model_dimension,priority:1;index;not null" json:"video_id"`
	Video              Video     `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	ModelIdentifier    string    `gorm:"size:255;uniqueIndex:idx_video_semantic_model_dimension,priority:2;index:idx_semantic_model_dimension,priority:1;not null" json:"model_identifier"`
	Dimension          int       `gorm:"uniqueIndex:idx_video_semantic_model_dimension,priority:3;index:idx_semantic_model_dimension,priority:2;not null" json:"dimension"`
	Generation         int       `gorm:"not null;default:1;index" json:"generation"`
	ContentFingerprint string    `gorm:"size:64;not null;index" json:"content_fingerprint"`
	IndexedAt          time.Time `gorm:"not null;index" json:"indexed_at" ts_type:"string"`
	CreatedAt          time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt          time.Time `json:"updated_at" ts_type:"string"`
}

// SemanticIndexAttempt keeps resumable per-video errors without storing request payloads.
type SemanticIndexAttempt struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	VideoID            uint       `gorm:"uniqueIndex:idx_semantic_attempt_video_model_generation,priority:1;index;not null" json:"video_id"`
	Video              Video      `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	ModelIdentifier    string     `gorm:"size:255;uniqueIndex:idx_semantic_attempt_video_model_generation,priority:2;not null" json:"model_identifier"`
	Generation         int        `gorm:"uniqueIndex:idx_semantic_attempt_video_model_generation,priority:3;not null" json:"generation"`
	Status             string     `gorm:"size:32;not null;index" json:"status"`
	AttemptCount       int        `gorm:"not null;default:0" json:"attempt_count"`
	ContentFingerprint string     `gorm:"size:64;not null;default:''" json:"content_fingerprint"`
	Dimension          int        `gorm:"not null;default:0" json:"dimension"`
	ErrorCode          string     `gorm:"size:64;not null;default:''" json:"error_code"`
	LastError          string     `gorm:"type:text;not null;default:''" json:"last_error"`
	LastAttemptedAt    *time.Time `gorm:"index" json:"last_attempted_at,omitempty" ts_type:"string"`
	CompletedAt        *time.Time `json:"completed_at,omitempty" ts_type:"string"`
	CreatedAt          time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt          time.Time  `json:"updated_at" ts_type:"string"`
}
