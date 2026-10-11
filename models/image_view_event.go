package models

import "time"

// 图片查看事件的来源（观看回顾 D-RECAP-SOURCES）。
//
//   - desktop_viewer：桌面照片页查看器里同一张图连续显示满 1.5 秒；
//   - mobile_feed：手机端刷到图片，与互动表既有的浏览计数在同一处记录。
const (
	ImageViewSourceDesktopViewer = "desktop_viewer"
	ImageViewSourceMobileFeed    = "mobile_feed"
)

// ImageViewEvent 是一条图片查看流水，与 play_events 对称。
//
// 只追加：应用层没有 UPDATE / DELETE 路径。图片软删除时事件保留（回顾照样算它），
// 永久删除时随 images 级联清除。同一图片同一来源 30 分钟内只记一次，判定读数据库里
// 最近的事件而不是内存，重启后也不会灌水。迁移不回填：老库没有任何查看时间点，
// 手机端互动表里已有的 view_count 不合成事件。
type ImageViewEvent struct {
	ID uint `gorm:"primarykey" json:"id"`
	// 两个索引对应两条访问路径：按图片查最近一次查看（去重），按时间窗聚合回顾。
	ImageID   uint      `gorm:"not null;index:idx_image_view_events_image_viewed,priority:1" json:"image_id"`
	Image     Image     `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	ViewedAt  time.Time `gorm:"not null;index:idx_image_view_events_image_viewed,priority:2;index:idx_image_view_events_viewed_at" json:"viewed_at" ts_type:"string"`
	Source    string    `gorm:"size:16;not null" json:"source"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
}
