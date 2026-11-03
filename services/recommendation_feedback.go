package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
)

type RecommendationFeedbackItem struct {
	ID         uint       `json:"id"`
	TargetKind string     `json:"target_kind"`
	TargetID   uint       `json:"target_id"`
	Effect     string     `json:"effect"`
	ExpiresAt  *time.Time `json:"expires_at" ts_type:"string"`
	Revision   int64      `json:"revision"`
	Label      string     `json:"label"`
	Active     bool       `json:"active"`
}

type RecommendationFeedbackRequest struct {
	TargetKind       string `json:"target_kind"`
	TargetID         uint   `json:"target_id"`
	ID               uint   `json:"id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// RecommendationTagPreferenceRequest edits one reviewed target, never an inferred preference.
type RecommendationTagPreferenceRequest struct {
	TagID            uint   `json:"tag_id"`
	Effect           string `json:"effect"`
	ID               uint   `json:"id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func claimTagPreference(tx *gorm.DB, tag models.Tag) error {
	changed := tx.Model(&models.Tag{}).Where("id = ? AND preference_revision = ?", tag.ID, tag.PreferenceRevision).UpdateColumn("preference_revision", gorm.Expr("preference_revision + 1"))
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return ErrRecommendationFeedbackConflict
	}
	return nil
}

// SetRecommendationTagPreference supports explicit positive, negative and neutral choices.
func (s *VideoService) SetRecommendationTagPreference(ctx context.Context, input RecommendationTagPreferenceRequest) (*models.RecommendationFeedback, error) {
	if input.TagID == 0 || input.TagID > experienceMaxSafeInteger || input.ID > experienceMaxSafeInteger || input.ExpectedRevision >= experienceMaxSafeInteger || (input.Effect != "boost" && input.Effect != "downweight" && input.Effect != "neutral") || (input.ID == 0) != (input.ExpectedRevision == 0) || input.ExpectedRevision < 0 {
		return nil, errors.New("invalid_input: 标签推荐偏好无效")
	}
	if input.Effect == "neutral" {
		if input.ID == 0 {
			return nil, errors.New("invalid_input: 请先读取当前偏好")
		}
		err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				var row models.RecommendationFeedback
				if err := tx.Where("id = ? AND revision = ? AND target_kind = ? AND target_id = ?", input.ID, input.ExpectedRevision, "tag", input.TagID).First(&row).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return ErrRecommendationFeedbackConflict
					}
					return err
				}
				var tag models.Tag
				if err := tx.First(&tag, input.TagID).Error; err != nil {
					return err
				}
				if err := claimTagPreference(tx, tag); err != nil {
					return err
				}
				changed := tx.Where("id = ? AND revision = ?", row.ID, row.Revision).Delete(&models.RecommendationFeedback{})
				if changed.Error != nil {
					return changed.Error
				}
				if changed.RowsAffected != 1 {
					return ErrRecommendationFeedbackConflict
				}
				return nil
			})
		})
		return nil, err
	}
	return s.setRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "tag", TargetID: input.TagID, ID: input.ID, ExpectedRevision: input.ExpectedRevision}, input.Effect)
}

var ErrRecommendationFeedbackConflict = errors.New("推荐反馈已变化，请刷新列表后重试")

// SetRecommendationFeedback creates a preference or updates the exact row/revision the user reviewed.
func (s *VideoService) SetRecommendationFeedback(ctx context.Context, request RecommendationFeedbackRequest) (*models.RecommendationFeedback, error) {
	return s.setRecommendationFeedback(ctx, request, "downweight")
}

