package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"sort"
	"time"
	"video-master/database"
	"video-master/models"
)

// TagMergePreferencePreview exposes only the reviewed preference outcome.
type TagMergePreferencePreview struct {
	Token       string `json:"token"`
	Effect      string `json:"effect"`
	HasFeedback bool   `json:"has_feedback"`
}
type tagMergePreview struct {
	sources   []uint
	target    uint
	signature string
	db        *gorm.DB
	owner     context.Context
	expires   time.Time
	result    *MergeTagsResult
}

func tagPreferenceSnapshot(db *gorm.DB, tags []models.Tag) (string, string, bool, error) {
	sort.Slice(tags, func(i, j int) bool { return tags[i].ID < tags[j].ID })
	ids := make([]uint, 0, len(tags))
	roots := make([][2]int64, 0, len(tags))
	for _, tag := range tags {
		ids = append(ids, tag.ID)
		roots = append(roots, [2]int64{int64(tag.ID), tag.PreferenceRevision})
	}
	var rows []models.RecommendationFeedback
	if err := db.Where("target_kind = ? AND target_id IN ?", "tag", ids).Order("id").Find(&rows).Error; err != nil {
		return "", "", false, err
	}
	effect := ""
	if len(rows) > 0 {
		effect = "boost"
	}
	for _, row := range rows {
		if row.Effect == "downweight" {
			effect = "downweight"
		}
	}
	encoded, err := json.Marshal(struct {
		Roots [][2]int64
		Rows  []models.RecommendationFeedback
	}{roots, rows})
	if err != nil {
		return "", "", false, err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), effect, len(rows) > 0, nil
}

// PreviewTagMergePreferences captures both existing rows and the absence of rows.
func (s *TagService) PreviewTagMergePreferences(ctx context.Context, sources []uint, target uint) (*TagMergePreferencePreview, error) {
	if target == 0 || target > experienceMaxSafeInteger || len(sources) == 0 || len(sources) > 200 {
		return nil, errors.New("invalid_input: 请选择1至200个来源标签")
	}
	seen := map[uint]bool{target: true}
	for _, id := range sources {
		if id == 0 || id > experienceMaxSafeInteger || seen[id] {
			return nil, errors.New("invalid_input: 来源标签必须有效、互不重复且不包含目标")
		}
		seen[id] = true
	}
	sources = append([]uint{}, sources...)
	var result TagMergePreferencePreview
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var tags []models.Tag
		if err := db.Where("id IN ?", append(append([]uint{}, sources...), target)).Find(&tags).Error; err != nil {
			return err
		}
		if len(tags) != len(sources)+1 {
			return gorm.ErrRecordNotFound
		}
		for _, tag := range tags {
			if tag.AutomaticKind != "" {
				return errors.New("invalid_input: 自动标签不能合并")
			}
		}
		signature, effect, hasFeedback, err := tagPreferenceSnapshot(db, tags)
		if err != nil {
			return err
		}
		result.Effect, result.HasFeedback = effect, hasFeedback
		if !hasFeedback {
			return nil
		}
		s.mergeMu.Lock()
		defer s.mergeMu.Unlock()
		if s.mergePreviews == nil {
			s.mergePreviews = map[string]*tagMergePreview{}
		}
		for key, preview := range s.mergePreviews {
			if preview.owner.Err() != nil || time.Now().After(preview.expires) {
				delete(s.mergePreviews, key)
			}
		}
		if len(s.mergePreviews) >= 16 {
			return errors.New("invalid_input: 合并预览过多，请稍后重试")
		}
		result.Token = uuid.NewString()
		s.mergePreviews[result.Token] = &tagMergePreview{sources: append([]uint{}, sources...), target: target, signature: signature, db: database.DB, owner: ctx, expires: time.Now().Add(10 * time.Minute)}
		return nil
	})
	return &result, err
}

// MergeTagsWithoutFeedback preserves the old binding for a merge with no preference changes.
func (s *TagService) MergeTagsWithoutFeedback(ctx context.Context, sources []uint, target uint) (*MergeTagsResult, error) {
	return s.mergeTags(ctx, sources, target, func(db *gorm.DB, tags []models.Tag) error {
		_, _, has, err := tagPreferenceSnapshot(db, tags)
		if err != nil {
			return err
		}
		if has {
			return ErrRecommendationFeedbackConflict
		}
		return nil
	})
}

// MergeTagsFromPreferencePreview applies exactly the captured targets and versions.
func (s *TagService) MergeTagsFromPreferencePreview(ctx context.Context, token string) (*MergeTagsResult, error) {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	preview := s.mergePreviews[token]
	if preview == nil || preview.db != database.DB || preview.owner.Err() != nil || time.Now().After(preview.expires) {
		return nil, ErrRecommendationFeedbackConflict
	}
	if preview.result != nil {
		return preview.result, nil
	}
	result, err := s.mergeTags(ctx, preview.sources, preview.target, func(db *gorm.DB, tags []models.Tag) error {
		signature, _, _, err := tagPreferenceSnapshot(db, tags)
		if err != nil {
			return err
		}
		if signature != preview.signature {
			return ErrRecommendationFeedbackConflict
		}
		return nil
	})
	if err == nil {
		preview.result = result
	}
	return result, err
}
