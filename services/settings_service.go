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
	settings.ShortFeedMaxDurationMinutes = positiveOrDefault(input.ShortFeedMaxDurationMinutes, DefaultShortFeedMaxDurationMinutes)
	settings.Theme = input.Theme
	settings.LogEnabled = input.LogEnabled
	settings.BilingualEnabled = input.BilingualEnabled
	settings.BilingualLang = input.BilingualLang
	settings.DeepLApiKey = input.DeepLApiKey
	settings.AITaggingBaseURL = input.AITaggingBaseURL
	settings.AITaggingAPIKey = input.AITaggingAPIKey
	settings.AITaggingModel = input.AITaggingModel
	settings.AITaggingFrameCount = positiveOrDefault(input.AITaggingFrameCount, defaultAITaggingFrameCount)
	settings.AITaggingSubtitleCharLimit = positiveOrDefault(input.AITaggingSubtitleCharLimit, defaultAITaggingSubtitleCharLimit)
	settings.AITaggingStartupBatchSize = positiveOrDefault(input.AITaggingStartupBatchSize, defaultAITaggingStartupBatchSize)

	return database.DB.Save(&settings).Error
}

func positiveOrDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
