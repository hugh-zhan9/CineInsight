package models

import (
	"time"
)

// Video 视频文件模型
type Video struct {
	ID              uint           `gorm:"primarykey" json:"id"`
	Name            string         `json:"name"`                                                                    // 文件名
	Path            string         `gorm:"uniqueIndex:idx_videos_path_active,where:deleted_at IS NULL" json:"path"` // 完整路径
	Directory       string         `json:"directory"`                                                               // 所在目录
	Size            int64          `json:"size"`                                                                    // 文件大小（字节）
	Duration        float64        `json:"duration"`                                                                // 时长（秒）
	Resolution      string         `json:"resolution"`                                                              // 分辨率 (如 1920x1080)
	Width           int            `json:"width"`                                                                   // 宽度
	Height          int            `json:"height"`                                                                  // 高度
	IsStale         bool           `gorm:"default:false" json:"is_stale"`                                           // 当前路径是否失效/待纠偏
	PlayCount       int            `gorm:"default:0" json:"play_count"`                                             // 播放次数
	RandomPlayCount int            `gorm:"default:0" json:"random_play_count"`                                      // 随机播放次数
	LastPlayedAt    *time.Time     `json:"last_played_at" ts_type:"string"`                                         // 最后播放时间
	Tags            []Tag          `gorm:"many2many:video_tags;" json:"tags"`                                       // 标签（多对多）
	CreatedAt       time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt       time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt       SoftDeleteTime `gorm:"index" json:"-"`
}

// SubtitleSegment stores searchable SRT segments for fast subtitle lookup.
type SubtitleSegment struct {
	ID              uint      `gorm:"primarykey" json:"id"`
	VideoID         uint      `gorm:"index;uniqueIndex:idx_subtitle_segments_video_index" json:"video_id"`
	Video           Video     `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	SegmentIndex    int       `gorm:"uniqueIndex:idx_subtitle_segments_video_index" json:"segment_index"`
	StartTimeMs     int64     `json:"start_time_ms"`
	EndTimeMs       int64     `json:"end_time_ms"`
	Text            string    `gorm:"type:text" json:"text"`
	SubtitlePath    string    `json:"subtitle_path"`
	SubtitleModTime int64     `gorm:"index" json:"subtitle_mod_time"`
	CreatedAt       time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt       time.Time `json:"updated_at" ts_type:"string"`
}

// SubtitleIndexState tracks whether a video's SRT file has been indexed.
type SubtitleIndexState struct {
	ID              uint      `gorm:"primarykey" json:"id"`
	VideoID         uint      `gorm:"uniqueIndex" json:"video_id"`
	Video           Video     `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	SubtitlePath    string    `json:"subtitle_path"`
	SubtitleModTime int64     `gorm:"index" json:"subtitle_mod_time"`
	SubtitleSize    int64     `json:"subtitle_size"`
	SegmentCount    int       `json:"segment_count"`
	LastCheckedAt   time.Time `json:"last_checked_at" ts_type:"string"`
	CreatedAt       time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt       time.Time `json:"updated_at" ts_type:"string"`
}

// Tag 标签模型
type Tag struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	Name      string         `gorm:"unique" json:"name"` // 标签名称
	Color     string         `json:"color"`              // 标签颜色
	Videos    []Video        `gorm:"many2many:video_tags;" json:"-"`
	CreatedAt time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt SoftDeleteTime `gorm:"index" json:"-"`
}

// Settings 应用设置
type Settings struct {
	ID                  uint      `gorm:"primarykey" json:"id"`
	ConfirmBeforeDelete bool      `json:"confirm_before_delete"`              // 删除前确认
	DeleteOriginalFile  bool      `json:"delete_original_file"`               // 是否删除原始文件
	VideoExtensions     string    `json:"video_extensions"`                   // 支持的视频格式（逗号分隔）
	PlayWeight          float64   `gorm:"default:2.0" json:"play_weight"`     // 播放权重（1次播放 = N次随机播放）
	AutoScanOnStartup   bool      `json:"auto_scan_on_startup"`               // 启动时自动增量扫描
	Theme               string    `gorm:"default:'system'" json:"theme"`      // 主题模式: light, dark, system
	LogEnabled          bool      `json:"log_enabled"`                        // 是否启用日志
	BilingualEnabled    bool      `json:"bilingual_enabled"`                  // 是否开启双语字幕
	BilingualLang       string    `gorm:"default:'zh'" json:"bilingual_lang"` // 双语目标语言代码 (zh/ja/ko/fr/de/es)
	DeepLApiKey         string    `json:"deepl_api_key"`                      // DeepL API Key
	UpdatedAt           time.Time `json:"updated_at" ts_type:"string"`
}

// ScanDirectory 扫描目录配置
type ScanDirectory struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	Path      string         `json:"path"`  // 目录路径
	Alias     string         `json:"alias"` // 目录别名
	CreatedAt time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt SoftDeleteTime `gorm:"index" json:"-"`
}
