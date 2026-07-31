package services

import (
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
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
	return database.DB.Transaction(func(tx *gorm.DB) error {
		var settings models.Settings
		if err := tx.First(&settings).Error; err != nil {
			return err
		}

		settings.ConfirmBeforeDelete = input.ConfirmBeforeDelete
		settings.DeleteOriginalFile = input.DeleteOriginalFile
		settings.VideoExtensions = input.VideoExtensions
		settings.PlayWeight = input.PlayWeight
		settings.AutoScanOnStartup = input.AutoScanOnStartup
		settings.LibraryWatchEnabled = input.LibraryWatchEnabled
		settings.LocalMetadataEnabled = input.LocalMetadataEnabled
		settings.AIQualityEnabled = input.AIQualityEnabled
		settings.ShortFeedMaxDurationMinutes = positiveOrDefault(input.ShortFeedMaxDurationMinutes, DefaultShortFeedMaxDurationMinutes)
		settings.Theme = input.Theme
		settings.LogEnabled = input.LogEnabled
		settings.BilingualEnabled = input.BilingualEnabled
		settings.BilingualLang = input.BilingualLang
		settings.DeepLApiKey = input.DeepLApiKey
		settings.SubtitleTranslationProvider = string(normalizeSubtitleTranslationProvider(input.SubtitleTranslationProvider))
		settings.SubtitleTranslationBaseURL = strings.TrimSpace(input.SubtitleTranslationBaseURL)
		settings.SubtitleTranslationAPIKey = strings.TrimSpace(input.SubtitleTranslationAPIKey)
		settings.SubtitleTranslationModel = strings.TrimSpace(input.SubtitleTranslationModel)
		settings.SubtitleWhisperXModel = normalizeSubtitleWhisperXModel(input.SubtitleWhisperXModel)
		settings.SubtitleWhisperXBatchSize = normalizeSubtitleWhisperXBatchSize(input.SubtitleWhisperXBatchSize)
		settings.AITaggingBaseURL = input.AITaggingBaseURL
		settings.AITaggingAPIKey = input.AITaggingAPIKey
		settings.AITaggingModel = input.AITaggingModel
		settings.AITaggingFrameCount = 0
		settings.AITaggingImagesPerRequest = positiveOrDefault(input.AITaggingImagesPerRequest, defaultAITaggingImagesPerRequest)
		settings.AITaggingSubtitleCharLimit = positiveOrDefault(input.AITaggingSubtitleCharLimit, defaultAITaggingSubtitleCharLimit)
		settings.AITaggingStartupBatchSize = positiveOrDefault(input.AITaggingStartupBatchSize, defaultAITaggingStartupBatchSize)
		settings.AITaggingMaxExtraFrames = normalizeAITaggingMaxExtraFrames(input.AITaggingMaxExtraFrames)

		if err := tx.Save(&settings).Error; err != nil {
			return err
		}
		return syncShortVideoTags(tx)
	})
}

func positiveOrDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
