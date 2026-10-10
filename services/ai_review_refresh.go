package services

import (
	"context"
	"errors"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// ReviewCandidateRefresh contains only requested candidates that are still pending.
// An old batch outcome is a reason to re-read a row, never a deletion instruction.
type ReviewCandidateRefresh struct {
	VideoItems []AITaggingReviewItem      `json:"video_items"`
	ImageItems []ImageAITaggingReviewItem `json:"image_items"`
}

func reviewRefreshIDs(ids []uint) ([]uint, error) {
	if len(ids) > 200 {
		return nil, errors.New("review_refresh_limit: 每次最多核对200条候选")
	}
	seen := map[uint]bool{}
	unique := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, errors.New("review_candidate_invalid: 无效的候选编号")
		}
		if !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	return unique, nil
}

func (s *AITaggingService) RefreshPendingReviewCandidates(ctx context.Context, ids []uint) ([]AITaggingReviewItem, error) {
	ids, err := reviewRefreshIDs(ids)
	if err != nil {
		return nil, err
	}
	items := []AITaggingReviewItem{}
	if len(ids) == 0 {
		return items, nil
	}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var rows []models.AITagCandidate
		if err := aiTagCandidateQueryDB(db, 0, "", "pending").Where("ai_tag_candidates.id IN ?", ids).Find(&rows).Error; err != nil {
			return err
		}
		items = aiTagCandidateReviewItems(rows)
		return nil
	})
	return items, err
}

func (s *ImageAITaggingService) RefreshPendingReviewCandidates(ctx context.Context, ids []uint) ([]ImageAITaggingReviewItem, error) {
	ids, err := reviewRefreshIDs(ids)
	if err != nil {
		return nil, err
	}
	items := []ImageAITaggingReviewItem{}
	if len(ids) == 0 {
		return items, nil
	}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		if s.db == nil || db.Statement.ConnPool != s.db.Statement.ConnPool {
			return ErrReviewClosed
		}
		var rows []models.ImageAITagCandidate
		if err := s.candidateQuery(0, "", "pending").WithContext(ctx).Where("image_ai_tag_candidates.id IN ?", ids).Find(&rows).Error; err != nil {
			return err
		}
		items = imageAITagCandidateReviewItems(rows)
		return nil
	})
	return items, err
}
