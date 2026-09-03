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
	IsLiked                bool           `gorm:"not null;default:false;index" json:"is_liked"`                            // 手机端点赞状态的投影；与 is_favorite 同构
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
	ID                    uint    `gorm:"primarykey" json:"id"`
	ConfirmBeforeDelete   bool    `json:"confirm_before_delete"` // 删除前确认
	DeleteOriginalFile    bool    `json:"delete_original_file"`  // 是否删除原始文件
	VideoExtensions       string  `json:"video_extensions"`      // 支持的视频格式（逗号分隔）
	ImageExtensions       string  `json:"image_extensions"`      // 支持的图片格式（逗号分隔），老库零值由使用方回退默认清单
	ScanExcludePaths      string  `gorm:"type:text" json:"scan_exclude_paths"`
	ImageScanExcludePaths string  `gorm:"type:text" json:"image_scan_exclude_paths"` // 图片扫描黑名单；空值回退共用 scan_exclude_paths（老库行为）
	PlayWeight            float64 `gorm:"default:2.0" json:"play_weight"`            // 播放权重（1次播放 = N次随机播放）
	RandomHalfLifeDays    int     `gorm:"not null;default:90" json:"random_half_life_days"`
	AutoScanOnStartup     bool    `json:"auto_scan_on_startup"`   // 启动时自动增量扫描
	LibraryWatchEnabled   bool    `json:"library_watch_enabled"`  // 实时同步片库
	LocalMetadataEnabled  bool    `json:"local_metadata_enabled"` // 新视频本地元数据自动填空与补全任务
	// 点「播放」时的续播口径。resume=交给播放器自己决定（IINA 会接着上次播），
	// restart=总是从头，restart_watched=已看的从头、没看完的接着播。
	PlaybackResumeMode string `gorm:"type:text;not null;default:'resume'" json:"playback_resume_mode"`

	// 扫描完成后自动接着跑的后台任务。默认全关：这些都吃 CPU/IO，
	// 什么时候跑该由用户决定，不能装完就替他占着机器。
	AutoTechnicalBackfill        bool       `json:"auto_technical_backfill"`  // 补全分辨率/时长等技术元数据
	AutoPerceptualHash           bool       `json:"auto_perceptual_hash"`     // 补全感知哈希（近似重复检测的前提）
	AutoCleanupAnalysis          bool       `json:"auto_cleanup_analysis"`    // 扫描后重算清理候选
	AutoImageEXIFBackfill        bool       `json:"auto_image_exif_backfill"` // 图片 EXIF/GPS 补全
	AutoCollectionSuggestions    bool       `json:"auto_collection_suggestions"`
	AIQualityEnabled             bool       `json:"ai_quality_enabled"` // 显示 AI 质量评估入口
	ShortFeedMaxDurationMinutes  int        `gorm:"default:5" json:"short_feed_max_duration_minutes"`
	ShortFeedFeedbackSyncEnabled bool       `gorm:"not null;default:true" json:"short_feed_feedback_sync_enabled"`
	Theme                        string     `gorm:"default:'system'" json:"theme"`      // 主题模式: light, dark, system
	LogEnabled                   bool       `json:"log_enabled"`                        // 是否启用日志
	BilingualEnabled             bool       `json:"bilingual_enabled"`                  // 是否开启双语字幕
	BilingualLang                string     `gorm:"default:'zh'" json:"bilingual_lang"` // 双语目标语言代码 (zh/ja/ko/fr/de/es)
	DeepLApiKey                  string     `json:"deepl_api_key"`                      // DeepL API Key
	SubtitleTranslationProvider  string     `gorm:"default:'deepl'" json:"subtitle_translation_provider"`
	SubtitleTranslationBaseURL   string     `json:"subtitle_translation_base_url"`
	SubtitleTranslationAPIKey    string     `json:"subtitle_translation_api_key"`
	SubtitleTranslationModel     string     `json:"subtitle_translation_model"`
	SubtitleWhisperXModel        string     `gorm:"default:'medium'" json:"subtitle_whisperx_model"`
	SubtitleWhisperXBatchSize    int        `gorm:"default:8" json:"subtitle_whisperx_batch_size"`
	AITaggingBaseURL             string     `json:"ai_tagging_base_url"` // OpenAI 兼容接口地址
	AITaggingAPIKey              string     `json:"ai_tagging_api_key"`  // AI 标签 API Key
	AITaggingModel               string     `json:"ai_tagging_model"`    // AI 标签模型
	SemanticEmbeddingModel       string     `gorm:"type:text;not null;default:''" json:"semantic_embedding_model"`
	AITaggingFrameCount          int        `gorm:"default:0" json:"ai_tagging_frame_count"` // 兼容旧设置；抽帧现按视频时长自动规划
	AITaggingImagesPerRequest    int        `gorm:"default:10" json:"ai_tagging_images_per_request"`
	AITaggingSubtitleCharLimit   int        `gorm:"default:4000" json:"ai_tagging_subtitle_char_limit"`
	AITaggingStartupBatchSize    int        `gorm:"default:10" json:"ai_tagging_startup_batch_size"`
	AITaggingMaxExtraFrames      int        `gorm:"default:20" json:"ai_tagging_max_extra_frames"`
	BackupDirectory              string     `gorm:"type:text;not null;default:''" json:"backup_directory"`
	BackupRetentionCount         int        `gorm:"not null;default:7" json:"backup_retention_count"`
	BackupIntervalHours          int        `gorm:"not null;default:24" json:"backup_interval_hours"`
	BackupLastAttemptAt          *time.Time `json:"backup_last_attempt_at" ts_type:"string"`
	BackupLastSuccessAt          *time.Time `json:"backup_last_success_at" ts_type:"string"`
	BackupLastError              string     `gorm:"type:text;not null;default:''" json:"backup_last_error"`
	// 后台任务空闲调度（D-032）。只挡自动触发的任务，用户显式启动永远不受影响。
	//
	// 这一列有意不带 gorm default：默认值为 true 的布尔列会被 GORM 的 Create 当成
	// "零值即未设置"跳过，双向迁移器的 Unscoped().Create 会把用户关掉的开关翻回 true。
	// 默认开由两处显式保证：新库在 ApplySchema 里带值插入，老库由
	// database.migrateIdleSchedulingSetting 显式刷成 true。
	IdleSchedulingEnabled bool   `json:"idle_scheduling_enabled"`
	IdleThresholdMinutes  int    `gorm:"not null;default:5" json:"idle_threshold_minutes"`
	IdleRequireACPower    bool   `gorm:"not null;default:false" json:"idle_require_ac_power"`
	IdleWindowStart       string `gorm:"type:text;not null;default:''" json:"idle_window_start"`
	IdleWindowEnd         string `gorm:"type:text;not null;default:''" json:"idle_window_end"`

	// 桌面通知与 Dock 角标（D-013）。仅 macOS 生效，其他平台是空实现。
	//
	// 与 IdleSchedulingEnabled 同理，这一列也不带 gorm default 标签：默认值为 true
	// 的布尔列会被 GORM 的 Create 当成"零值即未设置"跳过，双向迁移器的
	// Unscoped().Create 会把用户关掉的开关翻回 true。默认开由新库在 ApplySchema 里
	// 带值插入、老库由 database.migrateDesktopNotificationsSetting 显式刷成 true。
	DesktopNotificationsEnabled bool `json:"desktop_notifications_enabled"`

	// 兼容性转封装代理（D-005、D-006）。
	//
	// AutoCompatibilityProxy 默认关，零值即默认，不需要迁移。
	// ProxyCacheLimitBytes 默认 50 GiB，但同样**不带 gorm default 标签**：0 在这里
	// 是"不限"这个有意义的取值，带上默认标签之后 GORM 的 Create 会把它当成未设置，
	// 双向迁移器就会把用户设的"不限"翻回 50 GiB。默认值由新库在 ApplySchema 里
	// 带值插入、老库由 database.migrateProxyCacheLimitSetting 显式刷出。
	AutoCompatibilityProxy bool  `json:"auto_compatibility_proxy"`
	ProxyCacheLimitBytes   int64 `json:"proxy_cache_limit_bytes"`

	// 人脸识别（D-016、D-022）。两列的默认值都是零值，因此不需要显式迁移，
	// 也不用（更不能用）gorm default 标签。
	//
	// AutoFaceAnalysis 默认关：人脸分析是重任务，且它产出的是人物候选，
	// 装完就替用户跑一遍并不合适。FaceModelMirrorURL 为空时用 manifest 里的
	// 官方地址，非空时替换其 URL 前缀（本机直连 GitHub 未必通）。
	AutoFaceAnalysis   bool   `json:"auto_face_analysis"`
	FaceModelMirrorURL string `gorm:"type:text;not null;default:''" json:"face_model_mirror_url"`

	// 帧哈希序列（D-026）。默认关，零值即默认，不需要显式迁移，也不用（更不能用）
	// gorm default 标签。回填要逐帧抽整部片，是本应用里最吃 CPU 的一类任务，
	// 什么时候跑该由用户决定。
	AutoFrameHashSequence bool `json:"auto_frame_hash_sequence"`

	// 浏览器插件桥接（D-B03、D-B05、D-B06）。
	//
	// BrowserBridgeEnabled 默认关，零值即默认，不需要显式迁移，也不用（更不能用）
	// gorm default 标签：这条通道会让桌面端按外部请求去取任意地址并往磁盘写文件，
	// 该由用户显式打开。
	//
	// BrowserBridgeToken 是配对凭据，由桌面端生成、用户复制进插件；为空时桥接
	// 不启动。BrowserDownloadDirectory 为空时拒绝建任务——不替用户挑落盘目录。
	//
	// BrowserDownloadConcurrency 带 default 是安全的：0 在这里不是"不限"而是非法值，
	// 读取侧还会再归一化一次，因此不存在双向迁移器把用户设的合法值翻掉的情况。
	BrowserBridgeEnabled       bool   `json:"browser_bridge_enabled"`
	BrowserBridgeToken         string `gorm:"type:text;not null;default:''" json:"browser_bridge_token"`
	BrowserDownloadDirectory   string `gorm:"type:text;not null;default:''" json:"browser_download_directory"`
	BrowserDownloadConcurrency int    `gorm:"not null;default:2" json:"browser_download_concurrency"`

	UpdatedAt time.Time `json:"updated_at" ts_type:"string"`
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
