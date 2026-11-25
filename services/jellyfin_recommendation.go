package services

import (
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func jellyfinSortHasRandom(q url.Values) bool {
	for _, key := range strings.Split(jellyfinParam(q, "SortBy"), ",") {
		if strings.EqualFold(strings.TrimSpace(key), "random") {
			return true
		}
	}
	return false
}

func (s *JellyfinServer) selectRandomRecommendationVideos(r *http.Request, query *gorm.DB, total int64, limit int) ([]models.Video, error) {
	s.recommendMu.Lock()
	defer s.recommendMu.Unlock()
	if total <= 0 || limit <= 0 {
		return []models.Video{}, nil
	}

	silentDB := database.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).WithContext(r.Context())
	seenSub := silentDB.Model(&models.JellyfinRecommendationHistory{}).Select("video_id")
	var unseenIDs []uint
	if err := query.Session(&gorm.Session{}).Where("videos.id NOT IN (?)", seenSub).Order("RANDOM()").Limit(limit).Pluck("videos.id", &unseenIDs).Error; err != nil {
		return nil, err
	}

	selectedIDs := unseenIDs
	resetScope := len(unseenIDs) < limit && int64(len(unseenIDs)) < total
	if resetScope {
		need := limit - len(unseenIDs)
		refill := query.Session(&gorm.Session{})
		if len(unseenIDs) > 0 {
			refill = refill.Where("videos.id NOT IN ?", unseenIDs)
		}
		var refillIDs []uint
		if err := refill.Order("RANDOM()").Limit(need).Pluck("videos.id", &refillIDs).Error; err != nil {
			return nil, err
		}
		selectedIDs = append(unseenIDs, refillIDs...)
	}

	if len(selectedIDs) == 0 {
		return []models.Video{}, nil
	}

	err := silentDB.Transaction(func(tx *gorm.DB) error {
		if resetScope {
			var seenInScope []uint
			seenSubTx := tx.Model(&models.JellyfinRecommendationHistory{}).Select("video_id")
			scope := query.Session(&gorm.Session{}).Where("videos.id IN (?)", seenSubTx)
			// Keep the candidate filters, but read the history on the transaction connection.
			scope.Statement.ConnPool = tx.Statement.ConnPool
			if err := scope.Pluck("videos.id", &seenInScope).Error; err != nil {
				return err
			}
			for chunk := range slices.Chunk(seenInScope, 500) {
				if err := tx.Where("video_id IN ?", chunk).Delete(&models.JellyfinRecommendationHistory{}).Error; err != nil {
					return err
				}
			}
		}
		now := s.now().UTC()
		history := make([]models.JellyfinRecommendationHistory, 0, len(selectedIDs))
		for _, id := range selectedIDs {
			history = append(history, models.JellyfinRecommendationHistory{VideoID: id, RecommendedAt: now})
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "video_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"recommended_at"}),
		}).Create(&history).Error
	})
	if err != nil {
		return nil, err
	}

	var rows []models.Video
	if err := silentDB.Preload("Tags").Where("id IN ?", selectedIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	positions := make(map[uint]int, len(selectedIDs))
	for i, id := range selectedIDs {
		positions[id] = i
	}
	sort.Slice(rows, func(i, j int) bool { return positions[rows[i].ID] < positions[rows[j].ID] })
	return rows, nil
}
