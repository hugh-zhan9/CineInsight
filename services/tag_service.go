package services

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TagService struct{}

var ErrAITagLibraryEmptyConfirmationRequired = errors.New("AI 标签库非空，拒绝未经确认的空保存")

const (
	ShortVideoTagName             = "短视频"
	LowResolutionTagName          = "低清"
	shortVideoAutomaticTagKind    = "short_video"
	lowResolutionAutomaticTagKind = "low_resolution"
	automaticTagNamespace         = "自动"
)

type MergeTagsResult struct {
	TargetTagID     uint `json:"target_tag_id"`
	MergedTagCount  int  `json:"merged_tag_count"`
	VideoLinksMoved int  `json:"video_links_moved"`
	ImageLinksMoved int  `json:"image_links_moved"`
}

type ShortVideoTagSyncResult struct {
	TagID   uint  `json:"tag_id"`
	Added   int64 `json:"added"`
	Removed int64 `json:"removed"`
}

func (s *TagService) GetAITagLibrary() ([]models.Tag, error) {
	var tags []models.Tag
	err := database.DB.Where("is_system = ?", true).
		Order("namespace asc, sort_order asc, id asc").
		Find(&tags).Error
	return tags, err
}

func (s *TagService) SaveAITagLibrary(inputs []AITagLibraryInput) ([]models.Tag, error) {
	return s.saveAITagLibrary(inputs, false)
}

// ClearAITagLibrary is the explicit destructive counterpart to SaveAITagLibrary.
// Keeping the empty operation separate prevents a failed/stale client load from
// being interpreted as an intentional deletion of the entire AI tag library.
func (s *TagService) ClearAITagLibrary() ([]models.Tag, error) {
	return s.saveAITagLibrary(nil, true)
}

