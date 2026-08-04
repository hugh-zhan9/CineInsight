package models

import "time"

// VideoEnhancementTask 状态机（docs/loopx/design/2026-08-04-video-super-resolution）：
// queued -> running -> completed；queued/running -> cancel_requested -> cancelled；
// queued/running -> failed。每个源视频同时最多一个活跃任务（部分唯一索引，
// 见 database 迁移）。任务表是唯一事实源，崩溃续跑与启动对账都以它为准。
const (
	EnhancementStatusQueued          = "queued"
	EnhancementStatusRunning         = "running"
	EnhancementStatusCancelRequested = "cancel_requested"
	EnhancementStatusCancelled       = "cancelled"
	EnhancementStatusCompleted       = "completed"
	EnhancementStatusFailed          = "failed"

	EnhancementProfileGeneral = "general"
	EnhancementProfileAnime   = "anime"

	EnhancementPhasePreflight = "preflight"
	EnhancementPhaseExtract   = "extract"
	EnhancementPhaseEnhance   = "enhance"
	EnhancementPhaseEncode    = "encode"
	EnhancementPhasePublish   = "publish"
	EnhancementPhaseVerify    = "verify"
)

// VideoEnhancementTask 是持久化的超分任务记录。
type VideoEnhancementTask struct {
	ID      uint  `gorm:"primarykey" json:"id"`
	VideoID uint  `gorm:"not null;index" json:"video_id"`
	Video   Video `gorm:"constraint:OnUpdate:CASCADE,OnDelete:RESTRICT;" json:"-"`

	Profile string `gorm:"size:16;not null" json:"profile"`
	Scale   int    `gorm:"not null;default:2" json:"scale"`

	Status string `gorm:"size:24;not null;index" json:"status"`
	Phase  string `gorm:"size:16;not null;default:'preflight'" json:"phase"`

	// 创建时记录 size/mtime；preflight 补算并固化 SHA-256。
	SourceSize      int64  `gorm:"not null" json:"-"`
	SourceModTimeNS int64  `gorm:"not null" json:"-"`
	SourceSHA256    string `gorm:"size:64;not null;default:''" json:"-"`

	RuntimeVersion string `gorm:"size:64;not null;default:''" json:"runtime_version"`
	ModelVersion   string `gorm:"size:64;not null;default:''" json:"model_version"`
	OutputBasename string `gorm:"size:255;not null" json:"output_basename"`

	TotalFrames     int64 `gorm:"not null;default:0" json:"total_frames"`
	CommittedFrames int64 `gorm:"not null;default:0" json:"committed_frames"`

	ErrorCode    string `gorm:"size:32;not null;default:''" json:"error_code"`
	ErrorSummary string `gorm:"type:text;not null;default:''" json:"error_summary"`

	OutputVideoID *uint `gorm:"index" json:"output_video_id,omitempty"`
	RelationID    *uint `json:"relation_id,omitempty"`

	StartedAt  *time.Time `json:"started_at,omitempty" ts_type:"string"`
	FinishedAt *time.Time `json:"finished_at,omitempty" ts_type:"string"`
	CreatedAt  time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt  time.Time  `json:"updated_at" ts_type:"string"`
}
