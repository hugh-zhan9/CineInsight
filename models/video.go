package models

import (
	"time"

	"gorm.io/gorm"
)

// Video 视频文件模型
type Video struct {
	ID              uint           `gorm:"primarykey" json:"id"`
	Name            string         `json:"name"`                                                                    // 文件名
	Path            string         `gorm:"uniqueIndex:idx_videos_path_active,where:deleted_at IS NULL" json:"path"` // 完整路径
	Directory       string         `json:"directory"`                                                               // 所在目录
	Size            int64          `json:"size"`                                                                    // 文件大小（字节）
	Duration        float64        `json:"duration"`                                                                // 时长（秒）
	PlayCount       int            `gorm:"default:0" json:"play_count"`                                             // 播放次数
	RandomPlayCount int            `gorm:"default:0" json:"random_play_count"`                                      // 随机播放次数
	LastPlayedAt    *time.Time     `json:"last_played_at"`                                                          // 最后播放时间
	Tags            []Tag          `gorm:"many2many:video_tags;" json:"tags"`                                       // 标签（多对多）
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
}

// Tag 标签模型
type Tag struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	Name      string         `gorm:"unique" json:"name"` // 标签名称
	Color     string         `json:"color"`              // 标签颜色
	Videos    []Video        `gorm:"many2many:video_tags;" json:"-"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// Settings 应用设置
type Settings struct {
	ID                  uint      `gorm:"primarykey" json:"id"`
	ConfirmBeforeDelete bool      `json:"confirm_before_delete"`          // 删除前确认
	DeleteOriginalFile  bool      `json:"delete_original_file"`           // 是否删除原始文件
	VideoExtensions     string    `json:"video_extensions"`               // 支持的视频格式（逗号分隔）
	PlayWeight          float64   `gorm:"default:2.0" json:"play_weight"` // 播放权重（1次播放 = N次随机播放）
	AutoScanOnStartup   bool      `json:"auto_scan_on_startup"`           // 启动时自动增量扫描
	LogEnabled          bool      `json:"log_enabled"`                    // 是否启用日志
	UpdatedAt           time.Time `json:"updated_at"`
}

// ScanDirectory 扫描目录配置
type ScanDirectory struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	Path      string         `json:"path"`  // 目录路径
	Alias     string         `json:"alias"` // 目录别名
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}