func (s *TagService) saveAITagLibrary(inputs []AITagLibraryInput, allowEmpty bool) ([]models.Tag, error) {
	normalizedNames := make(map[string]struct{}, len(inputs))
	for i := range inputs {
		inputs[i].Name = strings.TrimSpace(inputs[i].Name)
		inputs[i].Namespace = strings.TrimSpace(inputs[i].Namespace)
		inputs[i].Color = strings.TrimSpace(inputs[i].Color)
		if inputs[i].Name == "" {
			return nil, fmt.Errorf("标签名称不能为空")
		}
		normalized := normalizeAITagName(inputs[i].Name)
		if _, exists := normalizedNames[normalized]; exists {
			return nil, fmt.Errorf("标签名称重复: %s", inputs[i].Name)
		}
		normalizedNames[normalized] = struct{}{}
	}

	err := database.Transaction(func(tx *gorm.DB) error {
		var existingSystemTags []models.Tag
		if err := tx.Where("is_system = ?", true).Find(&existingSystemTags).Error; err != nil {
			return err
		}
		if len(inputs) == 0 && len(existingSystemTags) > 0 && !allowEmpty {
			return ErrAITagLibraryEmptyConfirmationRequired
		}
		submittedIDs := make(map[uint]struct{}, len(inputs))
		namespaceOrders := make(map[string]int)
		libraryChanged := false

		for _, input := range inputs {
			namespaceOrders[input.Namespace]++
			color := input.Color
			if color == "" {
				color = tagColorPalette[len(submittedIDs)%len(tagColorPalette)]
			}
			var tag models.Tag
			if input.ID > 0 {
				if err := tx.Unscoped().First(&tag, input.ID).Error; err != nil {
					return err
				}
				if tag.AutomaticKind != "" {
					return fmt.Errorf("自动标签不能加入 AI 标签库: %s", tag.Name)
				}
				var conflict models.Tag
				err := tx.Unscoped().Where("name = ? AND id <> ?", input.Name, tag.ID).First(&conflict).Error
				if err == nil {
					if conflict.AutomaticKind != "" {
						return fmt.Errorf("自动标签不能加入 AI 标签库: %s", conflict.Name)
					}
					if conflict.IsSystem {
						return fmt.Errorf("标签名称已存在于 AI 标签库: %s", input.Name)
					}
					// Reuse the existing manual tag instead of renaming over it. This
					// preserves all of its current video relationships when users add
					// that name to the AI library from an existing library row.
					tag = conflict
				}
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					if err != nil {
						return err
					}
				}
			} else {
				err := tx.Unscoped().Where("name = ?", input.Name).First(&tag).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if errors.Is(err, gorm.ErrRecordNotFound) {
					tag = models.Tag{Name: input.Name}
				}
			}
			if tag.AutomaticKind != "" {
				return fmt.Errorf("自动标签不能加入 AI 标签库: %s", tag.Name)
			}
			if input.Namespace == automaticTagNamespace && tag.Namespace != automaticTagNamespace {
				return fmt.Errorf("自动是系统分类，不能手动分配")
			}

			sortOrder := namespaceOrders[input.Namespace]
			nameChanged := tag.ID != 0 && tag.Name != input.Name
			changed := tag.ID == 0 ||
				tag.Name != input.Name ||
				tag.Namespace != input.Namespace ||
				tag.Color != color ||
				!tag.IsSystem ||
				tag.IsActive != input.IsActive ||
				tag.ReviewRequired != input.ReviewRequired ||
				tag.SortOrder != sortOrder ||
				tag.DeletedAt.IsValid()
			if changed {
				isNew := tag.ID == 0
				tag.Name = input.Name
				tag.Namespace = input.Namespace
				tag.Color = color
				tag.IsSystem = true
				tag.IsActive = input.IsActive
				tag.ReviewRequired = input.ReviewRequired
				tag.SortOrder = sortOrder
				tag.DeletedAt.Clear()
				if err := tx.Unscoped().Save(&tag).Error; err != nil {
					return err
				}
				if isNew && !input.IsActive {
					if err := tx.Model(&tag).UpdateColumn("is_active", false).Error; err != nil {
						return err
					}
					tag.IsActive = false
				}
				if nameChanged {
					if err := tx.Model(&models.AITagCandidate{}).
						Where("matched_tag_id = ? AND status = ?", tag.ID, models.AITagCandidateStatusPending).
						Updates(map[string]interface{}{
							"suggested_name":  tag.Name,
							"normalized_name": normalizeAITagName(tag.Name),
						}).Error; err != nil {
						return err
					}
					// 图片侧候选改为作废而不是就地改名：image_ai_tag_candidates 上有
					// (image_id, normalized_name) WHERE status='pending' 的部分唯一索引，
					// 把「日落」改名成「海边」会和同一张图已有的「海边」候选撞键，
					// 整个保存事务跟着失败。作废是安全的——图片打标的证据指纹含标签名，
					// 改名本身就会让指纹失配，下一轮会用新名字重新提出候选。
					if err := tx.Model(&models.ImageAITagCandidate{}).
						Where("matched_tag_id = ? AND status = ?", tag.ID, models.AITagCandidateStatusPending).
						Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
						return err
					}
				}
				libraryChanged = true
			}
			submittedIDs[tag.ID] = struct{}{}
		}

		for _, tag := range existingSystemTags {
			if _, submitted := submittedIDs[tag.ID]; submitted {
				continue
			}
			if err := tx.Model(&tag).Updates(map[string]interface{}{
				"is_system":       false,
				"is_active":       true,
				"review_required": false,
				"sort_order":      0,
			}).Error; err != nil {
				return err
			}
			libraryChanged = true
		}
		if !libraryChanged {
			return nil
		}
		return resetAITaggingAfterLibraryChange(tx)
	})
	if err != nil {
		return nil, err
	}
	return s.GetAITagLibrary()
}

