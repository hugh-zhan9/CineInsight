package models

import "time"

// 多版本聚合（D-MW-VERSIONS，2026-10-10）：用户确认建立的版本组。版本组只做汇总展示，
// 不拥有任何文件级字段（已看、进度、评分、收藏、标签、人物、字幕、播放计数仍按视频各自保存）。
// 三张表都没有非零 gorm default（GORM Create 会跳过零值，双向迁移器也会把它翻成默认值）。

// VideoVersionGroup 是一个版本组；Revision 是所有成员变更的 CAS 版本号。
type VideoVersionGroup struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	Title     string    `gorm:"size:200;not null" json:"title"`
	Revision  int64     `gorm:"not null" json:"revision"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time `json:"updated_at" ts_type:"string"`
}

// VideoVersionMember 以视频 ID 为主键，因此一个视频至多属于一个版本组。
// 视频软删除时成员行保留（恢复后自动回到组内），永久删除时随外键级联删除。
// Position 从 1 开始，最小的那个是主版本；(group_id, position) 只建普通索引，
// 重排在一个事务内整体改写，不需要唯一约束。
type VideoVersionMember struct {
	VideoID   uint              `gorm:"primaryKey;autoIncrement:false" json:"video_id"`
	Video     Video             `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	GroupID   uint              `gorm:"not null;index:idx_video_version_members_group_position,priority:1" json:"group_id"`
	Group     VideoVersionGroup `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Label     string            `gorm:"size:40;not null" json:"label"`
	Position  int               `gorm:"not null;check:chk_video_version_members_position,position > 0;index:idx_video_version_members_group_position,priority:2" json:"position"`
	CreatedAt time.Time         `json:"created_at" ts_type:"string"`
}

// VideoVersionSuggestionDismissal 记录用户忽略过的建议视频对（规范化为 low < high）。
// 不建外键：视频永久删除后这一行惰性滞留，无害。
type VideoVersionSuggestionDismissal struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	VideoLowID  uint      `gorm:"not null;uniqueIndex:idx_video_version_dismissal_pair,priority:1" json:"video_low_id"`
	VideoHighID uint      `gorm:"not null;uniqueIndex:idx_video_version_dismissal_pair,priority:2;check:chk_video_version_dismissal_order,video_low_id < video_high_id" json:"video_high_id"`
	CreatedAt   time.Time `json:"created_at" ts_type:"string"`
}
