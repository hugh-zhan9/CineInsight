package models

import "time"

// 场景检索的画面索引（D-MW-SCENES，场景检索合同「数据」节）。两张表都是可重建的
// 派生数据：worker 是每个视频派生行的唯一写者，单视频结果在一个事务里删旧段、
// 插新段并 upsert 状态。查询只认 model_id 为当前模型、source_size 等于 videos.size、
// status = indexed 的状态行，源文件或模型一变，旧位置就不再返回。
//
// 数值列一律不带非零 gorm default：迁移器逐行复制时 GORM 会跳过零值字段，默认值
// 写在标签上会把合法的 0 翻成别的值（同 VideoFrameHashSequence 的说明）。

// SceneIndexStatus* 是 scene_index_states.status 的取值。
const (
	SceneIndexStatusIndexed = "indexed"
	SceneIndexStatusFailed  = "failed"
)

// SceneIndexState 记录一个视频当前画面索引的来源指纹、模型与结果。
type SceneIndexState struct {
	VideoID       uint   `gorm:"primaryKey;autoIncrement:false" json:"video_id"`
	Video         Video  `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	ModelID       string `gorm:"size:200;not null;default:'';index" json:"model_id"`
	Provider      string `gorm:"size:16;not null;default:''" json:"provider"`
	SourceSize    int64  `gorm:"not null" json:"source_size"`
	SourceMtimeNS int64  `gorm:"column:source_mtime_ns;not null" json:"source_mtime_ns"`
	IntervalMS    int    `gorm:"column:interval_ms;not null" json:"interval_ms"`
	SegmentCount  int    `gorm:"not null;default:0" json:"segment_count"`
	Status        string `gorm:"size:16;not null;default:''" json:"status"`
	// LastError 落库前已抹掉绝对路径（与帧哈希同口径）。
	LastError string     `gorm:"type:text;not null;default:''" json:"last_error"`
	IndexedAt *time.Time `json:"indexed_at" ts_type:"string"`
	UpdatedAt time.Time  `json:"updated_at" ts_type:"string"`
}

// SceneVisualSegment 是一段画面相近的连续采样帧。本地模型存 int8 量化向量
// （4 字节小端 float32 scale + 512 字节 int8，共 516 字节），外部描述存 caption、
// embedding 为空。时间区间为 [start_ms, end_ms)。
type SceneVisualSegment struct {
	ID        uint   `gorm:"primarykey;index:idx_scene_visual_segments_model_id_id,priority:2" json:"id"`
	VideoID   uint   `gorm:"not null;index:idx_scene_visual_segments_video_id" json:"video_id"`
	Video     Video  `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	ModelID   string `gorm:"size:200;not null;default:'';index:idx_scene_visual_segments_model_id_id,priority:1" json:"model_id"`
	StartMS   int64  `gorm:"column:start_ms;not null" json:"start_ms"`
	EndMS     int64  `gorm:"column:end_ms;not null" json:"end_ms"`
	Embedding []byte `json:"-"`
	Caption   string `gorm:"type:text;not null;default:''" json:"caption"`
}