func (s *VideoService) setRecommendationFeedback(ctx context.Context, request RecommendationFeedbackRequest, tagEffect string) (*models.RecommendationFeedback, error) {
	kind, id := request.TargetKind, request.TargetID
	if id == 0 || (kind != "work_video" && kind != "tag" && kind != "work_group") || (request.ID == 0) != (request.ExpectedRevision == 0) || request.ExpectedRevision < 0 || kind == "work_group" && request.ID == 0 {
		return nil, errors.New("推荐反馈目标无效")
	}
	var result models.RecommendationFeedback
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			now := time.Now()
			effect := tagEffect
			var expires *time.Time
			scope, err := applyScanScope(tx.Model(&models.Video{}), true)
			if err != nil {
				return err
			}
			switch kind {
			case "tag":
				var tag models.Tag
				if err := tx.First(&tag, id).Error; err != nil {
					return errors.New("标签已不存在，请刷新")
				}
				if err := claimTagPreference(tx, tag); err != nil {
					return err
				}
			case "work_video":
				var video models.Video
				if err := scope.First(&video, id).Error; err != nil {
					return errors.New("视频已不在可访问的片库范围内")
				}
				if request.ID == 0 {
					var member models.VideoVersionMember
					err := tx.First(&member, "video_id = ?", id).Error
					if err == nil {
						kind = "work_group"
						id = member.GroupID
					} else if !errors.Is(err, gorm.ErrRecordNotFound) {
						return err
					}
				}
			case "work_group":
				var count int64
				if err := scope.Where("videos.id IN (?)", tx.Model(&models.VideoVersionMember{}).Select("video_id").Where("group_id = ?", id)).Count(&count).Error; err != nil {
					return err
				}
				if count == 0 {
					return errors.New("作品已不在可访问的片库范围内")
				}
			}
			if kind != "tag" {
				effect = "snooze"
				until := now.Add(7 * 24 * time.Hour)
				expires = &until
			}
			if request.ID == 0 {
				result = models.RecommendationFeedback{TargetKind: kind, TargetID: id, Effect: effect, ExpiresAt: expires, Revision: 1, CreatedAt: now, UpdatedAt: now}
				created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&result)
				if created.Error != nil {
					return created.Error
				}
				if created.RowsAffected != 1 {
					return ErrRecommendationFeedbackConflict
				}
				return nil
			}
			changed := tx.Model(&models.RecommendationFeedback{}).Where("id = ? AND revision = ? AND target_kind = ? AND target_id = ?", request.ID, request.ExpectedRevision, kind, id).Updates(map[string]any{"effect": effect, "expires_at": expires, "revision": gorm.Expr("revision + 1"), "updated_at": now})
			if changed.Error != nil {
				return changed.Error
			}
			if changed.RowsAffected != 1 {
				return ErrRecommendationFeedbackConflict
			}
			return tx.First(&result, request.ID).Error
		})
	})
	return &result, err
}

// recommendationTargetScope is shared by selection and feedback management. Offline
// members retain identity; deleted, blacklisted and out-of-scope targets do not.
func recommendationTargetScope(query, db *gorm.DB) (*gorm.DB, error) {
	accessible, err := applyScanScope(db.Model(&models.Video{}), true)
	if err != nil {
		return nil, err
	}
	videoIDs := accessible.Select("videos.id")
	groupIDs := db.Model(&models.VideoVersionMember{}).Select("group_id").Where("video_id IN (?)", videoIDs)
	groups := db.Model(&models.VideoVersionGroup{}).Select("id").Where("id IN (?)", groupIDs)
	tags := db.Model(&models.Tag{}).Select("id")
	return query.Where("(target_kind = ? AND target_id IN (?)) OR (target_kind = ? AND target_id IN (?)) OR (target_kind = ? AND target_id IN (?))", "work_video", videoIDs, "work_group", groups, "tag", tags), nil
}

