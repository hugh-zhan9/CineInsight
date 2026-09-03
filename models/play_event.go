package models

import "time"

// 播放事件的来源。桌面端按是否随机分两种，手机端一种，
// legacy 是启动迁移从 videos.last_played_at 一次性回填出来的历史点。
const (
	PlayEventSourceDesktopPlay   = "desktop_play"
	PlayEventSourceDesktopRandom = "desktop_random"
	PlayEventSourceMobileFeed    = "mobile_feed"
	PlayEventSourceLegacy        = "legacy"
)

// PlayEvent 是一条播放流水。
//
// 只追加：应用层没有任何 UPDATE / DELETE 路径，行只会随视频被永久删除而级联消失。
// 视频软删除（进回收站）时事件保留，洞察聚合照样算它——「这一年我看了多少」不该
// 因为后来把片子扔进回收站就凭空少一截。
//
// 它与 videos.play_count / random_play_count / last_played_at 并存而不是取代它们：
// 那三列是随机算法的输入，账本不参与算法（见 ALGORITHM.md）。
type PlayEvent struct {
	ID uint `gorm:"primarykey" json:"id"`
	// 两个索引对应两条访问路径：按视频查它的播放史，按时间查热力图窗口。
	VideoID   uint      `gorm:"not null;index:idx_play_events_video_played,priority:1" json:"video_id"`
	Video     Video     `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	PlayedAt  time.Time `gorm:"not null;index:idx_play_events_video_played,priority:2;index:idx_play_events_played_at" json:"played_at" ts_type:"string"`
	Source    string    `gorm:"size:16;not null" json:"source"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
}
