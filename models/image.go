package models

import "time"

// Image 图片文件模型，作为与视频并行的一等实体（设计 5.1.2）。
type Image struct {
	ID                  uint           `gorm:"primarykey" json:"id"`
	Name                string         `json:"name"`                                                                    // 文件名
	Path                string         `gorm:"uniqueIndex:idx_images_path_active,where:deleted_at IS NULL" json:"path"` // 完整路径
	Directory           string         `json:"directory"`                                                               // 所在目录
	Size                int64          `json:"size"`                                                                    // 文件大小（字节），迁移指纹之一
	Width               int            `json:"width"`                                                                   // 像素宽度，0=未探测
	Height              int            `json:"height"`                                                                  // 像素高度，0=未探测
	Format              string         `gorm:"size:16;not null;default:''" json:"format"`                               // 小写扩展名（无点），洞察聚合用
	IsStale             bool           `gorm:"default:false" json:"is_stale"`                                           // 当前路径是否失效/待纠偏
	IsFavorite          bool           `gorm:"not null;default:false" json:"is_favorite"`                               // 收藏
	PersonalRating      *float64       `gorm:"type:numeric(3,1);check:chk_images_personal_rating,personal_rating IS NULL OR (personal_rating >= 0 AND personal_rating <= 10 AND personal_rating * 2 = CAST(personal_rating * 2 AS INTEGER))" json:"personal_rating"`
	PerceptualHash      string         `gorm:"size:16;not null;default:''" json:"perceptual_hash"` // 64 位 dHash hex，''=未回填
	HashSourceSize      int64          `gorm:"not null;default:0" json:"hash_source_size"`         // 哈希时源文件大小，stale 判定
	HashSourceModTimeNS int64          `gorm:"not null;default:0" json:"hash_source_mod_time_ns"`  // 哈希时源文件 mtime（纳秒），stale 判定
	Tags                []Tag          `gorm:"many2many:image_tags;" json:"tags"`                  // 标签（多对多，与视频共享 tags 表）
	CreatedAt           time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt           time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt           SoftDeleteTime `gorm:"index" json:"-"`
}

// ImageDirectory 图片扫描目录配置，镜像 ScanDirectory。
type ImageDirectory struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	Path      string         `json:"path"`  // 目录路径
	Alias     string         `json:"alias"` // 目录别名
	CreatedAt time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt SoftDeleteTime `gorm:"index" json:"-"`
}

// ImageTrashEntry 记录恢复软删除图片所需的信息，镜像 VideoTrashEntry。
type ImageTrashEntry struct {
	ID           uint      `gorm:"primarykey" json:"id"`
	ImageID      uint      `gorm:"uniqueIndex;not null" json:"image_id"`
	ImageName    string    `gorm:"not null" json:"image_name"`
	OriginalPath string    `gorm:"not null" json:"original_path"`
	TrashPath    string    `gorm:"uniqueIndex:idx_image_trash_entries_trash_path,where:trash_path <> ''" json:"trash_path"`
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

// ImageAIDescription 单表承载图片 AI 描述的结果与任务状态（设计 4.6.2）。
type ImageAIDescription struct {
	ID              uint       `gorm:"primarykey" json:"id"`
	ImageID         uint       `gorm:"uniqueIndex;not null" json:"image_id"`
	Image           Image      `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	Status          string     `gorm:"size:32;not null;index" json:"status"`
	Description     string     `gorm:"type:text;not null;default:''" json:"description"`
	ModelIdentifier string     `gorm:"size:255;not null;default:''" json:"model_identifier"`
	ErrorCode       string     `gorm:"size:64;not null;default:''" json:"error_code"`
	LastError       string     `gorm:"type:text;not null;default:''" json:"last_error"`
	AttemptCount    int        `gorm:"not null;default:0" json:"attempt_count"`
	GeneratedAt     *time.Time `json:"generated_at,omitempty" ts_type:"string"`
	CreatedAt       time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt       time.Time  `json:"updated_at" ts_type:"string"`
}

// TableName 固定表名为设计契约的 image_ai_descriptions；GORM 默认复数化会把
// AIDescription 错误拆分为 image_a_idescriptions。
func (ImageAIDescription) TableName() string {
	return "image_ai_descriptions"
}

// ImageSemanticIndex 记录按模型与维度隔离的图片向量落库成功状态，镜像 VideoSemanticIndex。
type ImageSemanticIndex struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	ImageID            uint      `gorm:"uniqueIndex:idx_image_semantic_model_dimension,priority:1;index;not null" json:"image_id"`
	Image              Image     `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	ModelIdentifier    string    `gorm:"size:255;uniqueIndex:idx_image_semantic_model_dimension,priority:2;index:idx_image_semantic_lookup_model_dimension,priority:1;not null" json:"model_identifier"`
	Dimension          int       `gorm:"uniqueIndex:idx_image_semantic_model_dimension,priority:3;index:idx_image_semantic_lookup_model_dimension,priority:2;not null" json:"dimension"`
	Generation         int       `gorm:"not null;default:1;index" json:"generation"`
	ContentFingerprint string    `gorm:"size:64;not null;index" json:"content_fingerprint"`
	IndexedAt          time.Time `gorm:"not null;index" json:"indexed_at" ts_type:"string"`
	CreatedAt          time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt          time.Time `json:"updated_at" ts_type:"string"`
}

// ImageSemanticIndexAttempt 保留可续跑的单图索引失败留痕，镜像 SemanticIndexAttempt。
type ImageSemanticIndexAttempt struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	ImageID            uint       `gorm:"uniqueIndex:idx_image_semantic_attempt_image_model_generation,priority:1;index;not null" json:"image_id"`
	Image              Image      `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	ModelIdentifier    string     `gorm:"size:255;uniqueIndex:idx_image_semantic_attempt_image_model_generation,priority:2;not null" json:"model_identifier"`
	Generation         int        `gorm:"uniqueIndex:idx_image_semantic_attempt_image_model_generation,priority:3;not null" json:"generation"`
	Status             string     `gorm:"size:32;not null;index" json:"status"`
	AttemptCount       int        `gorm:"not null;default:0" json:"attempt_count"`
	ContentFingerprint string     `gorm:"size:64;not null;default:''" json:"content_fingerprint"`
	Dimension          int        `gorm:"not null;default:0" json:"dimension"`
	ErrorCode          string     `gorm:"size:64;not null;default:''" json:"error_code"`
	LastError          string     `gorm:"type:text;not null;default:''" json:"last_error"`
	LastAttemptedAt    *time.Time `gorm:"index" json:"last_attempted_at,omitempty" ts_type:"string"`
	CompletedAt        *time.Time `json:"completed_at,omitempty" ts_type:"string"`
	CreatedAt          time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt          time.Time  `json:"updated_at" ts_type:"string"`
}

// ImageNearDuplicateDismissal 持久化用户对图片"近似重复"误报的忽略：被忽略的图片对
// 不再进入后续清理分析的近似重复候选。低 ID 存 ImageLowID，高 ID 存 ImageHighID。
type ImageNearDuplicateDismissal struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	ImageLowID  uint      `gorm:"not null;uniqueIndex:idx_image_near_dup_dismissal_pair" json:"image_low_id"`
	ImageHighID uint      `gorm:"not null;uniqueIndex:idx_image_near_dup_dismissal_pair" json:"image_high_id"`
	CreatedAt   time.Time `json:"created_at" ts_type:"string"`
}
