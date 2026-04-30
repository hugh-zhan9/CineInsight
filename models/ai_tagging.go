package models

import "time"

const (
	AITagConfidenceHigh   = "high"
	AITagConfidenceMedium = "medium"
	AITagConfidenceLow    = "low"

	AITagCandidateStatusPending    = "pending"
	AITagCandidateStatusApproved   = "approved"
	AITagCandidateStatusRejected   = "rejected"
	AITagCandidateStatusSuperseded = "superseded"

	AITaggingStateStatusPending    = "pending"
	AITaggingStateStatusProcessing = "processing"
	AITaggingStateStatusCompleted  = "completed"
	AITaggingStateStatusSkipped    = "skipped"
	AITaggingStateStatusFailed     = "failed"
)

// AITagCandidate stores unconfirmed AI suggestions outside the canonical tag tables.
type AITagCandidate struct {
	ID             uint       `gorm:"primarykey" json:"id"`
	VideoID        uint       `gorm:"index:idx_ai_tag_candidates_video_status,priority:1" json:"video_id"`
	Video          Video      `gorm:"constraint:OnDelete:CASCADE;" json:"video"`
	SuggestedName  string     `gorm:"not null" json:"suggested_name"`
	NormalizedName string     `gorm:"index" json:"normalized_name"`
	MatchedTagID   *uint      `gorm:"index:idx_ai_tag_candidates_matched_status,priority:1" json:"matched_tag_id,omitempty"`
	MatchedTag     *Tag       `json:"matched_tag,omitempty"`
	Confidence     string     `gorm:"index;not null" json:"confidence"`
	Reasoning      string     `gorm:"type:text" json:"reasoning"`
	SourceSummary  string     `gorm:"type:text" json:"source_summary"`
	Status         string     `gorm:"index:idx_ai_tag_candidates_video_status,priority:2;index:idx_ai_tag_candidates_matched_status,priority:2;not null;default:'pending'" json:"status"`
	CreatedAt      time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt      time.Time  `json:"updated_at" ts_type:"string"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty" ts_type:"string"`
	RejectedAt     *time.Time `json:"rejected_at,omitempty" ts_type:"string"`
}

// AITagApprovalRecord records which official video/tag links were created by AI candidate approval.
type AITagApprovalRecord struct {
	ID          uint           `gorm:"primarykey" json:"id"`
	VideoID     uint           `gorm:"uniqueIndex:idx_ai_tag_approval_video_tag,priority:1;index" json:"video_id"`
	Video       Video          `gorm:"constraint:OnDelete:CASCADE;" json:"video"`
	TagID       uint           `gorm:"uniqueIndex:idx_ai_tag_approval_video_tag,priority:2;index" json:"tag_id"`
	Tag         Tag            `gorm:"constraint:OnDelete:CASCADE;" json:"tag"`
	CandidateID uint           `gorm:"uniqueIndex" json:"candidate_id"`
	Candidate   AITagCandidate `gorm:"constraint:OnDelete:CASCADE;" json:"candidate"`
	CreatedAt   time.Time      `json:"created_at" ts_type:"string"`
}

// AITaggingState tracks worker idempotency and why a video was skipped or retried.
type AITaggingState struct {
	ID                  uint       `gorm:"primarykey" json:"id"`
	VideoID             uint       `gorm:"uniqueIndex" json:"video_id"`
	Video               Video      `gorm:"constraint:OnDelete:CASCADE;" json:"video"`
	Status              string     `gorm:"index:idx_ai_tagging_states_status_processed,priority:1;not null;default:'pending'" json:"status"`
	SkipReason          string     `json:"skip_reason"`
	EvidenceFingerprint string     `gorm:"index" json:"evidence_fingerprint"`
	AttemptCount        int        `gorm:"default:0" json:"attempt_count"`
	LastError           string     `gorm:"type:text" json:"last_error"`
	LastProcessedAt     *time.Time `gorm:"index:idx_ai_tagging_states_status_processed,priority:2" json:"last_processed_at,omitempty" ts_type:"string"`
	CreatedAt           time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt           time.Time  `json:"updated_at" ts_type:"string"`
}
