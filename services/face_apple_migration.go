package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"reflect"
	"video-master/database"
	"video-master/models"
)

// migrateAppleFaces keeps observation IDs, all review decisions and relationship provenance.
// A group is published only when every crop succeeds and membership is still the same.
func (s *FaceAnalysisService) migrateAppleFaces(ctx context.Context, session FaceWorkerSession) (int, error) {
	var legacy []models.FaceObservation
	if err := database.DB.WithContext(ctx).Select("id", "cluster_id").Where("length(embedding) = ?", 2048).Order("id").Find(&legacy).Error; err != nil {
		return 0, err
	}
	s.update(func(status *FaceAnalysisStatus) {
		status.Migrating = len(legacy) > 0
		status.MigrationTotal = len(legacy)
	})
	defer s.update(func(status *FaceAnalysisStatus) { status.Migrating = false })
	seen := map[uint]bool{}
	pending := 0
	processed := 0
	for _, row := range legacy {
		if err := ctx.Err(); err != nil {
			return pending, err
		}
		if row.ClusterID != nil {
			if seen[*row.ClusterID] {
				continue
			}
			seen[*row.ClusterID] = true
		}
		query := database.DB.WithContext(ctx).Model(&models.FaceObservation{})
		if row.ClusterID != nil {
			query = query.Where("cluster_id = ?", *row.ClusterID)
		} else {
			query = query.Where("id = ? AND cluster_id IS NULL", row.ID)
		}
		var snapshot []models.FaceObservation
		if err := query.Order("id").Find(&snapshot).Error; err != nil {
			return pending, err
		}
		if len(snapshot) == 0 {
			continue
		}
		vectors := make([][]float32, len(snapshot))
		valid := true
		for i, observation := range snapshot {
			if v, err := decodeFaceEmbedding(observation.Embedding); err == nil {
				vectors[i] = v
				continue
			}
			path, ok := s.cropFilePath(observation.CropPath)
			if !ok {
				valid = false
				break
			}
			faces, err := s.detectMigrationFace(ctx, session, fmt.Sprintf("migration-%d", observation.ID), path)
			if err != nil || len(faces) != 1 {
				if ctx.Err() != nil {
					return pending, ctx.Err()
				}
				if errors.Is(err, errFaceWorkerGone) {
					return pending, err
				}
				valid = false
				break
			}
			vectors[i], ok = normalizeFaceEmbedding(faces[0].Embedding)
			if !ok {
				valid = false
				break
			}
		}
		for _, row := range snapshot {
			if len(row.Embedding) == 2048 {
				processed++
			}
		}
		s.update(func(status *FaceAnalysisStatus) { status.MigrationProcessed = processed })
		if !valid {
			pending += len(snapshot)
			continue
		}
		faceClusterAssignmentMu.Lock()
		err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
			q := tx.Model(&models.FaceObservation{})
			if row.ClusterID != nil {
				q = q.Where("cluster_id = ?", *row.ClusterID)
			} else {
				q = q.Where("id = ? AND cluster_id IS NULL", row.ID)
			}
			var current []models.FaceObservation
			if err := q.Order("id").Find(&current).Error; err != nil {
				return err
			}
			if len(current) != len(snapshot) {
				pending += len(snapshot)
				return nil
			}
			for i, c := range current {
				if c.ID != snapshot[i].ID || !bytes.Equal(c.Embedding, snapshot[i].Embedding) || !reflect.DeepEqual(c.ClusterID, snapshot[i].ClusterID) {
					pending += len(snapshot)
					return nil
				}
			}
			sum := make([]float32, faceEmbeddingDims)
			for i, c := range current {
				for j, v := range vectors[i] {
					sum[j] += v
				}
				if err := tx.Model(&models.FaceObservation{}).Where("id = ?", c.ID).UpdateColumn("embedding", encodeFaceEmbedding(vectors[i])).Error; err != nil {
					return err
				}
			}
			if row.ClusterID == nil {
				return nil
			}
			centroid, ok := normalizeFaceEmbedding(sum)
			if !ok {
				return fmt.Errorf("apple_face_migration_centroid")
			}
			if err := tx.Model(&models.FaceCluster{}).Where("id = ?", *row.ClusterID).UpdateColumn("centroid", encodeFaceEmbedding(centroid)).Error; err != nil {
				return err
			}
			return tx.Where("cluster_id = ?", *row.ClusterID).Delete(&models.FacePersonCandidate{}).Error
		})
		faceClusterAssignmentMu.Unlock()
		if err != nil {
			return pending, err
		}
	}
	return pending, nil
}

func (s *FaceAnalysisService) detectMigrationFace(ctx context.Context, session FaceWorkerSession, id, path string) ([]DetectedFace, error) {
	if err := s.waitForPauseHook(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	slot := s.slot
	s.mu.Unlock()
	if slot != nil {
		if err := slot.Acquire(ctx); err != nil {
			return nil, err
		}
		defer slot.Release()
	}
	return session.Detect(ctx, id, path)
}
