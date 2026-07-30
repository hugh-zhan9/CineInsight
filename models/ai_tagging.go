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

	AITagAgentActionFinalize       = "finalize"
	AITagAgentActionMoreFrames     = "request_more_frames"
	AITagAgentActionTranscript     = "request_transcript"
	AITagAgentActionFindSameSource = "find_same_source"

	AITagToolStatusNotRun   = "not_run"
	AITagToolStatusSuccess  = "success"
	AITagToolStatusPartial  = "partial"
	AITagToolStatusFailed   = "failed"
	AITagToolStatusRejected = "rejected"

	VideoSameSourceStatusDetected = "detected"
	VideoSameSourceStatusRejected = "rejected"
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

// AITagAgentStep stores one sanitized agent decision without raw media or transcript text.
type AITagAgentStep struct {
	ID                  uint      `gorm:"primarykey" json:"id"`
	VideoID             uint      `gorm:"index:idx_ai_tag_agent_steps_video_created,priority:1;uniqueIndex:idx_ai_tag_agent_step_run_round,priority:1" json:"video_id"`
	Video               Video     `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	EvidenceFingerprint string    `gorm:"size:64;uniqueIndex:idx_ai_tag_agent_step_run_round,priority:2" json:"evidence_fingerprint"`
	Attempt             int       `gorm:"uniqueIndex:idx_ai_tag_agent_step_run_round,priority:3" json:"attempt"`
	Round               int       `gorm:"uniqueIndex:idx_ai_tag_agent_step_run_round,priority:4" json:"round"`
	Action              string    `gorm:"size:32;not null" json:"action"`
	RequestedCount      int       `gorm:"not null;default:0" json:"requested_count"`
	ActualCount         int       `gorm:"not null;default:0" json:"actual_count"`
	ToolStatus          string    `gorm:"size:16;not null;default:'not_run'" json:"tool_status"`
	ObservationCode     string    `gorm:"size:64;not null;default:''" json:"observation_code"`
	DurationMS          int64     `gorm:"not null;default:0" json:"duration_ms"`
	FinishReason        string    `gorm:"size:64;not null;default:''" json:"finish_reason"`
	CreatedAt           time.Time `gorm:"index:idx_ai_tag_agent_steps_video_created,priority:2" json:"created_at" ts_type:"string"`
}

// VideoVisualFingerprint caches non-reversible visual hashes for same-source recall.
type VideoVisualFingerprint struct {
	ID                 uint      `gorm:"primarykey" json:"id"`
	VideoID            uint      `gorm:"uniqueIndex" json:"video_id"`
	Video              Video     `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	ContentFingerprint string    `gorm:"size:64;not null;index" json:"content_fingerprint"`
	AlgorithmVersion   string    `gorm:"size:64;not null" json:"algorithm_version"`
	Duration           float64   `gorm:"not null;default:0" json:"duration"`
	FrameHashesJSON    string    `gorm:"type:text;not null" json:"-"`
	SampleCount        int       `gorm:"not null;default:0" json:"sample_count"`
	CreatedAt          time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt          time.Time `json:"updated_at" ts_type:"string"`
}

// VideoSameSourceRelation stores a detected or user-rejected normalized video pair.
type VideoSameSourceRelation struct {
	ID                uint       `gorm:"primarykey" json:"id"`
	VideoAID          uint       `gorm:"uniqueIndex:idx_video_same_source_pair,priority:1;index:idx_video_same_source_a_status,priority:1" json:"video_a_id"`
	VideoA            Video      `gorm:"foreignKey:VideoAID;constraint:OnDelete:CASCADE;" json:"video_a"`
	VideoBID          uint       `gorm:"uniqueIndex:idx_video_same_source_pair,priority:2;index:idx_video_same_source_b_status,priority:1" json:"video_b_id"`
	VideoB            Video      `gorm:"foreignKey:VideoBID;constraint:OnDelete:CASCADE;" json:"video_b"`
	VideoAFingerprint string     `gorm:"size:64;not null" json:"-"`
	VideoBFingerprint string     `gorm:"size:64;not null" json:"-"`
	Status            string     `gorm:"size:16;not null;index:idx_video_same_source_unread,priority:1;index:idx_video_same_source_a_status,priority:2;index:idx_video_same_source_b_status,priority:2" json:"status"`
	Confidence        string     `gorm:"size:16;not null;default:''" json:"confidence"`
	Reasoning         string     `gorm:"type:text;not null;default:''" json:"reasoning"`
	DetectionVersion  string     `gorm:"size:64;not null" json:"detection_version"`
	IsUnread          bool       `gorm:"not null;default:true;index:idx_video_same_source_unread,priority:2" json:"is_unread"`
	RejectedAt        *time.Time `json:"rejected_at,omitempty" ts_type:"string"`
	CreatedAt         time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt         time.Time  `gorm:"index:idx_video_same_source_unread,priority:3" json:"updated_at" ts_type:"string"`
}
