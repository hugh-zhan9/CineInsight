package services

import (
	"context"
	"errors"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	VideoFaceAnalysisStatusCompleted = "completed"
	VideoFaceAnalysisStatusSkipped   = "skipped"
	VideoFaceAnalysisStatusFailed    = "failed"
)

type DetectedVideoFace struct {
	FrameIndex    int
	FramePosition float64
	X             int
	Y             int
	Width         int
	Height        int
	Score         float64
	Signature     string
	Source        string
}

type VideoFaceDetector interface {
	DetectVideoFaces(ctx context.Context, video models.Video) ([]DetectedVideoFace, error)
}

type VideoFaceServiceOptions struct {
	Detector VideoFaceDetector
}

type VideoFaceService struct {
	detector VideoFaceDetector
}

type VideoFaceAnalysisResult struct {
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	FaceCount    int    `json:"face_count"`
	ClusterCount int    `json:"cluster_count"`
}

func NewVideoFaceService(options VideoFaceServiceOptions) *VideoFaceService {
	return &VideoFaceService{detector: options.Detector}
}

func (s *VideoFaceService) AnalyzeVideo(ctx context.Context, video models.Video) (*VideoFaceAnalysisResult, error) {
	if s == nil || s.detector == nil {
		return &VideoFaceAnalysisResult{Status: VideoFaceAnalysisStatusSkipped, Reason: "detector_unavailable"}, nil
	}
	faces, err := s.detector.DetectVideoFaces(ctx, video)
	if err != nil {
		if errors.Is(err, ErrVideoFaceDetectorUnavailable) {
			return &VideoFaceAnalysisResult{Status: VideoFaceAnalysisStatusSkipped, Reason: "detector_unavailable"}, nil
		}
		return nil, err
	}
	clusterIDs := make(map[uint]struct{})
	signatureCounts := make(map[string]int)
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		var existing []models.VideoFace
		if err := tx.Where("video_id = ?", video.ID).Find(&existing).Error; err != nil {
			return err
		}
		for _, face := range existing {
			if strings.TrimSpace(face.Signature) == "" || face.FaceClusterID == nil {
				continue
			}
			signatureCounts[face.Signature]++
		}
		if err := tx.Where("video_id = ?", video.ID).Delete(&models.VideoFace{}).Error; err != nil {
			return err
		}
		for _, face := range faces {
			signature := strings.TrimSpace(face.Signature)
			if signature == "" {
				continue
			}
			clusterID, err := upsertFaceCluster(tx, signature)
			if err != nil {
				return err
			}
			clusterIDs[clusterID] = struct{}{}
			source := strings.TrimSpace(face.Source)
			if source == "" {
				source = "local"
			}
			record := models.VideoFace{
				VideoID:       video.ID,
				FaceClusterID: &clusterID,
				FrameIndex:    face.FrameIndex,
				FramePosition: face.FramePosition,
				X:             face.X,
				Y:             face.Y,
				Width:         face.Width,
				Height:        face.Height,
				Score:         face.Score,
				Signature:     signature,
				Status:        models.VideoFaceStatusDetected,
				Source:        source,
			}
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
		}
		for signature, count := range signatureCounts {
			if err := tx.Model(&models.FaceCluster{}).
				Where("signature = ?", signature).
				Update("face_count", gorm.Expr("face_count - ?", count)).Error; err != nil {
				return err
			}
		}
		for _, face := range faces {
			signature := strings.TrimSpace(face.Signature)
			if signature == "" {
				continue
			}
			if err := tx.Model(&models.FaceCluster{}).
				Where("signature = ?", signature).
				Update("face_count", gorm.Expr("face_count + ?", 1)).Error; err != nil {
				return err
			}
			var cluster models.FaceCluster
			if err := tx.Where("signature = ?", signature).First(&cluster).Error; err != nil {
				return err
			}
			if cluster.RepresentativeFace == nil {
				var record models.VideoFace
				if err := tx.Where("video_id = ? AND signature = ?", video.ID, signature).First(&record).Error; err != nil {
					return err
				}
				if err := tx.Model(&models.FaceCluster{}).
					Where("id = ? AND representative_face IS NULL", cluster.ID).
					Update("representative_face", record.ID).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &VideoFaceAnalysisResult{
		Status:       VideoFaceAnalysisStatusCompleted,
		FaceCount:    len(faces),
		ClusterCount: len(clusterIDs),
	}, nil
}

var ErrVideoFaceDetectorUnavailable = errors.New("video face detector unavailable")

func upsertFaceCluster(tx *gorm.DB, signature string) (uint, error) {
	cluster := models.FaceCluster{Signature: signature}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "signature"}},
		DoNothing: true,
	}).Create(&cluster).Error; err != nil {
		return 0, err
	}
	var loaded models.FaceCluster
	if err := tx.Where("signature = ?", signature).First(&loaded).Error; err != nil {
		return 0, err
	}
	return loaded.ID, nil
}
