package models

import "time"

// ImageAITagCandidate 镜像 AITagCandidate 的字段与索引形态，承载图片侧未确认的 AI 标签建议。
// 图片侧是单轮请求，没有 run 历史表，因此不带 RunID/Run。
type ImageAITagCandidate struct {
	ID            uint   `gorm:"primarykey" json:"id"`
	ImageID       uint   `gorm:"index:idx_image_ai_tag_candidates_image_status,priority:1" json:"image_id"`
	Image         Image  `gorm:"constraint:OnDelete:CASCADE;" json:"image"`
	SuggestedName string `gorm:"not null" json:"suggested_name"`
	// 部分唯一索引 (image_id, normalized_name) WHERE status='pending' 由
	// database.ensureImageAITaggingIndexes 建：GORM 标签表达不了带 WHERE 的唯一索引。
	NormalizedName string     `gorm:"index" json:"normalized_name"`
	MatchedTagID   *uint      `gorm:"index:idx_image_ai_tag_candidates_matched_status,priority:1" json:"matched_tag_id,omitempty"`
	MatchedTag     *Tag       `json:"matched_tag,omitempty"`
	Confidence     string     `gorm:"index;not null" json:"confidence"`
	Reasoning      string     `gorm:"type:text" json:"reasoning"`
	SourceSummary  string     `gorm:"type:text" json:"source_summary"`
	Status         string     `gorm:"index:idx_image_ai_tag_candidates_image_status,priority:2;index:idx_image_ai_tag_candidates_matched_status,priority:2;index:idx_image_ai_tag_candidates_status_approved,priority:1;index:idx_image_ai_tag_candidates_status_rejected,priority:1;not null;default:'pending'" json:"status"`
	CreatedAt      time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt      time.Time  `json:"updated_at" ts_type:"string"`
	ApprovedAt     *time.Time `gorm:"index:idx_image_ai_tag_candidates_status_approved,priority:2" json:"approved_at,omitempty" ts_type:"string"`
	RejectedAt     *time.Time `gorm:"index:idx_image_ai_tag_candidates_status_rejected,priority:2" json:"rejected_at,omitempty" ts_type:"string"`
}

// TableName 固定表名为设计契约的 image_ai_tag_candidates；GORM 默认复数化会把
// ImageAITagCandidate 错误拆分为 image_a_itag_candidates。
func (ImageAITagCandidate) TableName() string {
	return "image_ai_tag_candidates"
}

// ImageAITagApprovalRecord 记录哪些 image_tags 关联是 AI 候选被接受产生的，
// 镜像 AITagApprovalRecord：(image_id, tag_id) 唯一、candidate_id 唯一。
// 审批逻辑靠它反推手工标签，不能省。
type ImageAITagApprovalRecord struct {
	ID          uint                `gorm:"primarykey" json:"id"`
	ImageID     uint                `gorm:"uniqueIndex:idx_image_ai_tag_approval_image_tag,priority:1;index" json:"image_id"`
	Image       Image               `gorm:"constraint:OnDelete:CASCADE;" json:"image"`
	TagID       uint                `gorm:"uniqueIndex:idx_image_ai_tag_approval_image_tag,priority:2;index" json:"tag_id"`
	Tag         Tag                 `gorm:"constraint:OnDelete:CASCADE;" json:"tag"`
	CandidateID uint                `gorm:"uniqueIndex" json:"candidate_id"`
	Candidate   ImageAITagCandidate `gorm:"constraint:OnDelete:CASCADE;" json:"candidate"`
	CreatedAt   time.Time           `json:"created_at" ts_type:"string"`
}

// TableName 固定表名为设计契约的 image_ai_tag_approval_records；理由同 ImageAITagCandidate。
func (ImageAITagApprovalRecord) TableName() string {
	return "image_ai_tag_approval_records"
}

// ImageAITaggingState 镜像 AITaggingState，跟踪单图打标的幂等证据与跳过/重试原因。
type ImageAITaggingState struct {
	ID                  uint       `gorm:"primarykey" json:"id"`
	ImageID             uint       `gorm:"uniqueIndex" json:"image_id"`
	Image               Image      `gorm:"constraint:OnDelete:CASCADE;" json:"image"`
	Status              string     `gorm:"index:idx_image_ai_tagging_states_status_processed,priority:1;not null;default:'pending'" json:"status"`
	SkipReason          string     `json:"skip_reason"`
	EvidenceFingerprint string     `gorm:"index" json:"evidence_fingerprint"`
	AttemptCount        int        `gorm:"default:0" json:"attempt_count"`
	LastError           string     `gorm:"type:text" json:"last_error"`
	LastProcessedAt     *time.Time `gorm:"index:idx_image_ai_tagging_states_status_processed,priority:2" json:"last_processed_at,omitempty" ts_type:"string"`
	CreatedAt           time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt           time.Time  `json:"updated_at" ts_type:"string"`
}

// TableName 固定表名为设计契约的 image_ai_tagging_states；理由同 ImageAITagCandidate。
func (ImageAITaggingState) TableName() string {
	return "image_ai_tagging_states"
}
