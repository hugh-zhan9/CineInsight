package models

import "time"

// 产品完善度批次（2026-09-29）新增的七张表。schema 集中在这一个文件里一次落地，
// 其他切片只使用这些字段，不得再改 models/ 与 AllModels()（详细设计 Planning Handoff）。

// 迁移残留的状态（migration_staged_sources.state，D-PC05）。
const (
	MigrationStagedStatePending = "pending"
	MigrationStagedStateCleaned = "cleaned"
)

// MigrationStagedSource 记录文件迁移时留下的暂存源文件（.<名>.cineinsight-migrating-<hex>）。
//
// 不建外键：视频可能已被永久删除，而暂存文件仍要能被列出和清理。
type MigrationStagedSource struct {
	ID           uint       `gorm:"primarykey" json:"id"`
	VideoID      *uint      `gorm:"index" json:"video_id"`
	OriginalPath string     `gorm:"type:text;not null;default:''" json:"original_path"`
	StagedPath   string     `gorm:"not null;uniqueIndex:idx_migration_staged_sources_staged_path" json:"staged_path"`
	Size         int64      `gorm:"not null;default:0" json:"size"`
	State        string     `gorm:"size:16;not null;default:'pending';index" json:"state"`
	CleanedAt    *time.Time `json:"cleaned_at" ts_type:"string"`
	CreatedAt    time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt    time.Time  `json:"updated_at" ts_type:"string"`
}

// SubtitleJob 是字幕任务的落库记录（D-PC20），只保留最近 100 条终态行，
// 每次写入终态后由服务层裁剪。
type SubtitleJob struct {
	ID                  uint       `gorm:"primarykey;index:idx_subtitle_jobs_status_id,priority:2" json:"id"`
	VideoID             uint       `gorm:"not null;index" json:"video_id"`
	Video               Video      `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Engine              string     `gorm:"size:32;not null;default:''" json:"engine"`
	SourceLang          string     `gorm:"size:16;not null;default:''" json:"source_lang"`
	OptionsJSON         string     `gorm:"type:text;not null;default:''" json:"options_json"`
	Status              string     `gorm:"size:24;not null;default:'';index:idx_subtitle_jobs_status_id,priority:1" json:"status"`
	Message             string     `gorm:"type:text;not null;default:''" json:"message"`
	PendingArtifactPath string     `gorm:"type:text;not null;default:''" json:"pending_artifact_path"`
	StartedAt           *time.Time `json:"started_at" ts_type:"string"`
	FinishedAt          *time.Time `json:"finished_at" ts_type:"string"`
	CreatedAt           time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt           time.Time  `json:"updated_at" ts_type:"string"`
}

// 标签转人物记录的状态（tag_person_conversions.state，D-PC34）。
const (
	TagPersonConversionApplied = "applied"
	TagPersonConversionUndone  = "undone"
)

// TagPersonConversion 记录一次「标签转人物」，让它可以被完整撤销。
//
// 不建外键：标签是软删的，人物可能在转换之后被用户删掉，撤销时要能读到这条记录
// 并据此判断该还原什么。
type TagPersonConversion struct {
	ID                      uint       `gorm:"primarykey" json:"id"`
	TagID                   uint       `gorm:"not null;index" json:"tag_id"`
	PersonID                uint       `gorm:"not null" json:"person_id"`
	PersonCreated           bool       `gorm:"not null;default:false" json:"person_created"`
	VideoIDsJSON            string     `gorm:"type:text;not null;default:'[]'" json:"video_ids_json"`
	ImageIDsJSON            string     `gorm:"type:text;not null;default:'[]'" json:"image_ids_json"`
	AddedVideoPersonIDsJSON string     `gorm:"type:text;not null;default:'[]'" json:"added_video_person_ids_json"`
	AddedImagePersonIDsJSON string     `gorm:"type:text;not null;default:'[]'" json:"added_image_person_ids_json"`
	State                   string     `gorm:"size:16;not null;default:'applied'" json:"state"`
	UndoneAt                *time.Time `json:"undone_at" ts_type:"string"`
	CreatedAt               time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt               time.Time  `json:"updated_at" ts_type:"string"`
}

// MovieVideoLink 是榜单条目（豆瓣 ID）与片库视频的关联（D-PC52）。
// 按豆瓣 ID 字符串逻辑关联榜单缓存表，理由同 movie_chart.go：不建外键。
type MovieVideoLink struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	DoubanID  string    `gorm:"size:16;not null;uniqueIndex:idx_movie_video_links_pair,priority:1" json:"douban_id"`
	VideoID   uint      `gorm:"not null;uniqueIndex:idx_movie_video_links_pair,priority:2;index" json:"video_id"`
	Video     Video     `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time `json:"updated_at" ts_type:"string"`
}

// JellyfinSession 是 Jellyfin 兼容层签发的会话令牌（D-PC47）。只存令牌的 SHA-256 十六进制，
// 不存明文。
type JellyfinSession struct {
	ID         uint      `gorm:"primarykey" json:"id"`
	TokenHash  string    `gorm:"size:64;not null;uniqueIndex:idx_jellyfin_sessions_token_hash" json:"-"`
	DeviceID   string    `gorm:"type:text;not null;default:''" json:"device_id"`
	Client     string    `gorm:"type:text;not null;default:''" json:"client"`
	LastSeenAt time.Time `gorm:"not null" json:"last_seen_at" ts_type:"string"`
	ExpiresAt  time.Time `gorm:"not null;index" json:"expires_at" ts_type:"string"`
	CreatedAt  time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt  time.Time `json:"updated_at" ts_type:"string"`
}

// BrowserDownloadTask 是浏览器插件桥接下载任务的落库记录（D-PC21）。
//
// DisplayURL 只含 scheme+host+path，写入前必须去掉 query 与 fragment；请求头与 Cookie
// 一律不存。
type BrowserDownloadTask struct {
	ID         uint       `gorm:"primarykey" json:"id"`
	TaskUID    string     `gorm:"size:32;not null;uniqueIndex:idx_browser_download_tasks_task_uid" json:"task_uid"`
	DisplayURL string     `gorm:"type:text;not null;default:''" json:"display_url"`
	FileName   string     `gorm:"type:text;not null;default:''" json:"file_name"`
	Directory  string     `gorm:"type:text;not null;default:''" json:"directory"`
	Status     string     `gorm:"size:24;not null;default:'';index" json:"status"`
	Error      string     `gorm:"type:text;not null;default:''" json:"error"`
	FinishedAt *time.Time `json:"finished_at" ts_type:"string"`
	CreatedAt  time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt  time.Time  `json:"updated_at" ts_type:"string"`
}

// 清理忽略的类别（cleanup_video_dismissals.category，D-PC31）。
const (
	CleanupDismissalCategoryShort = "short"
	CleanupDismissalCategoryLow   = "low"
)

// CleanupVideoDismissal 是用户对「极短片段 / 极低分辨率」候选的忽略记录。Fingerprint 是忽略时的
// size:mtimeNS，文件变了忽略随之失效。
type CleanupVideoDismissal struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	VideoID     uint      `gorm:"not null;uniqueIndex:idx_cleanup_video_dismissals_video_category,priority:1" json:"video_id"`
	Video       Video     `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Category    string    `gorm:"size:16;not null;uniqueIndex:idx_cleanup_video_dismissals_video_category,priority:2" json:"category"`
	Fingerprint string    `gorm:"size:64;not null;default:''" json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt   time.Time `json:"updated_at" ts_type:"string"`
}
