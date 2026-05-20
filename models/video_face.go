package models

import "time"

const (
	VideoFaceStatusDetected = "detected"
	VideoFaceStatusHidden   = "hidden"
)

// FaceCluster groups visually similar local face signatures without requiring a heavy embedding model.
type FaceCluster struct {
	ID                 uint           `gorm:"primarykey" json:"id"`
	DisplayName        string         `json:"display_name"`
	Signature          string         `gorm:"uniqueIndex:idx_face_clusters_signature" json:"signature"`
	RepresentativeFace *uint          `json:"representative_face,omitempty"`
	FaceCount          int            `gorm:"default:0" json:"face_count"`
	CreatedAt          time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt          time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt          SoftDeleteTime `gorm:"index" json:"-"`
}

// VideoFace stores lightweight face detections for a video frame.
type VideoFace struct {
	ID            uint           `gorm:"primarykey" json:"id"`
	VideoID       uint           `gorm:"index:idx_video_faces_video_status,priority:1" json:"video_id"`
	Video         Video          `gorm:"constraint:OnDelete:CASCADE;" json:"video"`
	FaceClusterID *uint          `gorm:"index" json:"face_cluster_id,omitempty"`
	FaceCluster   *FaceCluster   `json:"face_cluster,omitempty"`
	FrameIndex    int            `json:"frame_index"`
	FramePosition float64        `json:"frame_position"`
	X             int            `json:"x"`
	Y             int            `json:"y"`
	Width         int            `json:"width"`
	Height        int            `json:"height"`
	Score         float64        `json:"score"`
	Signature     string         `gorm:"index" json:"signature"`
	Status        string         `gorm:"index:idx_video_faces_video_status,priority:2;not null;default:'detected'" json:"status"`
	Source        string         `json:"source"`
	CreatedAt     time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt     time.Time      `json:"updated_at" ts_type:"string"`
	DeletedAt     SoftDeleteTime `gorm:"index" json:"-"`
}
