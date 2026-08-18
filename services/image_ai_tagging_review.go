package services

import (
	"fmt"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ImageAITaggingReviewItem 是候选审阅界面的展示单元，形态镜像 AITaggingReviewItem。
type ImageAITaggingReviewItem struct {
	ID             uint          `json:"id"`
	ImageID        uint          `json:"image_id"`
	Image          *models.Image `json:"image,omitempty"`
	ImageDeleted   bool          `json:"image_deleted"`
	SuggestedName  string        `json:"suggested_name"`
	NormalizedName string        `json:"normalized_name"`
	MatchedTagID   *uint         `json:"matched_tag_id,omitempty"`
	MatchedTag     *models.Tag   `json:"matched_tag,omitempty"`
	Confidence     string        `json:"confidence"`
	Reasoning      string        `json:"reasoning"`
	SourceSummary  string        `json:"source_summary"`
	Status         string        `json:"status"`
	CreatedAt      string        `json:"created_at"`
	UpdatedAt      string        `json:"updated_at"`
}

func imageAITagCandidateReviewItem(candidate models.ImageAITagCandidate) ImageAITaggingReviewItem {
	item := ImageAITaggingReviewItem{
		ID:             candidate.ID,
		ImageID:        candidate.ImageID,
		SuggestedName:  candidate.SuggestedName,
		NormalizedName: candidate.NormalizedName,
		MatchedTagID:   candidate.MatchedTagID,
		MatchedTag:     candidate.MatchedTag,
		Confidence:     candidate.Confidence,
		Reasoning:      candidate.Reasoning,
		SourceSummary:  candidate.SourceSummary,
		Status:         candidate.Status,
		CreatedAt:      candidate.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:      candidate.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
	if candidate.Image.ID != 0 {
		image := candidate.Image
		item.Image = &image
		item.ImageDeleted = image.DeletedAt.IsValid()
	}
	return item
}

// ListImageAITagCandidates 列出候选。imageID 为 0 表示不限图片；
// status 为空默认只看待审。Image 用 Unscoped 预载，软删的图片也要能在审阅界面看到来龙去脉。
func (s *ImageAITaggingService) ListImageAITagCandidates(imageID uint, confidence string, status string) ([]ImageAITaggingReviewItem, error) {
	query := s.db.
		Model(&models.ImageAITagCandidate{}).
		Preload("Image", func(db *gorm.DB) *gorm.DB { return db.Unscoped() }).
		Preload("Image.Tags").
		Preload("MatchedTag")
	if imageID > 0 {
		query = query.Where("image_ai_tag_candidates.image_id = ?", imageID)
	}
	if confidence = normalizeAIConfidence(confidence); confidence != "" {
		query = query.Where("image_ai_tag_candidates.confidence = ?", confidence)
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = models.AITagCandidateStatusPending
	}
	query = query.Where("image_ai_tag_candidates.status = ?", status)
	var candidates []models.ImageAITagCandidate
	if err := query.Order("image_ai_tag_candidates.created_at desc, image_ai_tag_candidates.id desc").Find(&candidates).Error; err != nil {
		return nil, err
	}
	items := make([]ImageAITaggingReviewItem, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, imageAITagCandidateReviewItem(candidate))
	}
	return items, nil
}

// activeImageExistsInTx 拒绝对已软删/不存在的图片做审批动作。
func activeImageExistsInTx(tx *gorm.DB, imageID uint) error {
	if imageID == 0 {
		return gorm.ErrRecordNotFound
	}
	var image models.Image
	return tx.Select("id").First(&image, imageID).Error
}

// hasManualOfficialImageTagsInTx 判断这张图是否有用户手工打的标签：
// image_tags 里的非自动标签关联，减去审批记录里记着的（AI 接受产生的），剩下的就是手工的。
// 这正是审批记录表存在的理由。
func (s *ImageAITaggingService) hasManualOfficialImageTagsInTx(tx *gorm.DB, imageID uint) (bool, error) {
	var officialCount int64
	if err := tx.Table("image_tags AS it").
		Joins("INNER JOIN tags ON tags.id = it.tag_id").
		Where("it.image_id = ? AND COALESCE(tags.automatic_kind, '') = ''", imageID).
		Count(&officialCount).Error; err != nil {
		return false, err
	}
	if officialCount == 0 {
		return false, nil
	}
	var aiApprovedCount int64
	if err := tx.Table("image_tags AS it").
		Joins("INNER JOIN image_ai_tag_approval_records AS ar ON ar.image_id = it.image_id AND ar.tag_id = it.tag_id").
		Where("it.image_id = ?", imageID).
		Count(&aiApprovedCount).Error; err != nil {
		return false, err
	}
	return officialCount > aiApprovedCount, nil
}

// resolveOfficialImageTagInTx 只接受仍在闭合词表中且启用的标签，防止候选产生后标签被停用
// 或移出词表还能被写入。
func (s *ImageAITaggingService) resolveOfficialImageTagInTx(tx *gorm.DB, candidate models.ImageAITagCandidate) (uint, error) {
	if candidate.MatchedTagID == nil {
		return 0, fmt.Errorf("候选未命中已配置的标签库")
	}
	var tag models.Tag
	if err := tx.First(&tag, *candidate.MatchedTagID).Error; err != nil {
		return 0, err
	}
	if !tag.IsSystem || !tag.IsActive {
		return 0, fmt.Errorf("候选对应的标签已不在启用的标签库中")
	}
	return tag.ID, nil
}

// ApproveImageAITagCandidate 接受候选：写 image_tags + 审批记录，并把同图同名的其他待审候选
// 置 superseded。事务语义逐条对齐视频侧 ApproveCandidate。
func (s *ImageAITaggingService) ApproveImageAITagCandidate(candidateID uint) (*ImageAITaggingReviewItem, error) {
	var approved models.ImageAITagCandidate
	err := database.Transaction(func(tx *gorm.DB) error {
		var candidate models.ImageAITagCandidate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", candidateID).First(&candidate).Error; err != nil {
			return err
		}
		if err := activeImageExistsInTx(tx, candidate.ImageID); err != nil {
			return err
		}
		if candidate.Status != models.AITagCandidateStatusPending {
			return fmt.Errorf("候选不是待审状态")
		}
		if candidate.Confidence != models.AITagConfidenceHigh && candidate.Confidence != models.AITagConfidenceMedium {
			return fmt.Errorf("候选置信度不可接受")
		}
		hasManualTags, err := s.hasManualOfficialImageTagsInTx(tx, candidate.ImageID)
		if err != nil {
			return err
		}
		if hasManualTags {
			// 用户已经自己给这张图打过标签：人的判断优先，AI 候选整体作废而不是叠加。
			now := s.now()
			if err := tx.Model(&models.ImageAITagCandidate{}).
				Where("image_id = ? AND status = ?", candidate.ImageID, models.AITagCandidateStatusPending).
				Updates(map[string]interface{}{
					"status":      models.AITagCandidateStatusSuperseded,
					"rejected_at": &now,
				}).Error; err != nil {
				return err
			}
			approved = candidate
			approved.Status = models.AITagCandidateStatusSuperseded
			approved.RejectedAt = &now
			return nil
		}
		tagID, err := s.resolveOfficialImageTagInTx(tx, candidate)
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, candidate.ImageID, tagID).Error; err != nil {
			return err
		}
		now := s.now()
		result := tx.Model(&models.ImageAITagCandidate{}).
			Where("id = ? AND status = ?", candidate.ID, models.AITagCandidateStatusPending).
			Updates(map[string]interface{}{
				"status":      models.AITagCandidateStatusApproved,
				"approved_at": &now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("候选已不是待审状态")
		}
		approvalRecord := models.ImageAITagApprovalRecord{
			ImageID:     candidate.ImageID,
			TagID:       tagID,
			CandidateID: candidate.ID,
		}
		if err := tx.Omit(clause.Associations).Clauses(clause.OnConflict{DoNothing: true}).Create(&approvalRecord).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.ImageAITagCandidate{}).
			Where("image_id = ? AND normalized_name = ? AND id <> ? AND status = ?", candidate.ImageID, candidate.NormalizedName, candidate.ID, models.AITagCandidateStatusPending).
			Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
			return err
		}
		approved = candidate
		approved.Status = models.AITagCandidateStatusApproved
		approved.ApprovedAt = &now
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.db.
		Preload("Image", func(db *gorm.DB) *gorm.DB { return db.Unscoped() }).
		Preload("Image.Tags").
		Preload("MatchedTag").
		First(&approved, candidateID).Error; err != nil {
		return nil, err
	}
	item := imageAITagCandidateReviewItem(approved)
	return &item, nil
}

