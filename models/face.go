package models

import "time"

// 人脸观测的媒体类型（D-017）。person_avatar 是人物头像提取出的"种子"观测，
// 它不参与聚类，只用来给未命名簇算人物候选。
const (
	FaceMediaKindVideo        = "video"
	FaceMediaKindImage        = "image"
	FaceMediaKindPersonAvatar = "person_avatar"
)

// 追加候选状态（D-019）。none 是绝大多数观测的状态；只有被吸收进"已命名"簇的
// 新观测才置 pending，等用户在审阅面板确认或忽略。任何路径都不会因此自动写
// video_people / image_people。
const (
	FaceAppendStatusNone      = "none"
	FaceAppendStatusPending   = "pending"
	FaceAppendStatusConfirmed = "confirmed"
	FaceAppendStatusDismissed = "dismissed"
)

// 簇状态（D-019）。
const (
	FaceClusterStatusUnnamed = "unnamed"
	FaceClusterStatusNamed   = "named"
	FaceClusterStatusIgnored = "ignored"
)

// FaceFrameMSNone 是"这条观测不来自视频帧"的哨兵值（图片与人物头像）。
// 取 -1 而不是 0：0 是视频第一帧的合法位置。
const FaceFrameMSNone = int64(-1)

// FaceNoFaceBBoxHash 是"这一份源里没有脸"的标记观测所用的 bbox 哈希（4.4.4）。
// 标记观测没有向量也没有裁剪图，存在的唯一理由是让下一次分析能凭
// (media_kind, media_id, source_fingerprint) 判定"这份源已经看过了"，
// 而不是每轮都重新解码一遍全无人脸的媒体。
const FaceNoFaceBBoxHash = "no_face"

// FaceObservation 是一次人脸检测的结果（D-017、D-018）。
//
// media_kind + media_id 是多态引用（视频 / 图片 / 人物头像），因此这里没有数据库
// 外键——两张不同的表指不过来。媒体永久删除后的孤儿观测由分析任务开跑前的
// 对账一次性清掉（见 services.pruneOrphanFaceObservations）。
//
// 向量与裁剪图都只留在本机：embedding 不进日志，crop_path 只经
// /preview/face-crop/{id} 这一条受控路由读取（D-020）。
type FaceObservation struct {
	ID        uint   `gorm:"primarykey" json:"id"`
	MediaKind string `gorm:"size:16;not null;uniqueIndex:idx_face_observations_identity,priority:1;index:idx_face_observations_media,priority:1" json:"media_kind"`
	MediaID   uint   `gorm:"not null;uniqueIndex:idx_face_observations_identity,priority:2;index:idx_face_observations_media,priority:2" json:"media_id"`
	// SourceFingerprint 是源文件的 size+mtime 指纹（视频与图片同口径）；
	// 指纹变了就是新的一份源，旧观测作废重算（D-018）。
	SourceFingerprint string `gorm:"size:64;not null;default:'';uniqueIndex:idx_face_observations_identity,priority:3" json:"source_fingerprint"`
	// FrameMS 是视频帧位置（毫秒）；图片与头像存哨兵值 FaceFrameMSNone。
	//
	// 有意不用 NULL：SQLite 与 Postgres 都把 NULL 当作互不相等，唯一键里只要有
	// 一列是 NULL，这一行就永远撞不上任何别的行——图片与头像的幂等就完全落在
	// 应用层，数据库这道保险等于没有。哨兵值让约束真正生效。
	FrameMS *int64 `gorm:"not null;default:-1;uniqueIndex:idx_face_observations_identity,priority:4" json:"frame_ms"`
	// BBox 是 "x,y,w,h" 的相对坐标（0–1），与源图尺寸解耦。
	// column 标签是必须的：GORM 会把 BBox 拆成 b_box，而设计与索引口径都是 bbox。
	BBox     string  `gorm:"column:bbox;size:64;not null;default:''" json:"bbox"`
	BBoxHash string  `gorm:"column:bbox_hash;size:16;not null;default:'';uniqueIndex:idx_face_observations_identity,priority:5" json:"bbox_hash"`
	Quality  float64 `gorm:"not null;default:0" json:"quality"`
	// Embedding 是 512 维 float32（2048 字节）小端序，已 L2 归一化；no_face 标记行为 NULL。
	Embedding []byte `json:"-"`
	// CropPath 是裁剪图在 faces 目录下的文件名（不是绝对路径）；无裁剪图时为空串。
	CropPath  string `gorm:"type:text;not null;default:''" json:"-"`
	ClusterID *uint  `gorm:"index:idx_face_observations_cluster,priority:1" json:"cluster_id"`
	// Cluster 的 SET NULL 让 ClearFaceData 能先删簇再删观测，也让簇被清理时
	// 观测不至于指向一个不存在的行。
	Cluster      *FaceCluster `gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;" json:"-"`
	AppendStatus string       `gorm:"size:16;not null;default:'none';index:idx_face_observations_cluster,priority:2" json:"append_status"`
	CreatedAt    time.Time    `json:"created_at" ts_type:"string"`
	UpdatedAt    time.Time    `json:"updated_at" ts_type:"string"`
}

// FaceCluster 是一组相似观测（D-017）。centroid 是簇内向量的归一化均值，
// 增量聚类只和它比对；representative_observation_id 指向质量最高的那条观测，
// 界面用它的裁剪图当簇头像。
type FaceCluster struct {
	ID     uint   `gorm:"primarykey" json:"id"`
	Status string `gorm:"size:16;not null;default:'unnamed';index:idx_face_clusters_status" json:"status"`
	// PersonID 在人物被删除时置空（外键 SET NULL），簇于是回到未命名重新出现在
	// 审阅面板里（4.4.4）。
	PersonID                    *uint     `gorm:"index:idx_face_clusters_person" json:"person_id"`
	Person                      *Person   `gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;" json:"-"`
	Centroid                    []byte    `json:"-"`
	ObservationCount            int       `gorm:"not null;default:0" json:"observation_count"`
	RepresentativeObservationID *uint     `json:"representative_observation_id"`
	CreatedAt                   time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt                   time.Time `json:"updated_at" ts_type:"string"`
}

// FacePersonCandidate 是"这个簇可能是这个人"的建议（D-017）。
// 只是建议：写 video_people / image_people 永远需要用户的一次动作（D-019）。
type FacePersonCandidate struct {
	ID         uint        `gorm:"primarykey" json:"id"`
	ClusterID  uint        `gorm:"not null;uniqueIndex:idx_face_person_candidates_pair,priority:1" json:"cluster_id"`
	Cluster    FaceCluster `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	PersonID   uint        `gorm:"not null;uniqueIndex:idx_face_person_candidates_pair,priority:2" json:"person_id"`
	Person     Person      `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Similarity float64     `gorm:"not null;default:0" json:"similarity"`
	CreatedAt  time.Time   `json:"created_at" ts_type:"string"`
	UpdatedAt  time.Time   `json:"updated_at" ts_type:"string"`
}
