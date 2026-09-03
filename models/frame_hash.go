package models

import "time"

// VideoFrameHashSequence 是一条视频按固定间隔抽出来的逐帧 dHash 序列（D-026）。
//
// 与三点感知哈希（VideoPerceptualHash）分工不同，两张表互不干涉：那张表回答
// "这两个文件是不是同一部片"，只取三个时间点；这张表回答"B 是不是从 A 里截下来
// 的一段"，必须有按时间排好的一整串哈希才能对齐偏移。
//
// Hashes 是连续的 uint64 大端字节流，8 字节一帧，不是文本：一部两小时的片按 2 秒
// 一帧是 3600 帧、28.8 KB，存成十六进制字符串会白涨一倍。帧数以 FrameCount 为准。
//
// IntervalMS 由代码写入（见 services 侧的 clipFrameIntervalMS），有意不挂 gorm
// default 标签：默认值写在标签上会让"0"这种非法值被静默翻成 2000，掩盖真正的
// 写入缺陷，双向迁移器的 Unscoped().Create 也会跳过零值字段。
//
// 源指纹（SourceSize + SourceModTimeNS）与缩略图、感知哈希同口径：不一致就视为
// 失效，序列不参与匹配，等回填任务重算。
type VideoFrameHashSequence struct {
	VideoID         uint      `gorm:"primaryKey;autoIncrement:false" json:"video_id"`
	Video           Video     `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	IntervalMS      int       `gorm:"column:interval_ms;not null" json:"interval_ms"`
	Hashes          []byte    `gorm:"not null" json:"-"`
	FrameCount      int       `gorm:"not null;default:0" json:"frame_count"`
	SourceSize      int64     `gorm:"not null" json:"source_size"`
	SourceModTimeNS int64     `gorm:"not null" json:"source_mod_time_ns"`
	ComputedAt      time.Time `gorm:"not null;index" json:"computed_at" ts_type:"string"`
	LastError       string    `gorm:"type:text;not null;default:''" json:"last_error"`
	CreatedAt       time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt       time.Time `json:"updated_at" ts_type:"string"`
}
