package services

import (
	"context"
	"errors"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

type FaceClusterObservationView struct {
	ObservationID uint   `json:"observation_id"`
	MediaKind     string `json:"media_kind"`
	MediaID       uint   `json:"media_id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	FrameMS       int64  `json:"frame_ms"`
	BBox          string `json:"bbox"`
	Unavailable   string `json:"unavailable"`
}
type FaceClusterObservationPage struct {
	Observations []FaceClusterObservationView `json:"observations"`
	NextID       uint                         `json:"next_id"`
}

// Read one bounded page on demand. No embeddings or crop filesystem paths are
// exposed, and media references are resolved separately by kind (IDs overlap).
func (s *FaceReviewService) GetFaceClusterObservations(ctx context.Context, clusterID, cursorID uint, limit int) (*FaceClusterObservationPage, error) {
	var cluster models.FaceCluster
	if err := database.DB.WithContext(ctx).Select("id").First(&cluster, clusterID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrFaceClusterNotFound
		}
		return nil, err
	}
	limit = normalizeEntityPageLimit(limit)
	var observations []models.FaceObservation
	if err := database.DB.WithContext(ctx).Select("id", "media_kind", "media_id", "frame_ms", "bbox").Where("cluster_id = ? AND id > ? AND media_kind IN ?", clusterID, cursorID, []string{models.FaceMediaKindVideo, models.FaceMediaKindImage}).Order("id ASC").Limit(limit + 1).Find(&observations).Error; err != nil {
		return nil, err
	}
	page := &FaceClusterObservationPage{Observations: make([]FaceClusterObservationView, 0, len(observations))}
	if len(observations) > limit {
		observations = observations[:limit]
		page.NextID = observations[len(observations)-1].ID
	}
	videoIDs, imageIDs := []uint{}, []uint{}
	for _, o := range observations {
		if o.MediaKind == models.FaceMediaKindVideo {
			videoIDs = append(videoIDs, o.MediaID)
		} else {
			imageIDs = append(imageIDs, o.MediaID)
		}
	}
	var videos []models.Video
	var images []models.Image
	fields := []string{"id", "name", "path", "size", "is_stale", "deleted_at"}
	if len(videoIDs) > 0 {
		if err := database.DB.WithContext(ctx).Unscoped().Select(fields).Where("id IN ?", videoIDs).Find(&videos).Error; err != nil {
			return nil, err
		}
	}
	if len(imageIDs) > 0 {
		if err := database.DB.WithContext(ctx).Unscoped().Select(fields).Where("id IN ?", imageIDs).Find(&images).Error; err != nil {
			return nil, err
		}
	}
	type source struct {
		name, path  string
		size        int64
		unavailable string
	}
	state := func(deleted, stale bool) string {
		if deleted {
			return "已删除"
		}
		if stale {
			return "路径失效"
		}
		return ""
	}
	videoMap, imageMap := map[uint]source{}, map[uint]source{}
	for _, v := range videos {
		videoMap[v.ID] = source{v.Name, v.Path, v.Size, state(v.DeletedAt.IsValid(), v.IsStale)}
	}
	for _, v := range images {
		imageMap[v.ID] = source{v.Name, v.Path, v.Size, state(v.DeletedAt.IsValid(), v.IsStale)}
	}
	for _, o := range observations {
		media, ok := videoMap[o.MediaID]
		if o.MediaKind == models.FaceMediaKindImage {
			media, ok = imageMap[o.MediaID]
		}
		if !ok {
			media.unavailable = "原记录已移除"
		}
		frameMS := models.FaceFrameMSNone
		if o.FrameMS != nil {
			frameMS = *o.FrameMS
		}
		page.Observations = append(page.Observations, FaceClusterObservationView{ObservationID: o.ID, MediaKind: o.MediaKind, MediaID: o.MediaID, Name: media.name, Path: media.path, Size: media.size, FrameMS: frameMS, BBox: o.BBox, Unavailable: media.unavailable})
	}
	return page, nil
}
