package services

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"video-master/database"
	"video-master/models"
)

const (
	envAITaggingBaseURL = "AI_TAGGING_BASE_URL"
	envAITaggingAPIKey  = "AI_TAGGING_API_KEY"
	envAITaggingModel   = "AI_TAGGING_MODEL"

	envAITaggingImagesPerRequest  = "AI_TAGGING_IMAGES_PER_REQUEST"
	envAITaggingSubtitleCharLimit = "AI_TAGGING_SUBTITLE_CHAR_LIMIT"
	envAITaggingStartupBatchSize  = "AI_TAGGING_STARTUP_BATCH_SIZE"
	envAITaggingMaxExtraFrames    = "AI_TAGGING_MAX_EXTRA_FRAMES"

	defaultAITaggingImagesPerRequest  = 10
	defaultAITaggingSubtitleCharLimit = 4000
	defaultAITaggingStartupBatchSize  = 10
	defaultAITaggingMaxExtraFrames    = 20
	maxAITaggingExtraFrames           = 100
)

type AITaggingConfig struct {
	BaseURL                   string
	APIKey                    string
	Model                     string
	ImagesPerRequest          int
	SubtitleCharLimit         int
	StartupBatchSize          int
	MaxExtraFrames            int
	SubtitleWhisperXModel     string
	SubtitleWhisperXBatchSize int
}

type AITaggingConfigProvider interface {
	Load() (AITaggingConfig, error)
}

type EnvAITaggingConfigProvider struct{}

func (EnvAITaggingConfigProvider) Load() (AITaggingConfig, error) {
	config := AITaggingConfig{
		BaseURL:                   strings.TrimSpace(os.Getenv(envAITaggingBaseURL)),
		APIKey:                    strings.TrimSpace(os.Getenv(envAITaggingAPIKey)),
		Model:                     strings.TrimSpace(os.Getenv(envAITaggingModel)),
		ImagesPerRequest:          envInt(envAITaggingImagesPerRequest, defaultAITaggingImagesPerRequest),
		SubtitleCharLimit:         envInt(envAITaggingSubtitleCharLimit, defaultAITaggingSubtitleCharLimit),
		StartupBatchSize:          envInt(envAITaggingStartupBatchSize, defaultAITaggingStartupBatchSize),
		MaxExtraFrames:            normalizeAITaggingMaxExtraFrames(envInt(envAITaggingMaxExtraFrames, defaultAITaggingMaxExtraFrames)),
		SubtitleWhisperXModel:     defaultSubtitleWhisperXModel,
		SubtitleWhisperXBatchSize: defaultSubtitleWhisperXBatchSize,
	}
	if config.BaseURL == "" || config.Model == "" {
		return config, fmt.Errorf("AI tagging config unavailable")
	}
	return config, nil
}

type SettingsAITaggingConfigProvider struct{}

func (SettingsAITaggingConfigProvider) Load() (AITaggingConfig, error) {
	envConfig := AITaggingConfig{
		BaseURL:                   strings.TrimSpace(os.Getenv(envAITaggingBaseURL)),
		APIKey:                    strings.TrimSpace(os.Getenv(envAITaggingAPIKey)),
		Model:                     strings.TrimSpace(os.Getenv(envAITaggingModel)),
		ImagesPerRequest:          envInt(envAITaggingImagesPerRequest, defaultAITaggingImagesPerRequest),
		SubtitleCharLimit:         envInt(envAITaggingSubtitleCharLimit, defaultAITaggingSubtitleCharLimit),
		StartupBatchSize:          envInt(envAITaggingStartupBatchSize, defaultAITaggingStartupBatchSize),
		MaxExtraFrames:            normalizeAITaggingMaxExtraFrames(envInt(envAITaggingMaxExtraFrames, defaultAITaggingMaxExtraFrames)),
		SubtitleWhisperXModel:     defaultSubtitleWhisperXModel,
		SubtitleWhisperXBatchSize: defaultSubtitleWhisperXBatchSize,
	}

	config := envConfig
	if database.DB != nil {
		var settings models.Settings
		if err := database.DB.First(&settings).Error; err == nil {
			if value := strings.TrimSpace(settings.AITaggingBaseURL); value != "" {
				config.BaseURL = value
			}
			if value := strings.TrimSpace(settings.AITaggingAPIKey); value != "" {
				config.APIKey = value
			}
			if value := strings.TrimSpace(settings.AITaggingModel); value != "" {
				config.Model = value
			}
			if settings.AITaggingImagesPerRequest > 0 {
				config.ImagesPerRequest = settings.AITaggingImagesPerRequest
			}
			if settings.AITaggingSubtitleCharLimit > 0 {
				config.SubtitleCharLimit = settings.AITaggingSubtitleCharLimit
			}
			if settings.AITaggingStartupBatchSize > 0 {
				config.StartupBatchSize = settings.AITaggingStartupBatchSize
			}
			config.MaxExtraFrames = normalizeAITaggingMaxExtraFrames(settings.AITaggingMaxExtraFrames)
			config.SubtitleWhisperXModel = normalizeSubtitleWhisperXModel(settings.SubtitleWhisperXModel)
			config.SubtitleWhisperXBatchSize = normalizeSubtitleWhisperXBatchSize(settings.SubtitleWhisperXBatchSize)
		}
	}

	if config.BaseURL == "" || config.Model == "" {
		return config, fmt.Errorf("AI tagging config unavailable")
	}
	return config, nil
}

func normalizeAITaggingMaxExtraFrames(value int) int {
	if value <= 0 {
		return defaultAITaggingMaxExtraFrames
	}
	if value > maxAITaggingExtraFrames {
		return maxAITaggingExtraFrames
	}
	return value
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
