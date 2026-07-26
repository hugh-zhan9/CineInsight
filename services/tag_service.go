package services

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

type TagService struct{}

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
				if err := tx.First(&tag, input.ID).Error; err != nil {
					return err
				}
				if !tag.IsSystem {
					return fmt.Errorf("标签 %d 不属于 AI 标签库", input.ID)
				}
				var conflict models.Tag
				err := tx.Unscoped().Where("name = ? AND id <> ?", input.Name, tag.ID).First(&conflict).Error
				if err == nil {
					return fmt.Errorf("标签名称已存在: %s", input.Name)
				}
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
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

			sortOrder := namespaceOrders[input.Namespace]
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
			Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.AITaggingState{}).
			Where("NOT EXISTS (SELECT 1 FROM video_tags WHERE video_tags.video_id = ai_tagging_states.video_id)").
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
	// 清理关联关系
	if err := database.DB.Model(&tag).Association("Videos").Clear(); err != nil {
		log.Printf("清理标签关联失败 id=%d err=%v", id, err)
		return err
	}
	log.Printf("删除标签 id=%d name=%s", id, tag.Name)
	return database.DB.Delete(&tag).Error
}
