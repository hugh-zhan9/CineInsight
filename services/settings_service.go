package services

import (
	"video-master/database"
	"video-master/models"
)

type SettingsService struct{}

// GetSettings 获取设置
func (s *SettingsService) GetSettings() (*models.Settings, error) {
	var settings models.Settings
	err := database.DB.First(&settings).Error
	return &settings, err
}

// UpdateSettings 更新设置
func (s *SettingsService) UpdateSettings(confirmDelete, deleteOriginal bool, videoExts string, playWeight float64, autoScan bool, logEnabled bool) error {
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return err
	}

	settings.ConfirmBeforeDelete = confirmDelete
	settings.DeleteOriginalFile = deleteOriginal
	settings.VideoExtensions = videoExts
	settings.PlayWeight = playWeight
	settings.AutoScanOnStartup = autoScan
	settings.LogEnabled = logEnabled

	return database.DB.Save(&settings).Error
}
