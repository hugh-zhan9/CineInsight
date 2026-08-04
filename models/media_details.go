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
