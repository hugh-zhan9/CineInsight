package models

import "time"

// VideoEmbedding stores AI semantic embeddings for a video.
type VideoEmbedding struct {
	ID         uint           `gorm:"primarykey" json:"id"`
	VideoID    uint           `gorm:"uniqueIndex:idx_video_embeddings_unique,priority:1" json:"video_id"`
	Video      Video          `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	Model      string         `gorm:"size:128;uniqueIndex:idx_video_embeddings_unique,priority:2" json:"model"`
	Kind       string         `gorm:"size:32;uniqueIndex:idx_video_embeddings_unique,priority:3" json:"kind"`
	VectorJSON string         `gorm:"type:text" json:"vector_json"`
	Dimension  int            `json:"dimension"`
	Source     string         `gorm:"size:64" json:"source"`
	CreatedAt  time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt  time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt  SoftDeleteTime `gorm:"index" json:"-"`
}
