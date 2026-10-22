package models

import "time"

// RecommendationFeedback stores preferences only; target IDs are never reused by title.
// Logical targets deliberately have no FK so invalid preferences remain visible and removable.
type RecommendationFeedback struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	TargetKind string     `gorm:"size:20;not null;uniqueIndex:idx_recommendation_feedback_target,priority:1" json:"target_kind"`
	TargetID   uint       `gorm:"not null;uniqueIndex:idx_recommendation_feedback_target,priority:2" json:"target_id"`
	Effect     string     `gorm:"size:20;not null;uniqueIndex:idx_recommendation_feedback_target,priority:3;index:idx_recommendation_feedback_expiry,priority:1" json:"effect"`
	ExpiresAt  *time.Time `gorm:"index:idx_recommendation_feedback_expiry,priority:2" json:"expires_at" ts_type:"string"`
	Revision   int64      `gorm:"not null" json:"revision"`
	CreatedAt  time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt  time.Time  `json:"updated_at" ts_type:"string"`
}
