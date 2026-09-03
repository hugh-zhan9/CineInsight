package models

import "time"

// 播放代理的两种编码策略（D-003）。
const (
	// PlaybackProxyStrategyRemux 只换容器不重编码：源已经是 H.264/HEVC + AAC/MP3。
	PlaybackProxyStrategyRemux = "remux"
	// PlaybackProxyStrategyTranscode 经 VideoToolbox 重编码到 H.264 + AAC。
	PlaybackProxyStrategyTranscode = "transcode"
)

// 代理任务的终态（D-001）。进行中不落表：表里出现一行就意味着这一轮已经跑完。
const (
	PlaybackProxyStatusReady  = "ready"
	PlaybackProxyStatusFailed = "failed"
)

// VideoPlaybackProxy 是一条兼容性转封装代理的元数据（D-001、D-002、D-005）。
//
// 代理本身是 ~/.CineInsight/proxies/ 下的派生文件，文件名由 video_id 与源指纹
// 推导，因此这里不存路径——存了就多一份会漂移的真相，而且日志纪律不许记路径全文。
//
// 源指纹是 size + mtime_ns，与缩略图、pHash、技术快照同一口径。预览与手机端在
// 命中代理之前都要 os.Stat 源文件比对一次；不一致就连文件带行一起删掉。
type VideoPlaybackProxy struct {
	ID      uint  `gorm:"primarykey" json:"id"`
	VideoID uint  `gorm:"not null;uniqueIndex:idx_video_playback_proxies_video" json:"video_id"`
	Video   Video `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	// SourceSize / SourceModTimeNS 是生成这份代理时源文件的指纹。
	SourceSize      int64 `gorm:"not null" json:"source_size"`
	SourceModTimeNS int64 `gorm:"not null" json:"source_mod_time_ns"`
	// Strategy 是 remux 或 transcode；Status 是 ready 或 failed。
	Strategy string `gorm:"size:16;not null" json:"strategy"`
	Status   string `gorm:"size:16;not null" json:"status"`
	// OutputSize 是产物字节数，LRU 求和用它；failed 行为 0。
	OutputSize int64 `gorm:"not null;default:0" json:"output_size"`
	// LastUsedAt 是 LRU 键：预览会话与手机端媒体请求命中时刷新（每视频 60 秒节流）。
	LastUsedAt time.Time `gorm:"not null;index:idx_video_playback_proxies_last_used" json:"last_used_at" ts_type:"string"`
	LastError  string    `gorm:"type:text;not null;default:''" json:"last_error"`
	CreatedAt  time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt  time.Time `json:"updated_at" ts_type:"string"`
}
