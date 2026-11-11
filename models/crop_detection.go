package models

import "time"

// VideoCropDetectionState records the delivered result of one source version; ProjectID is deliberately a logical reference.
type VideoCropDetectionState struct {
	VideoID          uint      `gorm:"primaryKey;autoIncrement:false" json:"video_id"`
	Video            Video     `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	SourceVersion    string    `gorm:"not null" json:"source_version"`
	AlgorithmVersion int       `gorm:"not null" json:"algorithm_version"`
	Revision         uint64    `gorm:"not null" json:"revision"`
	Status           string    `gorm:"not null" json:"status"`
	ResultJSON       string    `gorm:"type:text;not null" json:"-"`
	ProjectID        *uint     `json:"project_id"`
	LastError        string    `gorm:"type:text;not null" json:"last_error"`
	CheckedAt        time.Time `json:"checked_at"`
}
