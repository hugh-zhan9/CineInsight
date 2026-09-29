package models

import "time"

// FaceRelationWrite 记录人脸链路**实际新插入**的一条人物关系（video_people / image_people），
// 以及是哪个簇写的（META-04 / D-PC30 的可逆前提）。
//
// 只有「这一次 INSERT … ON CONFLICT DO NOTHING 真的插进去了」才记一行：关系已经存在
// （NFO 导入、手动添加、标签转人物等来源）时不记，于是解除关联 / 改派只能删掉或迁走
// 自己写过的关系，来源不明的关系一律保留。唯一的例外是「关系本身就是人脸链路写的」：
// 同一人物的另一个簇再确认到同一件媒体时也记一行（共同持有），任何一个簇离开该人物时，
// 只要还有别的簇持有，关系就留着。
//
// 没有外键：media_kind + media_id 与人脸观测一样是多态引用；person_id 同样不挂外键，
// 人物删除后的孤儿行由人脸审阅在对账时清掉（reconcileFaceClusterPeople）。
//
// CreatedAt 与写出的关系行的 created_at 取同一个值：关系被别的路径删掉又由别的来源重建时，
// 新关系的 created_at 会晚于这条记录，这条记录因此不再作数。
type FaceRelationWrite struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	PersonID  uint      `gorm:"not null;uniqueIndex:idx_face_relation_writes_identity,priority:1" json:"person_id"`
	MediaKind string    `gorm:"size:8;not null;uniqueIndex:idx_face_relation_writes_identity,priority:2" json:"media_kind"`
	MediaID   uint      `gorm:"not null;uniqueIndex:idx_face_relation_writes_identity,priority:3" json:"media_id"`
	ClusterID uint      `gorm:"not null;uniqueIndex:idx_face_relation_writes_identity,priority:4;index:idx_face_relation_writes_cluster" json:"cluster_id"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
}
