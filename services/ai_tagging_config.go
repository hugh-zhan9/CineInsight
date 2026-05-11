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

	envAITaggingFrameCount        = "AI_TAGGING_FRAME_COUNT"
	envAITaggingSubtitleCharLimit = "AI_TAGGING_SUBTITLE_CHAR_LIMIT"
	envAITaggingStartupBatchSize  = "AI_TAGGING_STARTUP_BATCH_SIZE"

	defaultAITaggingFrameCount        = 5
	defaultAITaggingSubtitleCharLimit = 4000
	defaultAITaggingStartupBatchSize  = 10
)

type AITaggingConfig struct {
	BaseURL           string
	APIKey            string
	Model             string
	FrameCount        int
	SubtitleCharLimit int
	StartupBatchSize  int
}

type AITaggingConfigProvider interface {
	Load() (AITaggingConfig, error)
}

type EnvAITaggingConfigProvider struct{}

func (EnvAITaggingConfigProvider) Load() (AITaggingConfig, error) {
	config := AITaggingConfig{
		BaseURL:           strings.TrimSpace(os.Getenv(envAITaggingBaseURL)),
		APIKey:            strings.TrimSpace(os.Getenv(envAITaggingAPIKey)),
		Model:             strings.TrimSpace(os.Getenv(envAITaggingModel)),
		FrameCount:        envInt(envAITaggingFrameCount, defaultAITaggingFrameCount),
		SubtitleCharLimit: envInt(envAITaggingSubtitleCharLimit, defaultAITaggingSubtitleCharLimit),
		StartupBatchSize:  envInt(envAITaggingStartupBatchSize, defaultAITaggingStartupBatchSize),
	}
	if config.BaseURL == "" || config.Model == "" {
		return config, fmt.Errorf("AI tagging config unavailable")
	}
	return config, nil
}

type SettingsAITaggingConfigProvider struct{}

func (SettingsAITaggingConfigProvider) Load() (AITaggingConfig, error) {
	envConfig := AITaggingConfig{
		BaseURL:           strings.TrimSpace(os.Getenv(envAITaggingBaseURL)),
		APIKey:            strings.TrimSpace(os.Getenv(envAITaggingAPIKey)),
		Model:             strings.TrimSpace(os.Getenv(envAITaggingModel)),
		FrameCount:        envInt(envAITaggingFrameCount, defaultAITaggingFrameCount),
		SubtitleCharLimit: envInt(envAITaggingSubtitleCharLimit, defaultAITaggingSubtitleCharLimit),
		StartupBatchSize:  envInt(envAITaggingStartupBatchSize, defaultAITaggingStartupBatchSize),
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
			if settings.AITaggingFrameCount > 0 {
				config.FrameCount = settings.AITaggingFrameCount
			}
			if settings.AITaggingSubtitleCharLimit > 0 {
				config.SubtitleCharLimit = settings.AITaggingSubtitleCharLimit
			}
			if settings.AITaggingStartupBatchSize > 0 {
				config.StartupBatchSize = settings.AITaggingStartupBatchSize
			}
		}
	}

	if config.BaseURL == "" || config.Model == "" {
		return config, fmt.Errorf("AI tagging config unavailable")
	}
	return config, nil
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
