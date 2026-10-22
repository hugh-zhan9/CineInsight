package models

import "time"

// TaskFailureRun is a diagnostic interval owned by an existing worker.
type TaskFailureRun struct {
	ID              uint       `gorm:"primaryKey;index:idx_task_failure_run_task_id,priority:2" json:"id"`
	TaskKey         string     `gorm:"size:64;not null;index:idx_task_failure_run_task_id,priority:1" json:"task_key"`
	StartedAt       time.Time  `json:"started_at" ts_type:"string"`
	FinishedAt      *time.Time `json:"finished_at" ts_type:"string"`
	State           string     `gorm:"size:16;not null;index" json:"state"`
	RecordsComplete bool       `gorm:"not null" json:"records_complete"`
}

// TaskFailureItem is append-only; MediaID intentionally has no media foreign key.
type TaskFailureItem struct {
	ID            uint           `gorm:"primaryKey;index:idx_task_failure_run_items,priority:2" json:"id"`
	RunID         uint           `gorm:"not null;uniqueIndex:idx_task_failure_attempt,priority:1;index:idx_task_failure_run_items,priority:1" json:"run_id"`
	Run           TaskFailureRun `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE" json:"-"`
	MediaKind     string         `gorm:"size:16;not null" json:"media_kind"`
	MediaID       uint           `gorm:"not null" json:"media_id"`
	AttemptKey    string         `gorm:"size:96;not null;uniqueIndex:idx_task_failure_attempt,priority:2" json:"attempt_key"`
	SourceVersion string         `gorm:"size:128;not null" json:"source_version"`
	StateVersion  string         `gorm:"size:128;not null" json:"state_version"`
	ErrorCode     string         `gorm:"size:64;not null" json:"error_code"`
	ErrorSummary  string         `gorm:"type:text;not null" json:"error_summary"`
	FailedAt      time.Time      `json:"failed_at" ts_type:"string"`
}
