package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// Named people own a managed copy; clearing or reanalysing faces must not remove
// the avatar the user saw when naming them.
func (s *FaceReviewService) importClusterAvatar(db *gorm.DB, cluster models.FaceCluster, personID uint) (managedImageImport, error) {
	if s.images == nil || s.resolveCrop == nil || cluster.RepresentativeObservationID == nil {
		return managedImageImport{}, nil
	}
	asset, err := s.resolveCrop(db, *cluster.RepresentativeObservationID)
	if errors.Is(err, os.ErrNotExist) {
		return managedImageImport{}, nil
	}
	if err != nil {
		return managedImageImport{}, fmt.Errorf("读取人脸头像失败: %s", scrubFacePaths(err.Error()))
	}
	if asset == nil {
		return managedImageImport{}, nil
	}
	imported, err := s.images.Import("people", personID, asset.Path)
	if err != nil {
		return managedImageImport{}, fmt.Errorf("保存人物头像失败: %s", scrubFacePaths(err.Error()))
	}
	return imported, nil
}

func (s *FaceReviewService) cleanupUnreferencedAvatar(imported managedImageImport) {
	if !imported.Created || s.images == nil {
		return
	}
	var count int64
	// A commit error can be ambiguous. Never remove an asset still referenced by a
	// committed person, or when the reference check itself fails.
	if err := database.DB.Model(&models.Person{}).Where("avatar_path = ?", imported.RelativePath).Count(&count).Error; err == nil && count == 0 {
		if err := s.images.Remove(imported.RelativePath); err != nil {
			log.Printf("Face avatar cleanup failed")
		}
	}
}

// BackfillNamedFaceAvatars repairs people created by the former naming flow.
// Only empty avatars are filled; explicit user avatars are never replaced.
func (s *FaceReviewService) BackfillNamedFaceAvatars(ctx context.Context) (int, error) {
	if s.images == nil || s.resolveCrop == nil {
		return 0, nil
	}
	var clusters []models.FaceCluster
	if err := database.DB.WithContext(ctx).Model(&models.FaceCluster{}).
		Joins("JOIN people ON people.id = face_clusters.person_id").
		Where("face_clusters.status = ? AND people.avatar_path = ?", models.FaceClusterStatusNamed, "").
		Order("face_clusters.observation_count DESC, face_clusters.id ASC").Find(&clusters).Error; err != nil {
		return 0, err
	}
	restored := 0
	done := map[uint]bool{}
	for _, cluster := range clusters {
		if err := ctx.Err(); err != nil {
			return restored, err
		}
		if cluster.PersonID == nil || done[*cluster.PersonID] {
			continue
		}
		imported, err := s.importClusterAvatar(database.DB.WithContext(ctx), cluster, *cluster.PersonID)
		if err != nil {
			return restored, err
		}
		if imported.RelativePath == "" {
			continue
		}
		update := database.DB.WithContext(ctx).Model(&models.Person{}).Where("id = ? AND avatar_path = ?", *cluster.PersonID, "").Update("avatar_path", imported.RelativePath)
		if update.Error != nil {
			s.cleanupUnreferencedAvatar(imported)
			return restored, update.Error
		}
		if update.RowsAffected == 0 {
			s.cleanupUnreferencedAvatar(imported)
		} else {
			restored++
			log.Printf("Face person avatar restored person_id=%d cluster_id=%d", *cluster.PersonID, cluster.ID)
		}
		done[*cluster.PersonID] = true
	}
	return restored, nil
}