func resetAITaggingAfterLibraryChange(tx *gorm.DB) error {
	if err := tx.Model(&models.AITagCandidate{}).
		Where("status = ?", models.AITagCandidateStatusPending).
		Where(`matched_tag_id IS NULL OR NOT EXISTS (
				SELECT 1 FROM tags
				WHERE tags.id = ai_tag_candidates.matched_tag_id
					AND tags.deleted_at IS NULL
					AND tags.is_system = ?
					AND tags.is_active = ?
			)`, true, true).
		Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
		return err
	}
	// 图片侧同理：标签被移出词表或停用后，指向它的待审候选不该继续挂着等人处理。
	if err := tx.Model(&models.ImageAITagCandidate{}).
		Where("status = ?", models.AITagCandidateStatusPending).
		Where(`matched_tag_id IS NULL OR NOT EXISTS (
				SELECT 1 FROM tags
				WHERE tags.id = image_ai_tag_candidates.matched_tag_id
					AND tags.deleted_at IS NULL
					AND tags.is_system = ?
					AND tags.is_active = ?
			)`, true, true).
		Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.AITaggingState{}).
		Where(`NOT EXISTS (
				SELECT 1 FROM video_tags
				INNER JOIN tags ON tags.id = video_tags.tag_id
				WHERE video_tags.video_id = ai_tagging_states.video_id
					AND COALESCE(tags.automatic_kind, '') = ''
			)`).
		Updates(map[string]interface{}{
			"status":               models.AITaggingStateStatusPending,
			"skip_reason":          "",
			"evidence_fingerprint": "",
			"last_error":           "",
		}).Error; err != nil {
		return err
	}
	return nil
}

// Category names are derived from tags; no empty category is persisted.
func (s *TagService) CreateTagCategory(name string, tagIDs []uint) error {
	name = strings.TrimSpace(name)
	if name == "" || len(tagIDs) == 0 {
		return fmt.Errorf("分类名称和至少一个标签不能为空")
	}
	if name == automaticTagNamespace {
		return fmt.Errorf("自动是系统分类，不能手动创建")
	}
	return database.Transaction(func(tx *gorm.DB) error {
		if exists, err := tagCategoryExists(tx, name); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("分类已存在: %s", name)
		}
		ids := make(map[uint]struct{}, len(tagIDs))
		for _, id := range tagIDs {
			if id == 0 {
				return fmt.Errorf("标签不存在")
			}
			ids[id] = struct{}{}
		}
		var tags []models.Tag
		if err := tx.Where("id IN ?", tagIDs).Find(&tags).Error; err != nil {
			return err
		}
		if len(tags) != len(ids) {
			return fmt.Errorf("标签不存在")
		}
		affectedAI := false
		for _, tag := range tags {
			if tag.AutomaticKind != "" {
				return fmt.Errorf("自动标签不能加入分类")
			}
			if tag.IsSystem {
				affectedAI = true
			}
		}
		if err := tx.Model(&models.Tag{}).Where("id IN ?", tagIDs).Update("namespace", name).Error; err != nil {
			return err
		}
		if affectedAI {
			return resetAITaggingAfterLibraryChange(tx)
		}
		return nil
	})
}

func (s *TagService) RenameTagCategory(oldName, newName string) error {
	oldName, newName = strings.TrimSpace(oldName), strings.TrimSpace(newName)
	if oldName == "" || newName == "" || oldName == newName {
		return fmt.Errorf("分类名称无效")
	}
	if oldName == automaticTagNamespace || newName == automaticTagNamespace {
		return fmt.Errorf("自动是系统分类，不能手动改名")
	}
	return s.changeTagCategory(oldName, newName, false)
}

func (s *TagService) DeleteTagCategory(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("分类名称不能为空")
	}
	if name == automaticTagNamespace {
		return fmt.Errorf("自动是系统分类，不能手动删除")
	}
	return s.changeTagCategory(name, "", true)
}

