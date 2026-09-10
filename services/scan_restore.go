package services

import (
	"errors"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// Caller holds the scan/path locks. Historical unknown and user deletions are
// intentionally left alone: file presence does not establish deletion intent.
func (s *VideoService) addScannedVideo(path string) (*models.Video, bool, error) {
	var video models.Video
	err := database.DB.Unscoped().Where("path = ?", path).
		Order("deleted_at IS NULL DESC, id DESC").First(&video).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	if err == nil && video.DeletedAt.IsValid() && video.DeletedBy == "scanner" {
		var entry models.VideoTrashEntry
		if err := database.DB.Where("video_id = ? AND deleted_by = ? AND file_moved = ? AND state = ?", video.ID, "scanner", false, trashStateDeleted).First(&entry).Error; err != nil {
			return nil, false, err
		}
		restored, err := s.restoreTrashEntry(&entry)
		return restored, err == nil, err
	}
	added, err := s.addVideo(path)
	return added, false, err
}
