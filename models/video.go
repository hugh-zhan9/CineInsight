package models

import (
	"time"
)

// Video 视频文件模型
type Video struct {
	ID                     uint           `gorm:"primarykey" json:"id"`
	Name                   string         `json:"name"`                                               // 文件名
	DisplayTitle           string         `gorm:"size:255;not null;default:''" json:"display_title"`  // 用户维护的显示标题
	OriginalTitle          string         `gorm:"size:255;not null;default:''" json:"original_title"` // 用户维护的原始标题
	Description            string         `gorm:"type:text;not null;default:''" json:"description"`
	PosterPath             string         `gorm:"type:text;not null;default:''" json:"-"`
	FanartPath             string         `gorm:"type:text;not null;default:''" json:"-"`
	PersonalRating         *float64       `gorm:"type:numeric(3,1);check:chk_videos_personal_rating,personal_rating IS NULL OR (personal_rating >= 0 AND personal_rating <= 10 AND personal_rating * 2 = CAST(personal_rating * 2 AS INTEGER))" json:"personal_rating"`
	Path                   string         `gorm:"uniqueIndex:idx_videos_path_active,where:deleted_at IS NULL" json:"path"` // 完整路径
	Directory              string         `json:"directory"`                                                               // 所在目录
	Size                   int64          `json:"size"`                                                                    // 文件大小（字节）
	Duration               float64        `json:"duration"`                                                                // 时长（秒）
	Resolution             string         `json:"resolution"`                                                              // 分辨率 (如 1920x1080)
	Width                  int            `json:"width"`                                                                   // 宽度
	Height                 int            `json:"height"`                                                                  // 高度
	IsStale                bool           `gorm:"default:false" json:"is_stale"`                                           // 当前路径是否失效/待纠偏
	PlayCount              int            `gorm:"default:0" json:"play_count"`                                             // 播放次数
	RandomPlayCount        int            `gorm:"default:0" json:"random_play_count"`                                      // 随机播放次数
	LastPlayedAt           *time.Time     `json:"last_played_at" ts_type:"string"`                                         // 最后播放时间
	IsFavorite             bool           `gorm:"not null;default:false" json:"is_favorite"`                               // 主片库收藏状态
	IsWatched              bool           `gorm:"not null;default:false" json:"is_watched"`                                // 是否已看
	WatchPositionSeconds   float64        `gorm:"not null;default:0" json:"watch_position_seconds"`                        // 内嵌播放器观看位置（秒）
	WatchProgressUpdatedAt *time.Time     `json:"watch_progress_updated_at" ts_type:"string"`                              // 最近一次观看进度更新时间
	WatchedAt              *time.Time     `json:"watched_at" ts_type:"string"`                                             // 最近标记已看的时间
	Tags                   []Tag          `gorm:"many2many:video_tags;" json:"tags"`                                       // 标签（多对多）
	CreatedAt              time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt              time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt              SoftDeleteTime `gorm:"index" json:"-"`
}

// VideoTrashEntry 记录恢复软删除视频所需的信息。
type VideoTrashEntry struct {
	ID           uint      `gorm:"primarykey" json:"id"`
	VideoID      uint      `gorm:"uniqueIndex;not null" json:"video_id"`
	VideoName    string    `gorm:"not null" json:"video_name"`
	OriginalPath string    `gorm:"not null" json:"original_path"`
	TrashPath    string    `gorm:"uniqueIndex:idx_video_trash_entries_trash_path,where:trash_path <> ''" json:"trash_path"`
	FileMoved    bool      `gorm:"not null;default:false" json:"file_moved"`
	FileSize     int64     `gorm:"not null;default:0" json:"file_size"`
	FileModTime  int64     `gorm:"not null;default:0" json:"file_mod_time"`
	FileIdentity string    `json:"-"`
	FileSHA256   string    `json:"-"`
	State        string    `gorm:"not null;default:deleted;index" json:"state"`
	LastError    string    `json:"last_error"`
	CreatedAt    time.Time `gorm:"index" json:"created_at" ts_type:"string"`
	UpdatedAt    time.Time `json:"updated_at" ts_type:"string"`
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
	ID             uint           `gorm:"primarykey" json:"id"`
	Name           string         `gorm:"unique" json:"name"` // 标签名称
	Color          string         `json:"color"`              // 标签颜色
	Namespace      string         `gorm:"index" json:"namespace"`
	AutomaticKind  string         `gorm:"uniqueIndex:idx_tags_automatic_kind,where:automatic_kind <> ''" json:"automatic_kind"`
	IsSystem       bool           `gorm:"index;default:false" json:"is_system"`
	IsActive       bool           `gorm:"index;default:true" json:"is_active"`
	ReviewRequired bool           `gorm:"default:false" json:"review_required"`
	SortOrder      int            `json:"sort_order"`
	Videos         []Video        `gorm:"many2many:video_tags;" json:"-"`
	CreatedAt      time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt      time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt      SoftDeleteTime `gorm:"index" json:"-"`
}

