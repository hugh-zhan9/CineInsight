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
func (s *SettingsService) UpdateSettings(input models.Settings) error {
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return err
	}

	settings.ConfirmBeforeDelete = input.ConfirmBeforeDelete
	settings.DeleteOriginalFile = input.DeleteOriginalFile
	settings.VideoExtensions = input.VideoExtensions
	settings.PlayWeight = input.PlayWeight
	settings.AutoScanOnStartup = input.AutoScanOnStartup
	settings.LogEnabled = input.LogEnabled

	return database.DB.Save(&settings).Error
}
