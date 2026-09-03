package models

import "time"

// Person is a reusable locally managed actor identity.
type Person struct {
	ID           uint      `gorm:"primarykey" json:"id"`
	DisplayName  string    `gorm:"size:200;not null;index:idx_people_display_name" json:"display_name"`
	OriginalName string    `gorm:"size:200;not null;default:'';index:idx_people_original_name" json:"original_name"`
	AvatarPath   string    `gorm:"type:text;not null;default:''" json:"-"`
	CreatedAt    time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt    time.Time `json:"updated_at" ts_type:"string"`
}

// VideoPerson stores actor membership without role or display order.
type VideoPerson struct {
	VideoID   uint      `gorm:"primaryKey;autoIncrement:false;index:idx_video_people_person_video,priority:2" json:"video_id"`
	Video     Video     `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	PersonID  uint      `gorm:"primaryKey;autoIncrement:false;index:idx_video_people_person_video,priority:1" json:"person_id"`
	Person    Person    `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
}

// ImagePerson stores the same person entity on the image side. It mirrors
// VideoPerson exactly (composite key, no role, no order, cascade both ways):
// 2026-09-02 裁决 CD-02 让人物同时覆盖视频与图片，替换 2026-08-07「人物不覆盖图片」。
type ImagePerson struct {
	ImageID   uint      `gorm:"primaryKey;autoIncrement:false;index:idx_image_people_person_image,priority:2" json:"image_id"`
	Image     Image     `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	PersonID  uint      `gorm:"primaryKey;autoIncrement:false;index:idx_image_people_person_image,priority:1" json:"person_id"`
	Person    Person    `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
}

// MediaCollection is a manually curated ordered group of videos.
type MediaCollection struct {
	ID             uint           `gorm:"primarykey" json:"id"`
	Name           string         `gorm:"size:200;not null" json:"name"`
	NormalizedName string         `gorm:"size:200;not null;uniqueIndex:idx_media_collections_name_active,where:deleted_at IS NULL" json:"-"`
	Description    string         `gorm:"type:text;not null;default:''" json:"description"`
	CoverPath      string         `gorm:"type:text;not null;default:''" json:"-"`
	CreatedAt      time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt      time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt      SoftDeleteTime `gorm:"index" json:"-"`
}

// CollectionVideo stores one video's stable position in a collection.
type CollectionVideo struct {
	CollectionID uint            `gorm:"primaryKey;autoIncrement:false;index:idx_collection_videos_collection_position,priority:1;index:idx_collection_videos_video,priority:2" json:"collection_id"`
	Collection   MediaCollection `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	VideoID      uint            `gorm:"primaryKey;autoIncrement:false;index:idx_collection_videos_collection_position,priority:3;index:idx_collection_videos_video,priority:1" json:"video_id"`
	Video        Video           `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Position     int             `gorm:"not null;check:chk_collection_videos_position,position > 0;index:idx_collection_videos_collection_position,priority:2" json:"position"`
	CreatedAt    time.Time       `json:"created_at" ts_type:"string"`
	UpdatedAt    time.Time       `json:"updated_at" ts_type:"string"`
}

// VideoLocalMetadataState tracks local sidecar observation without persisting parsed candidates or source paths.
type VideoLocalMetadataState struct {
	VideoID                uint       `gorm:"primaryKey;autoIncrement:false" json:"video_id"`
	Video                  Video      `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	ObservedManifestSHA256 string     `gorm:"size:64;not null;default:'';index" json:"observed_manifest_sha256"`
	ObservedSourceStat     string     `gorm:"size:64;not null;default:''" json:"observed_source_stat"`
	AppliedManifestSHA256  string     `gorm:"size:64;not null;default:''" json:"applied_manifest_sha256"`
	Status                 string     `gorm:"size:32;not null;index" json:"status"`
	LastErrorCode          string     `gorm:"size:64;not null;default:''" json:"last_error_code"`
	LastError              string     `gorm:"type:text;not null;default:''" json:"last_error"`
	LastCheckedAt          time.Time  `gorm:"not null;index" json:"last_checked_at" ts_type:"string"`
	AppliedAt              *time.Time `json:"applied_at" ts_type:"string"`
	CreatedAt              time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt              time.Time  `json:"updated_at" ts_type:"string"`
}

// VideoTechnicalMetadata records the last successful local probe and the last attempt.
type VideoTechnicalMetadata struct {
	VideoID                    uint       `gorm:"primaryKey;autoIncrement:false" json:"video_id"`
	Video                      Video      `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	FormatName                 string     `gorm:"type:text;not null;default:''" json:"format_name"`
	FormatLongName             string     `gorm:"type:text;not null;default:''" json:"format_long_name"`
	TotalBitRate               *int64     `json:"total_bit_rate"`
	SuccessfulSourceSize       *int64     `json:"successful_source_size"`
	SuccessfulSourceModTimeNS  *int64     `json:"successful_source_mod_time_ns"`
	ProbedAt                   *time.Time `json:"probed_at" ts_type:"string"`
	LastAttemptSourceSize      *int64     `json:"last_attempt_source_size"`
	LastAttemptSourceModTimeNS *int64     `json:"last_attempt_source_mod_time_ns"`
	LastAttemptAt              *time.Time `json:"last_attempt_at" ts_type:"string"`
	LastError                  string     `gorm:"type:text;not null;default:''" json:"last_error"`
	CreatedAt                  time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt                  time.Time  `json:"updated_at" ts_type:"string"`
}

