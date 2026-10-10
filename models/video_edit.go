package models

import "time"

// 视频工作台（视频编辑合同「状态模型」）的三种任务。
const (
	VideoEditKindMerge     = "merge"
	VideoEditKindTrimIntro = "trim_intro"
	VideoEditKindHDReplace = "hd_replace"
)

// 项目状态（合同状态图）。draft/analyzing 期间配方可改（analyzing 时只由分析写回），
// 排队之后配方只读。
const (
	VideoEditStatusDraft       = "draft"
	VideoEditStatusAnalyzing   = "analyzing"
	VideoEditStatusQueued      = "queued"
	VideoEditStatusRunning     = "running"
	VideoEditStatusCompleted   = "completed"
	VideoEditStatusPartial     = "partial"
	VideoEditStatusFailed      = "failed"
	VideoEditStatusCancelled   = "cancelled"
	VideoEditStatusInterrupted = "interrupted"
)

// 导出项状态：queued/running/completed/failed/cancelled/interrupted 与项目同名常量共用。

// 导出模式：精确（默认，必要时重编码）与快速（流复制，切点取关键帧）。
const (
	VideoEditModePrecise = "precise"
	VideoEditModeFast    = "fast"
)

// 导出项阶段。
const (
	VideoEditPhasePending = "pending"
	VideoEditPhaseCheck   = "check"
	VideoEditPhaseEncode  = "encode"
	VideoEditPhaseConcat  = "concat"
	VideoEditPhaseVerify  = "verify"
	VideoEditPhasePublish = "publish"
	VideoEditPhaseDone    = "done"
)

// VideoEditProject 是一个编辑项目：配方、分析结果与确认过的警告都以 JSON 存在本行。
// 所有数值列都没有 gorm default（2026-09-02 禁令）：新行由服务显式写 revision=1、mode=precise。
type VideoEditProject struct {
	ID               uint       `gorm:"primarykey;index:idx_video_edit_projects_status_id,priority:2" json:"id"`
	Kind             string     `gorm:"size:16;not null" json:"kind"`
	Title            string     `gorm:"size:200;not null" json:"title"`
	Status           string     `gorm:"size:16;not null;index:idx_video_edit_projects_status_id,priority:1" json:"status"`
	Revision         uint64     `gorm:"not null" json:"revision"`
	Mode             string     `gorm:"size:16;not null" json:"mode"`
	RecipeJSON       string     `gorm:"type:text;not null" json:"-"`
	AnalysisJSON     string     `gorm:"type:text;not null" json:"-"`
	AcknowledgedJSON string     `gorm:"type:text;not null" json:"-"`
	ErrorCode        string     `gorm:"size:64;not null" json:"error_code"`
	ErrorMessage     string     `gorm:"type:text;not null" json:"error_message"`
	CreatedAt        time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt        time.Time  `json:"updated_at" ts_type:"string"`
	QueuedAt         *time.Time `json:"queued_at" ts_type:"string"`
	FinishedAt       *time.Time `json:"finished_at" ts_type:"string"`
}

// VideoEditItem 是项目的一个导出项（merge/hd_replace 一项，trim_intro 每个来源一项）。
// plan_json 在排队时冻结；publish_target/staged_size 在 rename 之前写入，供启动对账判断
// rename 之后、入库事务之前崩溃的项。output_video_id 是逻辑引用（无外键），成品删除后保留历史。
type VideoEditItem struct {
	ID            uint             `gorm:"primarykey" json:"id"`
	ProjectID     uint             `gorm:"not null;uniqueIndex:idx_video_edit_items_project_seq,priority:1" json:"project_id"`
	Project       VideoEditProject `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	Seq           int              `gorm:"not null;uniqueIndex:idx_video_edit_items_project_seq,priority:2" json:"seq"`
	Status        string           `gorm:"size:16;not null;index:idx_video_edit_items_status" json:"status"`
	Phase         string           `gorm:"size:16;not null" json:"phase"`
	Progress      float64          `gorm:"not null" json:"progress"`
	PlanJSON      string           `gorm:"type:text;not null" json:"-"`
	OutputName    string           `gorm:"type:text;not null" json:"output_name"`
	PublishTarget string           `gorm:"type:text;not null" json:"-"`
	StagedSize    int64            `gorm:"not null" json:"-"`
	OutputPath    string           `gorm:"type:text;not null" json:"-"`
	OutputVideoID *uint            `json:"output_video_id"`
	WorkDir       string           `gorm:"type:text;not null" json:"-"`
	ErrorCode     string           `gorm:"size:64;not null" json:"error_code"`
	ErrorMessage  string           `gorm:"type:text;not null" json:"error_message"`
	StartedAt     *time.Time       `json:"started_at" ts_type:"string"`
	FinishedAt    *time.Time       `json:"finished_at" ts_type:"string"`
}
