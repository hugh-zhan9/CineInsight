package services

import (
	"strings"
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
	settings.SubtitleTranslationProvider = string(normalizeSubtitleTranslationProvider(input.SubtitleTranslationProvider))
	settings.SubtitleTranslationBaseURL = input.SubtitleTranslationBaseURL
	settings.SubtitleTranslationAPIKey = input.SubtitleTranslationAPIKey
	settings.SubtitleTranslationModel = input.SubtitleTranslationModel
	settings.SubtitleWhisperXModel = normalizeSubtitleWhisperXModel(input.SubtitleWhisperXModel)
	settings.SubtitleWhisperXBatchSize = normalizeSubtitleWhisperXBatchSize(input.SubtitleWhisperXBatchSize)
	settings.SubtitleWhisperXComputeType = normalizeSubtitleWhisperXComputeType(input.SubtitleWhisperXComputeType)
	settings.AIBackendMode = string(normalizeAIBackendMode(input.AIBackendMode))
	settings.LocalMLModel = localMLModelOrDefault(input.LocalMLModel)
	settings.LocalMLDevice = normalizeLocalMLDevice(input.LocalMLDevice)
	settings.AITaggingBaseURL = input.AITaggingBaseURL
	settings.AITaggingAPIKey = input.AITaggingAPIKey
	settings.AITaggingModel = input.AITaggingModel
	settings.AIEmbeddingModel = input.AIEmbeddingModel
	settings.AITaggingFrameCount = positiveOrDefault(input.AITaggingFrameCount, defaultAITaggingFrameCount)
	settings.AITaggingSubtitleCharLimit = positiveOrDefault(input.AITaggingSubtitleCharLimit, defaultAITaggingSubtitleCharLimit)
	settings.AITaggingStartupBatchSize = positiveOrDefault(input.AITaggingStartupBatchSize, defaultAITaggingStartupBatchSize)

	return database.DB.Save(&settings).Error
}

func localMLModelOrDefault(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "", legacyBuiltinLocalModel, legacyOpenAILocalModel:
		return defaultLocalMLModel
	default:
		return value
	}
}

func positiveOrDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func normalizeSubtitleWhisperXModel(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "tiny", "base", "small", "medium", "large-v2", "large-v3":
		return strings.TrimSpace(strings.ToLower(value))
	default:
		return defaultSubtitleWhisperXModel
	}
}

func normalizeSubtitleWhisperXComputeType(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "int8", "float16", "float32":
		return strings.TrimSpace(strings.ToLower(value))
	default:
		return defaultSubtitleWhisperXComputeType
	}
}

func normalizeSubtitleWhisperXBatchSize(value int) int {
	if value <= 0 {
		return defaultSubtitleWhisperXBatchSize
	}
	if value > maxSubtitleWhisperXBatchSize {
		return maxSubtitleWhisperXBatchSize
	}
	return value
}