// RejectImageAITagCandidate 拒绝单个待审候选。
func (s *ImageAITaggingService) RejectImageAITagCandidate(candidateID uint) error {
	now := s.now()
	result := s.db.Model(&models.ImageAITagCandidate{}).
		Where("id = ? AND status = ?", candidateID, models.AITagCandidateStatusPending).
		Updates(map[string]interface{}{
			"status":      models.AITagCandidateStatusRejected,
			"rejected_at": &now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("候选不是待审状态")
	}
	return nil
}

// RejectPendingImageAITagCandidatesByImage 一次性拒绝某张图的全部待审候选。
func (s *ImageAITaggingService) RejectPendingImageAITagCandidatesByImage(imageID uint) (int64, error) {
	now := s.now()
	var rejected int64
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := activeImageExistsInTx(tx, imageID); err != nil {
			return err
		}
		result := tx.Model(&models.ImageAITagCandidate{}).
			Where("image_id = ? AND status = ?", imageID, models.AITagCandidateStatusPending).
			Updates(map[string]interface{}{
				"status":      models.AITagCandidateStatusRejected,
				"rejected_at": &now,
			})
		if result.Error != nil {
			return result.Error
		}
		rejected = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return rejected, nil
}

// ImageAITaggingSummary 是照片页顶部的待审计数，让用户知道有多少候选等着处理。
type ImageAITaggingSummary struct {
	ConfigAvailable bool  `json:"config_available"`
	Pending         int64 `json:"pending"`
	PendingImages   int64 `json:"pending_images"`
}

// GetImageAITaggingSummary 返回待审候选数与涉及的图片数。
func (s *ImageAITaggingService) GetImageAITaggingSummary() (*ImageAITaggingSummary, error) {
	summary := &ImageAITaggingSummary{}
	if _, _, err := s.prepareClient(); err == nil {
		summary.ConfigAvailable = true
	}
	if err := s.db.Model(&models.ImageAITagCandidate{}).
		Where("status = ?", models.AITagCandidateStatusPending).
		Count(&summary.Pending).Error; err != nil {
		return nil, err
	}
	if err := s.db.Model(&models.ImageAITagCandidate{}).
		Where("status = ?", models.AITagCandidateStatusPending).
		Distinct("image_id").Count(&summary.PendingImages).Error; err != nil {
		return nil, err
	}
	return summary, nil
}
