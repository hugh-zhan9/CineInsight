package models

import "time"

// CleanupConsolidationTask 只保存集中整理的标量状态；大份不可变计划和单项日志独立存储。
type CleanupConsolidationTask struct {
	ID         uint       `gorm:"primarykey" json:"id"`
	PreviewID  string     `gorm:"size:64;not null;uniqueIndex:idx_cleanup_consolidation_preview" json:"preview_id"`
	ActiveSlot *string    `gorm:"size:32;uniqueIndex:idx_cleanup_consolidation_active" json:"-"`
	OwnerScope string     `gorm:"size:64;not null" json:"-"`
	Status     string     `gorm:"size:24;not null;index:idx_cleanup_consolidation_status" json:"status"`
	Version    uint64     `gorm:"not null;default:1" json:"version"`
	Total      int        `gorm:"not null;default:0" json:"total"`
	Completed  int        `gorm:"not null;default:0" json:"completed"`
	BytesDone  int64      `gorm:"not null;default:0" json:"bytes_done"`
	BytesTotal int64      `gorm:"not null;default:0" json:"bytes_total"`
	Error      string     `gorm:"type:text;not null;default:''" json:"error"`
	CreatedAt  time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt  time.Time  `json:"updated_at" ts_type:"string"`
	FinishedAt *time.Time `json:"finished_at" ts_type:"string"`
}

// CleanupConsolidationTaskPlan 保存只写一次的确认计划，无媒体外键或级联删除。
type CleanupConsolidationTaskPlan struct {
	ID       uint   `gorm:"primarykey" json:"-"`
	TaskID   uint   `gorm:"not null;uniqueIndex:idx_cleanup_consolidation_plan_task" json:"-"`
	PlanJSON string `gorm:"type:text;not null" json:"-"`
}

// CleanupConsolidationTaskItem 保存一个视频家庭的版本化日志；Position 对应不可变计划。
// 普通 ID 主键支持现有迁移器的 FindInBatches。
type CleanupConsolidationTaskItem struct {
	ID          uint   `gorm:"primarykey" json:"-"`
	TaskID      uint   `gorm:"not null;uniqueIndex:idx_cleanup_consolidation_item_position,priority:1" json:"-"`
	Position    int    `gorm:"not null;uniqueIndex:idx_cleanup_consolidation_item_position,priority:2" json:"-"`
	JournalJSON string `gorm:"type:text;not null" json:"-"`
}
