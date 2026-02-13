package services

import (
	"video-master/database"
	"video-master/models"
)

type DirectoryService struct{}

// GetAllDirectories 获取所有扫描目录
func (s *DirectoryService) GetAllDirectories() ([]models.ScanDirectory, error) {
	var dirs []models.ScanDirectory
	err := database.DB.Order("created_at desc").Find(&dirs).Error
	return dirs, err
}

// AddDirectory 添加扫描目录
func (s *DirectoryService) AddDirectory(path, alias string) (*models.ScanDirectory, error) {
	dir := &models.ScanDirectory{
		Path:  path,
		Alias: alias,
	}
	err := database.DB.Create(dir).Error
	return dir, err
}

// UpdateDirectory 更新目录别名
func (s *DirectoryService) UpdateDirectory(id uint, path, alias string) error {
	return database.DB.Model(&models.ScanDirectory{}).Where("id = ?", id).Updates(map[string]interface{}{
		"path":  path,
		"alias": alias,
	}).Error
}

// DeleteDirectory 删除扫描目录
func (s *DirectoryService) DeleteDirectory(id uint) error {
	return database.DB.Delete(&models.ScanDirectory{}, id).Error
}