func (s *TagService) changeTagCategory(oldName, newName string, deleting bool) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var tags []models.Tag
		if err := tx.Where("namespace = ? AND COALESCE(automatic_kind, '') = ''", oldName).Find(&tags).Error; err != nil {
			return err
		}
		if len(tags) == 0 {
			return fmt.Errorf("分类不存在: %s", oldName)
		}
		if !deleting {
			if exists, err := tagCategoryExists(tx, newName); err != nil {
				return err
			} else if exists {
				return fmt.Errorf("分类已存在: %s", newName)
			}
		}
		ids := make([]uint, 0, len(tags))
		affectedAI := false
		for _, tag := range tags {
			ids = append(ids, tag.ID)
			affectedAI = affectedAI || tag.IsSystem
		}
		if err := tx.Model(&models.Tag{}).Where("id IN ?", ids).Update("namespace", newName).Error; err != nil {
			return err
		}
		if affectedAI {
			return resetAITaggingAfterLibraryChange(tx)
		}
		return nil
	})
}

func tagCategoryExists(tx *gorm.DB, name string) (bool, error) {
	var count int64
	err := tx.Model(&models.Tag{}).Where("LOWER(namespace) = LOWER(?) AND COALESCE(automatic_kind, '') = ''", name).Count(&count).Error
	return count > 0, err
}

// GetAllTags 获取所有标签
func (s *TagService) GetAllTags() ([]models.Tag, error) {
	var tags []models.Tag
	err := database.DB.Order("name").Find(&tags).Error
	return tags, err
}

// GetImageTags 仅返回实际关联过图片的标签（image_tags 中出现过的 tag），
// 供图片库筛选栏使用，避免展示只被视频使用的标签。
func (s *TagService) GetImageTags() ([]models.Tag, error) {
	var tags []models.Tag
	err := database.DB.
		Joins("JOIN image_tags ON image_tags.tag_id = tags.id").
		Group("tags.id").
		Order("tags.name").
		Find(&tags).Error
	return tags, err
}

