package services

import (
	"errors"
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

// CreateTag 创建标签
func (s *TagService) CreateTag(name, color string) (*models.Tag, error) {
	var existing models.Tag
	if err := database.DB.Where("name = ?", name).First(&existing).Error; err == nil {
		return &existing, errors.New("TAG_EXISTS")
	}

	tag := &models.Tag{
		Name:  name,
		Color: color,
	}
	err := database.DB.Create(tag).Error
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return tag, errors.New("TAG_EXISTS")
	}
	return tag, err
}

// UpdateTag 更新标签
func (s *TagService) UpdateTag(id uint, name, color string) error {
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
