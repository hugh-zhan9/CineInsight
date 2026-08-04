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

const (
	ShortVideoTagName          = "短视频"
	shortVideoAutomaticTagKind = "short_video"
)

type MergeTagsResult struct {
	TargetTagID     uint `json:"target_tag_id"`
	MergedTagCount  int  `json:"merged_tag_count"`
	VideoLinksMoved int  `json:"video_links_moved"`
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
	normalizedNames := make(map[string]struct{}, len(inputs))
	for i := range inputs {
		inputs[i].Name = strings.TrimSpace(inputs[i].Name)
		inputs[i].Namespace = strings.TrimSpace(inputs[i].Namespace)
		inputs[i].Color = strings.TrimSpace(inputs[i].Color)
		if inputs[i].Name == "" || inputs[i].Namespace == "" {
			return nil, fmt.Errorf("标签名称和分类不能为空")
		}
		normalized := normalizeAITagName(inputs[i].Name)
		if _, exists := normalizedNames[normalized]; exists {
			return nil, fmt.Errorf("标签名称重复: %s", inputs[i].Name)
		}
		normalizedNames[normalized] = struct{}{}
	}

	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var existingSystemTags []models.Tag
		if err := tx.Where("is_system = ?", true).Find(&existingSystemTags).Error; err != nil {
			return err
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
				"namespace":       "",
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
	})
	if err != nil {
		return nil, err
	}
	return s.GetAITagLibrary()
}

// GetAllTags 获取所有标签
func (s *TagService) GetAllTags() ([]models.Tag, error) {
	var tags []models.Tag
	err := database.DB.Order("name").Find(&tags).Error
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
	err := database.DB.Transaction(func(tx *gorm.DB) error {
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
	err := database.DB.Transaction(func(tx *gorm.DB) error {
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

	var eligibleCount int64
	if err := tx.Model(&models.Video{}).
		Where("is_stale = ? AND duration > ? AND duration < ?", false, 0, maxDurationSeconds).
		Count(&eligibleCount).Error; err != nil {
		return err
	}

	tag, err := ensureShortVideoAutomaticTag(tx, eligibleCount > 0)
	if err != nil {
		return err
	}
	if tag == nil {
		return nil
	}
	result.TagID = tag.ID

	added := tx.Exec(`
		INSERT INTO video_tags(video_id, tag_id)
		SELECT id, ? FROM videos
		WHERE deleted_at IS NULL AND is_stale = ? AND duration > ? AND duration < ?
		ON CONFLICT DO NOTHING
	`, tag.ID, false, 0, maxDurationSeconds)
	if added.Error != nil {
		return added.Error
	}
	result.Added = added.RowsAffected

	removed := tx.Exec(`
		DELETE FROM video_tags
		WHERE tag_id = ? AND video_id IN (
			SELECT id FROM videos
			WHERE deleted_at IS NULL AND (is_stale = ? OR duration <= ? OR duration >= ?)
		)
	`, tag.ID, true, 0, maxDurationSeconds)
	if removed.Error != nil {
		return removed.Error
	}
	result.Removed = removed.RowsAffected
	return nil
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
	eligible := !video.IsStale && video.Duration > 0 && video.Duration < maxDurationSeconds

	tag, err := ensureShortVideoAutomaticTag(tx, eligible)
	if err != nil {
		return err
	}
	if tag == nil {
		return nil
	}
	if eligible {
		return tx.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING", video.ID, tag.ID).Error
	}
	return tx.Exec("DELETE FROM video_tags WHERE video_id = ? AND tag_id = ?", video.ID, tag.ID).Error
}

func ensureShortVideoAutomaticTag(tx *gorm.DB, create bool) (*models.Tag, error) {
	var tag models.Tag
	err := tx.Unscoped().Where("automatic_kind = ?", shortVideoAutomaticTagKind).Order("id").First(&tag).Error
	if err == nil {
		if err := reserveShortVideoTagName(tx, tag.ID); err != nil {
			return nil, err
		}
		if tag.Name != ShortVideoTagName {
			tag.Name = ShortVideoTagName
			if err := tx.Unscoped().Model(&tag).Update("name", tag.Name).Error; err != nil {
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
		if err := reserveShortVideoTagName(tx, 0); err != nil {
			return nil, err
		}
		tag = models.Tag{
			Name:          ShortVideoTagName,
			Color:         tagColorPalette[0],
			Namespace:     "自动",
			AutomaticKind: shortVideoAutomaticTagKind,
			IsActive:      true,
		}
		createResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&tag)
		if createResult.Error != nil {
			return nil, createResult.Error
		}
		if createResult.RowsAffected == 1 {
			return &tag, nil
		}
		if err := tx.Where("automatic_kind = ?", shortVideoAutomaticTagKind).First(&tag).Error; err == nil {
			return &tag, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("创建短视频自动标签时发生并发冲突")
}

// reserveShortVideoTagName keeps the automatic label's public name stable.
// A pre-existing manual label is renamed without changing its ID or video
// relationships, so the automatic rule never takes ownership of those links.
func reserveShortVideoTagName(tx *gorm.DB, automaticTagID uint) error {
	var conflict models.Tag
	err := tx.Unscoped().Where("name = ? AND id <> ?", ShortVideoTagName, automaticTagID).First(&conflict).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	name, err := availableTagName(tx, ShortVideoTagName+"（原标签）", conflict.ID)
	if err != nil {
		return err
	}
	return tx.Unscoped().Model(&conflict).Update("name", name).Error
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
	err := database.DB.Create(tag).Error
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return tag, ErrTagExists
	}
	return tag, err
}

// UpdateTag 更新标签
func (s *TagService) UpdateTag(id uint, name, color string) error {
	var current models.Tag
	if err := database.DB.First(&current, id).Error; err != nil {
		return err
	}
	if current.IsSystem {
		return fmt.Errorf("系统标签请在设置中的 AI 标签库维护")
	}
	if current.AutomaticKind != "" {
		return fmt.Errorf("自动标签由应用维护，不能手动修改")
	}
	// 检查是否存在同名的活跃标签（排除自身）
	var existing models.Tag
	if err := database.DB.Where("name = ? AND id != ?", name, id).First(&existing).Error; err == nil {
		return ErrTagExists
	}

	// 如果存在被软删除的同名标签，先彻底删除它以避免唯一约束冲突
	database.DB.Unscoped().Where("name = ? AND deleted_at IS NOT NULL", name).Delete(&models.Tag{})

	return database.DB.Model(&models.Tag{}).Where("id = ?", id).Updates(map[string]interface{}{
		"name":  name,
		"color": color,
	}).Error
}

// DeleteTag 删除标签
func (s *TagService) DeleteTag(id uint) error {
	var tag models.Tag
	if err := database.DB.First(&tag, id).Error; err != nil {
		log.Printf("删除标签失败: 未找到 id=%d err=%v", id, err)
		return err
	}
	if tag.IsSystem {
		return fmt.Errorf("系统标签请在设置中的 AI 标签库维护")
	}
	if tag.AutomaticKind != "" {
		return fmt.Errorf("自动标签由应用维护，不能手动删除")
	}
	// 清理关联关系
	if err := database.DB.Model(&tag).Association("Videos").Clear(); err != nil {
		log.Printf("清理标签关联失败 id=%d err=%v", id, err)
		return err
	}
	log.Printf("删除标签 id=%d name=%s", id, tag.Name)
	return database.DB.Delete(&tag).Error
}
