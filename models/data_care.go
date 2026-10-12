package models

import "time"

// 数据维护四项（docs/loopx/design/2026-10-12-data-care）的新表。

// AutomationRule 是一条本地规则（D-RA-02）：媒体类型 + 一组同时满足的条件 + 一到多个动作。
// 条件与动作以 JSON 保存，结构与校验在 services 层（AutomationRuleService）。
// Enabled 没有 gorm default（默认 true 的布尔列禁带 default 标签），创建时显式写入。
type AutomationRule struct {
	ID             uint       `gorm:"primarykey" json:"id"`
	Name           string     `gorm:"size:100;not null" json:"name"`
	MediaKind      string     `gorm:"size:8;not null;index:idx_automation_rules_kind_position,priority:1" json:"media_kind"`
	Enabled        bool       `gorm:"not null" json:"enabled"`
	Position       int        `gorm:"not null;index:idx_automation_rules_kind_position,priority:2" json:"position"`
	ConditionsJSON string     `gorm:"type:text;not null" json:"conditions_json"`
	ActionsJSON    string     `gorm:"type:text;not null" json:"actions_json"`
	DisabledReason string     `gorm:"size:32;not null;default:''" json:"disabled_reason"`
	LastRunAt      *time.Time `json:"last_run_at" ts_type:"string"`
	CreatedAt      time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt      time.Time  `json:"updated_at" ts_type:"string"`
}

// AutomationRuleWrite 是规则账本（D-RA-03）：某条规则对某条媒体的某个目标处理过，每个组合至多一行。
// Inserted 表示这次是否真的插入了关系（关系本来就存在时也记一行，标 false）。
// MediaID 不挂外键：与人脸记账一样是多态媒体引用；媒体硬删时由失联清除与体检清理。
type AutomationRuleWrite struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	RuleID    uint           `gorm:"not null;uniqueIndex:idx_automation_rule_writes_identity,priority:1" json:"rule_id"`
	Rule      AutomationRule `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	MediaKind string         `gorm:"size:8;not null;uniqueIndex:idx_automation_rule_writes_identity,priority:2;index:idx_automation_rule_writes_media,priority:1" json:"media_kind"`
	MediaID   uint           `gorm:"not null;uniqueIndex:idx_automation_rule_writes_identity,priority:3;index:idx_automation_rule_writes_media,priority:2" json:"media_id"`
	Action    string         `gorm:"size:16;not null;uniqueIndex:idx_automation_rule_writes_identity,priority:4;index:idx_automation_rule_writes_target,priority:1" json:"action"`
	TargetID  uint           `gorm:"not null;uniqueIndex:idx_automation_rule_writes_identity,priority:5;index:idx_automation_rule_writes_target,priority:2" json:"target_id"`
	Inserted  bool           `gorm:"not null" json:"inserted"`
	CreatedAt time.Time      `json:"created_at" ts_type:"string"`
}

// MetadataBatchEdit 是一次批量编辑（D-MC-05），只保留最近 20 次，供撤销。
type MetadataBatchEdit struct {
	ID          uint       `gorm:"primarykey" json:"id"`
	Kind        string     `gorm:"size:8;not null" json:"kind"`
	SummaryJSON string     `gorm:"type:text;not null" json:"summary_json"`
	ItemCount   int        `gorm:"not null" json:"item_count"`
	CreatedAt   time.Time  `gorm:"index" json:"created_at" ts_type:"string"`
	UndoneAt    *time.Time `json:"undone_at" ts_type:"string"`
}

// MetadataBatchEditItem 记一次批量编辑里真正改变的（条目，字段）及改动前后的值。
// MediaID 不挂外键（视频与图片共用）；媒体硬删后撤销会跳过它，体检清理悬挂行。
type MetadataBatchEditItem struct {
	ID         uint              `gorm:"primarykey" json:"id"`
	BatchID    uint              `gorm:"not null;index" json:"batch_id"`
	Batch      MetadataBatchEdit `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	MediaID    uint              `gorm:"not null;index" json:"media_id"`
	Field      string            `gorm:"size:16;not null" json:"field"`
	BeforeJSON string            `gorm:"type:text;not null" json:"before_json"`
	AfterJSON  string            `gorm:"type:text;not null" json:"after_json"`
}

// DataHealthReport 是一次数据体检的结果（D-DH-04），只保留最近 10 份。
type DataHealthReport struct {
	ID          uint       `gorm:"primarykey" json:"id"`
	StartedAt   time.Time  `gorm:"not null;index" json:"started_at" ts_type:"string"`
	FinishedAt  *time.Time `json:"finished_at" ts_type:"string"`
	SummaryJSON string     `gorm:"type:text;not null" json:"summary_json"`
}
