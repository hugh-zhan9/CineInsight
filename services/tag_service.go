package services

import (
	"log"
	"strings"
	"video-master/database"
	"video-master/models"
)

type TagService struct{}

// GetAllTags 获取所有标签
func (s *TagService) GetAllTags() ([]models.Tag, error) {
	var tags []models.Tag
	err := database.DB.Order("name").Find(&tags).Error
	return tags, err
}

// 预设标签调色板（视觉和谐的 12 色）
var tagColorPalette = []string{
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
		softDeleted.DeletedAt.Valid = false
		if err := database.DB.Unscoped().Save(&softDeleted).Error; err != nil {
			log.Printf("恢复软删除标签失败: name=%s err=%v", name, err)
			return nil, err
		}
		log.Printf("恢复软删除标签: id=%d name=%s", softDeleted.ID, name)
		return &softDeleted, nil
	}

	tag := &models.Tag{
		Name:  name,
		Color: color,
	}
	err := database.DB.Create(tag).Error
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return tag, ErrTagExists
	}
	return tag, err
}

// UpdateTag 更新标签
func (s *TagService) UpdateTag(id uint, name, color string) error {
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
	// 清理关联关系
	if err := database.DB.Model(&tag).Association("Videos").Clear(); err != nil {
		log.Printf("清理标签关联失败 id=%d err=%v", id, err)
		return err
	}
	log.Printf("删除标签 id=%d name=%s", id, tag.Name)
	return database.DB.Delete(&tag).Error
}