// VideoPerceptualHash stores three local frame hashes tied to an exact source
// file fingerprint. Rows with a mismatching size/mtime are ignored until the
// backfill worker recomputes them.
type VideoPerceptualHash struct {
	VideoID         uint      `gorm:"primaryKey;autoIncrement:false" json:"video_id"`
	Video           Video     `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	SourceSize      int64     `gorm:"not null" json:"source_size"`
	SourceModTimeNS int64     `gorm:"not null" json:"source_mod_time_ns"`
	HashEarly       string    `gorm:"size:16;not null" json:"hash_early"`
	HashMiddle      string    `gorm:"size:16;not null" json:"hash_middle"`
	HashLate        string    `gorm:"size:16;not null" json:"hash_late"`
	ComputedAt      time.Time `gorm:"not null;index" json:"computed_at" ts_type:"string"`
	LastError       string    `gorm:"type:text;not null;default:''" json:"last_error"`
	CreatedAt       time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt       time.Time `json:"updated_at" ts_type:"string"`
}

// NearDuplicateDismissal 持久化用户对"近似重复"误报的忽略：被忽略的视频对
// 不再进入后续清理分析的近似重复候选。低 ID 存 VideoLowID，高 ID 存 VideoHighID。
type NearDuplicateDismissal struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	VideoLowID  uint      `gorm:"not null;uniqueIndex:idx_near_dup_dismissal_pair" json:"video_low_id"`
	VideoHighID uint      `gorm:"not null;uniqueIndex:idx_near_dup_dismissal_pair" json:"video_high_id"`
	CreatedAt   time.Time `json:"created_at" ts_type:"string"`
}

// MediaStream stores one supported ffprobe stream from the last successful snapshot.
type MediaStream struct {
	ID               uint      `gorm:"primarykey" json:"id"`
	VideoID          uint      `gorm:"not null;uniqueIndex:idx_media_streams_video_index,priority:1;index:idx_media_streams_video_type,priority:1" json:"video_id"`
	Video            Video     `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	StreamIndex      int       `gorm:"not null;uniqueIndex:idx_media_streams_video_index,priority:2" json:"stream_index"`
	StreamType       string    `gorm:"size:16;not null;index:idx_media_streams_video_type,priority:2" json:"stream_type"`
	CodecName        string    `gorm:"size:100;not null;default:''" json:"codec_name"`
	CodecLongName    string    `gorm:"type:text;not null;default:''" json:"codec_long_name"`
	Profile          string    `gorm:"size:100;not null;default:''" json:"profile"`
	BitRate          *int64    `json:"bit_rate"`
	Language         string    `gorm:"size:32;not null;default:''" json:"language"`
	Title            string    `gorm:"type:text;not null;default:''" json:"title"`
	IsDefault        bool      `gorm:"not null;default:false" json:"is_default"`
	Width            *int      `json:"width"`
	Height           *int      `json:"height"`
	AvgFrameRate     string    `gorm:"size:64;not null;default:''" json:"avg_frame_rate"`
	RealFrameRate    string    `gorm:"size:64;not null;default:''" json:"real_frame_rate"`
	PixelFormat      string    `gorm:"size:100;not null;default:''" json:"pixel_format"`
	BitsPerRawSample *int      `json:"bits_per_raw_sample"`
	ColorRange       string    `gorm:"size:64;not null;default:''" json:"color_range"`
	ColorSpace       string    `gorm:"size:64;not null;default:''" json:"color_space"`
	ColorTransfer    string    `gorm:"size:64;not null;default:''" json:"color_transfer"`
	ColorPrimaries   string    `gorm:"size:64;not null;default:''" json:"color_primaries"`
	IsHDR            *bool     `json:"is_hdr"`
	IsAttachedPic    bool      `gorm:"not null;default:false" json:"is_attached_pic"`
	SampleRate       *int64    `json:"sample_rate"`
	Channels         *int      `json:"channels"`
	ChannelLayout    string    `gorm:"size:100;not null;default:''" json:"channel_layout"`
	CreatedAt        time.Time `json:"created_at" ts_type:"string"`
}