// MergeTags retains targetTagID, unions all video and recommendation links into
// it, rewrites AI history references, and soft-deletes the source tags.
func (s *TagService) MergeTags(sourceTagIDs []uint, targetTagID uint) (*MergeTagsResult, error) {
	uniqueSources := make([]uint, 0, len(sourceTagIDs))
	seen := make(map[uint]struct{}, len(sourceTagIDs))
	for _, id := range sourceTagIDs {
		if id == 0 || id == targetTagID {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		uniqueSources = append(uniqueSources, id)
	}
	if targetTagID == 0 || len(uniqueSources) == 0 {
		return nil, fmt.Errorf("请选择目标标签和至少一个待合并标签")
	}

	result := &MergeTagsResult{TargetTagID: targetTagID, MergedTagCount: len(uniqueSources)}
	err := database.Transaction(func(tx *gorm.DB) error {
		var target models.Tag
		if err := tx.First(&target, targetTagID).Error; err != nil {
			return fmt.Errorf("目标标签不存在: %w", err)
		}
		var sources []models.Tag
		if err := tx.Where("id IN ?", uniqueSources).Find(&sources).Error; err != nil {
			return err
		}
		if len(sources) != len(uniqueSources) {
			return fmt.Errorf("部分待合并标签不存在")
		}
		if target.AutomaticKind != "" {
			return fmt.Errorf("自动标签不能作为合并目标")
		}
		for _, source := range sources {
			if source.AutomaticKind != "" {
				return fmt.Errorf("自动标签不能合并: %s", source.Name)
			}
		}

		insertResult := tx.Exec(`
			INSERT INTO video_tags(video_id, tag_id)
			SELECT video_id, ? FROM video_tags WHERE tag_id IN ?
			ON CONFLICT DO NOTHING
		`, targetTagID, uniqueSources)
		if insertResult.Error != nil {
			return insertResult.Error
		}
		result.VideoLinksMoved = int(insertResult.RowsAffected)
		if err := tx.Exec("DELETE FROM video_tags WHERE tag_id IN ?", uniqueSources).Error; err != nil {
			return err
		}

		// D-002: 图片侧关联同步改写到目标标签并按 (image_id, tag_id) 唯一键去重。
		imageInsertResult := tx.Exec(`
			INSERT INTO image_tags(image_id, tag_id)
			SELECT image_id, ? FROM image_tags WHERE tag_id IN ?
			ON CONFLICT DO NOTHING
		`, targetTagID, uniqueSources)
		if imageInsertResult.Error != nil {
			return imageInsertResult.Error
		}
		result.ImageLinksMoved = int(imageInsertResult.RowsAffected)
		if err := tx.Exec("DELETE FROM image_tags WHERE tag_id IN ?", uniqueSources).Error; err != nil {
			return err
		}

		pendingCandidates := tx.Model(&models.AITagCandidate{}).
			Where("matched_tag_id IN ? AND status = ?", uniqueSources, models.AITagCandidateStatusPending)
		if target.IsSystem {
			if err := pendingCandidates.Updates(map[string]interface{}{
				"suggested_name":  target.Name,
				"normalized_name": normalizeAITagName(target.Name),
			}).Error; err != nil {
				return err
			}
		} else if err := pendingCandidates.Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.AITagCandidate{}).
			Where("matched_tag_id IN ?", uniqueSources).
			Update("matched_tag_id", targetTagID).Error; err != nil {
			return err
		}
		approvalTagIDs := append([]uint{targetTagID}, uniqueSources...)
		var approvals []models.AITagApprovalRecord
		if err := tx.Where("tag_id IN ?", approvalTagIDs).Order("id").Find(&approvals).Error; err != nil {
			return err
		}
		keptApprovalByVideo := make(map[uint]models.AITagApprovalRecord)
		for _, approval := range approvals {
			kept, exists := keptApprovalByVideo[approval.VideoID]
			if !exists || (kept.TagID != targetTagID && approval.TagID == targetTagID) {
				keptApprovalByVideo[approval.VideoID] = approval
			}
		}
		deleteApprovalIDs := make([]uint, 0)
		for _, approval := range approvals {
			if keptApprovalByVideo[approval.VideoID].ID != approval.ID {
				deleteApprovalIDs = append(deleteApprovalIDs, approval.ID)
			}
		}
		if len(deleteApprovalIDs) > 0 {
			if err := tx.Where("id IN ?", deleteApprovalIDs).Delete(&models.AITagApprovalRecord{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&models.AITagApprovalRecord{}).
			Where("tag_id IN ?", uniqueSources).
			Update("tag_id", targetTagID).Error; err != nil {
			return err
		}

		var sourcePreferenceScore float64
		if err := tx.Model(&models.ShortFeedTagPreference{}).
			Where("tag_id IN ?", uniqueSources).
			Select("COALESCE(SUM(score), 0)").
			Scan(&sourcePreferenceScore).Error; err != nil {
			return err
		}
		if sourcePreferenceScore != 0 {
			var targetPreference models.ShortFeedTagPreference
			err := tx.Where("tag_id = ?", targetTagID).First(&targetPreference).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				targetPreference = models.ShortFeedTagPreference{TagID: targetTagID, Score: sourcePreferenceScore}
				if err := tx.Create(&targetPreference).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if err := tx.Model(&targetPreference).Update("score", targetPreference.Score+sourcePreferenceScore).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("tag_id IN ?", uniqueSources).Delete(&models.ShortFeedTagPreference{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN ?", uniqueSources).Delete(&models.Tag{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *TagService) SyncShortVideoTags() (*ShortVideoTagSyncResult, error) {
	result := &ShortVideoTagSyncResult{}
	err := database.Transaction(func(tx *gorm.DB) error {
		return syncShortVideoTagsWithResult(tx, result)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func syncShortVideoTags(tx *gorm.DB) error {
	return syncShortVideoTagsWithResult(tx, &ShortVideoTagSyncResult{})
}

func syncShortVideoTagsWithResult(tx *gorm.DB, result *ShortVideoTagSyncResult) error {
	maxDurationSeconds, err := shortVideoMaxDurationSeconds(tx)
	if err != nil {
		return err
	}
	shortTag, err := ensureShortVideoAutomaticTag(tx, true)
	if err != nil {
		return err
	}
	lowTag, err := ensureLowResolutionAutomaticTag(tx, true)
	if err != nil {
		return err
	}
	result.TagID = shortTag.ID
	result.Added, result.Removed, err = syncAutomaticTagBulk(tx, shortTag, "v.is_stale = ? AND v.duration > ? AND v.duration < ?", false, 0, maxDurationSeconds)
	if err != nil {
		return err
	}
	_, _, err = syncAutomaticTagBulk(tx, lowTag, "v.is_stale = ? AND v.height > ? AND v.height < ?", false, 0, 1080)
	return err
}

// Sync one rule without changing rows for which the user recorded a decision.
func syncAutomaticTagBulk(tx *gorm.DB, tag *models.Tag, eligibility string, args ...interface{}) (int64, int64, error) {
	addArgs := append([]interface{}{tag.ID, tag.AutomaticKind, true}, args...)
	added := tx.Exec(`
        INSERT INTO video_tags(video_id, tag_id)
        SELECT v.id, ? FROM videos v
        LEFT JOIN video_automatic_tag_overrides o ON o.video_id = v.id AND o.automatic_kind = ?
        WHERE v.deleted_at IS NULL AND ((o.id IS NOT NULL AND o.present = ?) OR (o.id IS NULL AND (`+eligibility+`)))
        ON CONFLICT DO NOTHING
    `, addArgs...)
	if added.Error != nil {
		return 0, 0, added.Error
	}
	removeArgs := append([]interface{}{tag.ID, tag.AutomaticKind, false}, args...)
	removed := tx.Exec(`
        DELETE FROM video_tags WHERE tag_id = ? AND video_id IN (
            SELECT v.id FROM videos v
            LEFT JOIN video_automatic_tag_overrides o ON o.video_id = v.id AND o.automatic_kind = ?
            WHERE v.deleted_at IS NULL AND ((o.id IS NOT NULL AND o.present = ?) OR (o.id IS NULL AND NOT (`+eligibility+`)))
        )
    `, removeArgs...)
	if removed.Error != nil {
		return 0, 0, removed.Error
	}
	return added.RowsAffected, removed.RowsAffected, nil
}

func syncShortVideoTagForVideo(tx *gorm.DB, videoID uint) error {
	maxDurationSeconds, err := shortVideoMaxDurationSeconds(tx)
	if err != nil {
		return err
	}
	var video models.Video
	if err := tx.First(&video, videoID).Error; err != nil {
		return err
	}
	shortTag, err := ensureShortVideoAutomaticTag(tx, true)
	if err != nil {
		return err
	}
	lowTag, err := ensureLowResolutionAutomaticTag(tx, true)
	if err != nil {
		return err
	}
	if err := syncAutomaticTagForVideo(tx, &video, shortTag, !video.IsStale && video.Duration > 0 && video.Duration < maxDurationSeconds); err != nil {
		return err
	}
	return syncAutomaticTagForVideo(tx, &video, lowTag, !video.IsStale && video.Height > 0 && video.Height < 1080)
}

func syncAutomaticTagForVideo(tx *gorm.DB, video *models.Video, tag *models.Tag, eligible bool) error {
	var override models.VideoAutomaticTagOverride
	err := tx.Where("video_id = ? AND automatic_kind = ?", video.ID, tag.AutomaticKind).First(&override).Error
	if err == nil {
		eligible = override.Present
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if eligible {
		return tx.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING", video.ID, tag.ID).Error
	}
	return tx.Exec("DELETE FROM video_tags WHERE video_id = ? AND tag_id = ?", video.ID, tag.ID).Error
}

func ensureShortVideoAutomaticTag(tx *gorm.DB, create bool) (*models.Tag, error) {
	return ensureAutomaticTag(tx, shortVideoAutomaticTagKind, ShortVideoTagName, tagColorPalette[0], create)
}

func ensureLowResolutionAutomaticTag(tx *gorm.DB, create bool) (*models.Tag, error) {
	return ensureAutomaticTag(tx, lowResolutionAutomaticTagKind, LowResolutionTagName, tagColorPalette[1], create)
}

func ensureAutomaticTag(tx *gorm.DB, kind, name, color string, create bool) (*models.Tag, error) {
	var tag models.Tag
	err := tx.Unscoped().Where("automatic_kind = ?", kind).Order("id").First(&tag).Error
	if err == nil {
		if err := reserveAutomaticTagName(tx, name, tag.ID); err != nil {
			return nil, err
		}
		if tag.Name != name {
			tag.Name = name
			if err := tx.Unscoped().Model(&tag).Update("name", name).Error; err != nil {
				return nil, err
			}
		}
		if tag.DeletedAt.IsValid() && create {
			tag.DeletedAt.Clear()
			tag.IsActive = true
			if err := tx.Unscoped().Save(&tag).Error; err != nil {
				return nil, err
			}
		}
		if tag.DeletedAt.IsValid() {
			return nil, nil
		}
		return &tag, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if !create {
		return nil, nil
	}
	for attempts := 0; attempts < 5; attempts++ {
		if err := reserveAutomaticTagName(tx, name, 0); err != nil {
			return nil, err
		}
		tag = models.Tag{Name: name, Color: color, Namespace: automaticTagNamespace, AutomaticKind: kind, IsActive: true}
		createResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&tag)
		if createResult.Error != nil {
			return nil, createResult.Error
		}
		if createResult.RowsAffected == 1 {
			return &tag, nil
		}
		if err := tx.Where("automatic_kind = ?", kind).First(&tag).Error; err == nil {
			return &tag, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("创建%s自动标签时发生并发冲突", name)
}

func reserveAutomaticTagName(tx *gorm.DB, name string, automaticTagID uint) error {
	var conflict models.Tag
	err := tx.Unscoped().Where("name = ? AND id <> ?", name, automaticTagID).First(&conflict).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	replacement, err := availableTagName(tx, name+"（原标签）", conflict.ID)
	if err != nil {
		return err
	}
	return tx.Unscoped().Model(&conflict).Update("name", replacement).Error
}

func availableTagName(tx *gorm.DB, preferred string, excludeID uint) (string, error) {
	for index := 0; ; index++ {
		candidate := preferred
		if index > 0 {
			candidate = fmt.Sprintf("%s %d", preferred, index+1)
		}
		var count int64
		if err := tx.Unscoped().Model(&models.Tag{}).Where("name = ? AND id <> ?", candidate, excludeID).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
	}
}

func shortVideoMaxDurationSeconds(tx *gorm.DB) (float64, error) {
	var settings models.Settings
	if err := tx.Select("short_feed_max_duration_minutes").First(&settings).Error; err != nil {
		return 0, err
	}
	minutes := settings.ShortFeedMaxDurationMinutes
	if minutes <= 0 {
		minutes = DefaultShortFeedMaxDurationMinutes
	}
	return float64(minutes * 60), nil
}

// 预设标签调色板（视觉和谐的 12 色）
var tagColorPalette = []string{
	"#0D9488", // 品牌青
	"#3b82f6", // 蓝
	"#ef4444", // 红
	"#10b981", // 绿
	"#f59e0b", // 琥珀
	"#8b5cf6", // 紫
	"#ec4899", // 粉
	"#06b6d4", // 青
	"#f97316", // 橙
	"#6366f1", // 靛蓝
	"#14b8a6", // 蓝绿
	"#e11d48", // 玫红
	"#84cc16", // 黄绿
}

// CreateTag 创建标签
func (s *TagService) CreateTag(name, color string) (*models.Tag, error) {
	return s.createTag(name, color, nil)
}

func (s *TagService) CreateTagWithCategory(name, color, category string) (*models.Tag, error) {
	category = strings.TrimSpace(category)
	if category == automaticTagNamespace {
		return nil, fmt.Errorf("自动是系统分类，不能手动分配")
	}
	return s.createTag(name, color, &category)
}

func (s *TagService) createTag(name, color string, category *string) (*models.Tag, error) {
	// 先检查是否存在活跃的同名标签
	var existing models.Tag
	if err := database.DB.Where("name = ?", name).First(&existing).Error; err == nil {
		return &existing, ErrTagExists
	}

	// 颜色为空时自动分配
	if color == "" {
		var count int64
		database.DB.Model(&models.Tag{}).Count(&count)
		color = tagColorPalette[int(count)%len(tagColorPalette)]
	}

	// 检查是否存在被软删除的同名标签，如果有则恢复
	var softDeleted models.Tag
	if err := database.DB.Unscoped().Where("name = ? AND deleted_at IS NOT NULL", name).First(&softDeleted).Error; err == nil {
		// 恢复软删除的标签
		softDeleted.Color = color
		if category != nil {
			softDeleted.Namespace = *category
		}
		softDeleted.IsActive = true
		softDeleted.DeletedAt.Clear()
		if err := database.DB.Unscoped().Save(&softDeleted).Error; err != nil {
			log.Printf("恢复软删除标签失败: name=%s err=%v", name, err)
			return nil, err
		}
		log.Printf("恢复软删除标签: id=%d name=%s", softDeleted.ID, name)
		return &softDeleted, nil
	}

	tag := &models.Tag{
		Name:     name,
		Color:    color,
		IsActive: true,
	}
	if category != nil {
		tag.Namespace = *category
	}
	err := database.DB.Create(tag).Error
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return tag, ErrTagExists
	}
	return tag, err
}

// UpdateTag 更新标签
func (s *TagService) UpdateTag(id uint, name, color string) error {
	return s.updateTag(id, name, color, nil)
}

// UpdateTagWithCategory edits an ordinary tag's display category together with
// its name and color, so the tag manager saves the row as one operation.
func (s *TagService) UpdateTagWithCategory(id uint, name, color, category string) error {
	category = strings.TrimSpace(category)
	return s.updateTag(id, name, color, &category)
}

func (s *TagService) updateTag(id uint, name, color string, category *string) error {
	var current models.Tag
	if err := database.DB.First(&current, id).Error; err != nil {
		return err
	}
	if current.IsSystem {
		return fmt.Errorf("AI 标签请在标签管理中的 AI 标签库维护")
	}
	if current.AutomaticKind != "" {
		return fmt.Errorf("自动标签由应用维护，不能手动修改")
	}
	if category != nil && *category == automaticTagNamespace && current.Namespace != automaticTagNamespace {
		return fmt.Errorf("自动是系统分类，不能手动分配")
	}
	// 检查是否存在同名的活跃标签（排除自身）
	var existing models.Tag
	if err := database.DB.Where("name = ? AND id != ?", name, id).First(&existing).Error; err == nil {
		return ErrTagExists
	}

	// 如果存在被软删除的同名标签，先彻底删除它以避免唯一约束冲突
	database.DB.Unscoped().Where("name = ? AND deleted_at IS NOT NULL", name).Delete(&models.Tag{})

	updates := map[string]interface{}{
		"name":  name,
		"color": color,
	}
	if category != nil {
		updates["namespace"] = *category
	}
	return database.DB.Model(&models.Tag{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteTag 删除标签
func (s *TagService) DeleteTag(id uint) error {
	var tag models.Tag
	if err := database.DB.First(&tag, id).Error; err != nil {
		log.Printf("删除标签失败: 未找到 id=%d err=%v", id, err)
		return err
	}
	if tag.IsSystem {
		return fmt.Errorf("AI 标签请在标签管理中的 AI 标签库维护")
	}
	if tag.AutomaticKind != "" {
		return fmt.Errorf("自动标签由应用维护，不能手动删除")
	}
	// 清理关联关系
	if err := database.DB.Model(&tag).Association("Videos").Clear(); err != nil {
		log.Printf("清理标签关联失败 id=%d err=%v", id, err)
		return err
	}
	// D-002: 删除标签时同步清理图片侧关联，视频侧行为不变。
	if err := database.DB.Exec("DELETE FROM image_tags WHERE tag_id = ?", id).Error; err != nil {
		log.Printf("清理图片标签关联失败 id=%d err=%v", id, err)
		return err
	}
	log.Printf("删除标签 id=%d name=%s", id, tag.Name)
	return database.DB.Delete(&tag).Error
}