// Settings 应用设置
type Settings struct {
	ID                          uint      `gorm:"primarykey" json:"id"`
	ConfirmBeforeDelete         bool      `json:"confirm_before_delete"` // 删除前确认
	DeleteOriginalFile          bool      `json:"delete_original_file"`  // 是否删除原始文件
	VideoExtensions             string    `json:"video_extensions"`      // 支持的视频格式（逗号分隔）
	ScanExcludePaths            string    `gorm:"type:text" json:"scan_exclude_paths"`
	PlayWeight                  float64   `gorm:"default:2.0" json:"play_weight"` // 播放权重（1次播放 = N次随机播放）
	AutoScanOnStartup           bool      `json:"auto_scan_on_startup"`           // 启动时自动增量扫描
	LibraryWatchEnabled         bool      `json:"library_watch_enabled"`          // 实时同步片库
	LocalMetadataEnabled        bool      `json:"local_metadata_enabled"`         // 新视频本地元数据自动填空与补全任务
	AIQualityEnabled            bool      `json:"ai_quality_enabled"`             // 显示 AI 质量评估入口
	ShortFeedMaxDurationMinutes int       `gorm:"default:5" json:"short_feed_max_duration_minutes"`
	Theme                       string    `gorm:"default:'system'" json:"theme"`      // 主题模式: light, dark, system
	LogEnabled                  bool      `json:"log_enabled"`                        // 是否启用日志
	BilingualEnabled            bool      `json:"bilingual_enabled"`                  // 是否开启双语字幕
	BilingualLang               string    `gorm:"default:'zh'" json:"bilingual_lang"` // 双语目标语言代码 (zh/ja/ko/fr/de/es)
	DeepLApiKey                 string    `json:"deepl_api_key"`                      // DeepL API Key
	SubtitleTranslationProvider string    `gorm:"default:'deepl'" json:"subtitle_translation_provider"`
	SubtitleTranslationBaseURL  string    `json:"subtitle_translation_base_url"`
	SubtitleTranslationAPIKey   string    `json:"subtitle_translation_api_key"`
	SubtitleTranslationModel    string    `json:"subtitle_translation_model"`
	SubtitleWhisperXModel       string    `gorm:"default:'medium'" json:"subtitle_whisperx_model"`
	SubtitleWhisperXBatchSize   int       `gorm:"default:8" json:"subtitle_whisperx_batch_size"`
	AITaggingBaseURL            string    `json:"ai_tagging_base_url"`                     // OpenAI 兼容接口地址
	AITaggingAPIKey             string    `json:"ai_tagging_api_key"`                      // AI 标签 API Key
	AITaggingModel              string    `json:"ai_tagging_model"`                        // AI 标签模型
	AITaggingFrameCount         int       `gorm:"default:0" json:"ai_tagging_frame_count"` // 兼容旧设置；抽帧现按视频时长自动规划
	AITaggingImagesPerRequest   int       `gorm:"default:10" json:"ai_tagging_images_per_request"`
	AITaggingSubtitleCharLimit  int       `gorm:"default:4000" json:"ai_tagging_subtitle_char_limit"`
	AITaggingStartupBatchSize   int       `gorm:"default:10" json:"ai_tagging_startup_batch_size"`
	AITaggingMaxExtraFrames     int       `gorm:"default:20" json:"ai_tagging_max_extra_frames"`
	UpdatedAt                   time.Time `json:"updated_at" ts_type:"string"`
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

// SavedLibraryView 保存用户命名的片库筛选条件。
type SavedLibraryView struct {
	ID         uint           `gorm:"primarykey" json:"id"`
	Name       string         `gorm:"not null;uniqueIndex:idx_saved_library_views_name_active,where:deleted_at IS NULL" json:"name"`
	SearchMode string         `gorm:"not null;default:'file'" json:"search_mode"`
	Keyword    string         `json:"keyword"`
	SmartView  string         `gorm:"index" json:"smart_view"`
	TagIDsJSON string         `gorm:"type:text;not null;default:'[]'" json:"tag_ids_json"`
	MinSize    int64          `json:"min_size"`
	MaxSize    int64          `json:"max_size"`
	MinHeight  int            `json:"min_height"`
	MaxHeight  int            `json:"max_height"`
	MinRating  *float64       `gorm:"type:numeric(3,1)" json:"min_rating"`
	MaxRating  *float64       `gorm:"type:numeric(3,1)" json:"max_rating"`
	SortMode   string         `gorm:"not null;default:'balanced'" json:"sort_mode"`
	CreatedAt  time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt  time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt  SoftDeleteTime `gorm:"index" json:"-"`
}