func (s *VideoService) ListRecommendationFeedback(ctx context.Context) ([]RecommendationFeedbackItem, error) {
	result := []RecommendationFeedbackItem{}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var rows []models.RecommendationFeedback
		if err := db.Order("id DESC").Find(&rows).Error; err != nil {
			return err
		}
		activeQuery, err := recommendationTargetScope(db.Model(&models.RecommendationFeedback{}), db)
		if err != nil {
			return err
		}
		var activeIDs []uint
		if err := activeQuery.Where("expires_at IS NULL OR expires_at > ?", time.Now()).Pluck("id", &activeIDs).Error; err != nil {
			return err
		}
		active := make(map[uint]bool, len(activeIDs))
		for _, id := range activeIDs {
			active[id] = true
		}
		for _, row := range rows {
			item := RecommendationFeedbackItem{ID: row.ID, TargetKind: row.TargetKind, TargetID: row.TargetID, Effect: row.Effect, ExpiresAt: row.ExpiresAt, Revision: row.Revision, Label: "目标已失效"}
			var label string
			var err error
			switch row.TargetKind {
			case "tag":
				var value models.Tag
				err = db.First(&value, row.TargetID).Error
				label = value.Name
			case "work_group":
				var value models.VideoVersionGroup
				err = db.First(&value, row.TargetID).Error
				label = value.Title
				if err == nil && strings.TrimSpace(label) == "" {
					var representative models.Video
					queryErr := db.Model(&models.Video{}).Joins("JOIN video_version_members ON video_version_members.video_id = videos.id").Where("video_version_members.group_id = ?", row.TargetID).Order("video_version_members.position, videos.id").First(&representative).Error
					if queryErr != nil && !errors.Is(queryErr, gorm.ErrRecordNotFound) {
						return queryErr
					}
					label = diaryVideoTitle(representative)
					if strings.TrimSpace(label) == "" {
						label = fmt.Sprintf("版本组 %d", row.TargetID)
					}
				}
			case "work_video":
				var value models.Video
				err = db.First(&value, row.TargetID).Error
				label = value.Name
			default:
				err = gorm.ErrRecordNotFound
			}
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				item.Label = label
				item.Active = active[row.ID]
			}
			result = append(result, item)
		}
		return nil
	})
	return result, err
}
func (s *VideoService) RevokeRecommendationFeedback(ctx context.Context, id uint, revision int64) error {
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var row models.RecommendationFeedback
			if err := tx.First(&row, "id = ? AND revision = ?", id, revision).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrRecommendationFeedbackConflict
				}
				return err
			}
			if row.TargetKind == "tag" {
				var tag models.Tag
				err := tx.First(&tag, row.TargetID).Error
				if err == nil {
					if err := claimTagPreference(tx, tag); err != nil {
						return err
					}
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			}
			result := tx.Where("id = ? AND revision = ?", id, revision).Delete(&models.RecommendationFeedback{})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrRecommendationFeedbackConflict
			}
			return nil
		})
	})
}

func mergeRecommendationTagFeedback(tx *gorm.DB, sources []uint, target uint) error {
	var rows []models.RecommendationFeedback
	if err := tx.Where("target_kind = ? AND target_id IN ?", "tag", append(append([]uint{}, sources...), target)).Find(&rows).Error; err != nil {
		return err
	}
	effect := "boost"
	var destination *models.RecommendationFeedback
	for i := range rows {
		if rows[i].Effect == "downweight" {
			effect = "downweight"
		}
		if rows[i].TargetID == target {
			destination = &rows[i]
		}
	}
	if len(rows) == 0 {
		return nil
	}
	if destination != nil {
		changed := tx.Model(&models.RecommendationFeedback{}).Where("id = ? AND revision = ?", destination.ID, destination.Revision).Updates(map[string]any{"effect": effect, "revision": destination.Revision + 1, "updated_at": time.Now()})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return ErrRecommendationFeedbackConflict
		}
	} else {
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.RecommendationFeedback{TargetKind: "tag", TargetID: target, Effect: effect, Revision: 1})
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected != 1 {
			return ErrRecommendationFeedbackConflict
		}
	}
	for _, row := range rows {
		if row.TargetID == target {
			continue
		}
		changed := tx.Where("id = ? AND revision = ?", row.ID, row.Revision).Delete(&models.RecommendationFeedback{})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return ErrRecommendationFeedbackConflict
		}
	}
	return nil
}