// TranslationGlossaryEntry binds one source term to its required translation for
// subtitle translation (D-033). CollectionID NULL means the entry is global.
// ScopeKey materializes COALESCE(collection_id, 0) so the uniqueness rule stays a
// plain multi-column index instead of an expression index, which SQLite and
// Postgres spell differently. SourceTermLower is written lowercased for the same
// reason: the unique key must not depend on a dialect's lower() in an index.
type TranslationGlossaryEntry struct {
	ID              uint             `gorm:"primarykey" json:"id"`
	CollectionID    *uint            `gorm:"index:idx_translation_glossary_entries_collection" json:"collection_id"`
	Collection      *MediaCollection `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	ScopeKey        int64            `gorm:"not null;default:0;uniqueIndex:idx_translation_glossary_entries_scope_term,priority:1" json:"scope_key"`
	SourceTerm      string           `gorm:"size:200;not null" json:"source_term"`
	SourceTermLower string           `gorm:"size:200;not null;uniqueIndex:idx_translation_glossary_entries_scope_term,priority:2" json:"source_term_lower"`
	TargetTerm      string           `gorm:"size:200;not null" json:"target_term"`
	Note            string           `gorm:"type:text;not null;default:''" json:"note"`
	CreatedAt       time.Time        `json:"created_at" ts_type:"string"`
	UpdatedAt       time.Time        `json:"updated_at" ts_type:"string"`
}

// 建议作品集的候选状态（D-024）。
const (
	CollectionSuggestionStatusPending   = "pending"
	CollectionSuggestionStatusConfirmed = "confirmed"
	CollectionSuggestionStatusDismissed = "dismissed"
)

// CollectionSuggestion 是一组按文件名解析出来的同系列视频（D-024）。
//
// Fingerprint 是成员 ID 排序后的 sha256，同时充当"忽略/已确认"的记忆键：
// 成员集合只要变一个，指纹就变，旧的判断不再适用，候选会重新出现。唯一索引保证
// 同一成员集合在库里最多一行——分析可以随便重跑，不会堆出一串重复候选。
//
// 分析只产出候选，任何情况下都不自动建作品集，也不改视频标题与文件名。
type CollectionSuggestion struct {
	ID uint `gorm:"primarykey" json:"id"`
	// ScanRoot 是成员所属的扫描根。跨扫描根不合并（D-023），所以它是分组键的一半。
	ScanRoot         string    `gorm:"type:text;not null;default:''" json:"scan_root"`
	SeriesName       string    `gorm:"size:200;not null" json:"series_name"`
	NormalizedSeries string    `gorm:"size:200;not null" json:"normalized_series"`
	Status           string    `gorm:"size:16;not null;index:idx_collection_suggestions_status" json:"status"`
	Fingerprint      string    `gorm:"size:64;not null;uniqueIndex:idx_collection_suggestions_fingerprint" json:"fingerprint"`
	CreatedAt        time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt        time.Time `json:"updated_at" ts_type:"string"`
}

// CollectionSuggestionMember 是候选里的一个视频（D-024）。
//
// Season 为 NULL 表示文件名里没有季信息（只有 SxxExx 给得出季）；Position 是按
// (season, episode) 排出来的次序，同集多版本（03 与 03v2）共用同一个 position，
// 界面据此提示"同集多版本"。
type CollectionSuggestionMember struct {
	SuggestionID uint                 `gorm:"primaryKey;autoIncrement:false" json:"suggestion_id"`
	Suggestion   CollectionSuggestion `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	VideoID      uint                 `gorm:"primaryKey;autoIncrement:false;index:idx_collection_suggestion_members_video" json:"video_id"`
	Video        Video                `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Season       *int                 `json:"season"`
	Episode      *int                 `json:"episode"`
	Position     int                  `gorm:"not null" json:"position"`
	CreatedAt    time.Time            `json:"created_at" ts_type:"string"`
}

// ClipDismissal 持久化用户对"截取片段"误报的忽略（D-028）。
//
// 与 NearDuplicateDismissal 的区别在于方向有意义：这一对是"完整片 → 截取片段"，
// 不能像近似重复那样归一成低 ID / 高 ID，两个字段各自固定角色。
//
// 四个指纹字段是忽略生效的条件：只有双方的源文件都还是当初那两个文件时，这条
// 忽略才算数。任一侧重编码（指纹变了）就是一份新的素材，候选应当重新出现让用户
// 再看一眼——这是设计里明确写下的边界，而不是可有可无的宽容。
type ClipDismissal struct {
	ID                  uint      `gorm:"primarykey" json:"id"`
	VideoFullID         uint      `gorm:"not null;uniqueIndex:idx_clip_dismissal_pair,priority:1" json:"video_full_id"`
	VideoClipID         uint      `gorm:"not null;uniqueIndex:idx_clip_dismissal_pair,priority:2" json:"video_clip_id"`
	FullSourceSize      int64     `gorm:"not null" json:"full_source_size"`
	FullSourceModTimeNS int64     `gorm:"not null" json:"full_source_mod_time_ns"`
	ClipSourceSize      int64     `gorm:"not null" json:"clip_source_size"`
	ClipSourceModTimeNS int64     `gorm:"not null" json:"clip_source_mod_time_ns"`
	CreatedAt           time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt           time.Time `json:"updated_at" ts_type:"string"`
}
