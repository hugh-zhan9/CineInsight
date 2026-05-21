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
	envAIBackendMode    = "AI_BACKEND_MODE"
	envLocalMLModel     = "LOCAL_ML_MODEL"
	envLocalMLDevice    = "LOCAL_ML_DEVICE"
	envAITaggingBaseURL = "AI_TAGGING_BASE_URL"
	envAITaggingAPIKey  = "AI_TAGGING_API_KEY"
	envAITaggingModel   = "AI_TAGGING_MODEL"
	envAIEmbeddingModel = "AI_EMBEDDING_MODEL"

	envAITaggingFrameCount        = "AI_TAGGING_FRAME_COUNT"
	envAITaggingSubtitleCharLimit = "AI_TAGGING_SUBTITLE_CHAR_LIMIT"
	envAITaggingStartupBatchSize  = "AI_TAGGING_STARTUP_BATCH_SIZE"

	defaultAITaggingFrameCount        = 5
	defaultAITaggingSubtitleCharLimit = 4000
	defaultAITaggingStartupBatchSize  = 10
)

type AITaggingConfig struct {
	Mode              AIBackendMode
	LocalMLModel      string
	LocalMLDevice     string
	BaseURL           string
	APIKey            string
	Model             string
	EmbeddingModel    string
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
		Mode:              normalizeAIBackendMode(os.Getenv(envAIBackendMode)),
		LocalMLModel:      localMLModelOrDefault(os.Getenv(envLocalMLModel)),
		LocalMLDevice:     normalizeLocalMLDevice(os.Getenv(envLocalMLDevice)),
		BaseURL:           strings.TrimSpace(os.Getenv(envAITaggingBaseURL)),
		APIKey:            strings.TrimSpace(os.Getenv(envAITaggingAPIKey)),
		Model:             strings.TrimSpace(os.Getenv(envAITaggingModel)),
		EmbeddingModel:    strings.TrimSpace(os.Getenv(envAIEmbeddingModel)),
		FrameCount:        envInt(envAITaggingFrameCount, defaultAITaggingFrameCount),
		SubtitleCharLimit: envInt(envAITaggingSubtitleCharLimit, defaultAITaggingSubtitleCharLimit),
		StartupBatchSize:  envInt(envAITaggingStartupBatchSize, defaultAITaggingStartupBatchSize),
	}
	if err := validateAITaggingConfig(config); err != nil {
		return config, err
	}
	return config, nil
}

type SettingsAITaggingConfigProvider struct{}

func (SettingsAITaggingConfigProvider) Load() (AITaggingConfig, error) {
	envConfig := AITaggingConfig{
		Mode:              normalizeAIBackendMode(os.Getenv(envAIBackendMode)),
		LocalMLModel:      localMLModelOrDefault(os.Getenv(envLocalMLModel)),
		LocalMLDevice:     normalizeLocalMLDevice(os.Getenv(envLocalMLDevice)),
		BaseURL:           strings.TrimSpace(os.Getenv(envAITaggingBaseURL)),
		APIKey:            strings.TrimSpace(os.Getenv(envAITaggingAPIKey)),
		Model:             strings.TrimSpace(os.Getenv(envAITaggingModel)),
		EmbeddingModel:    strings.TrimSpace(os.Getenv(envAIEmbeddingModel)),
		FrameCount:        envInt(envAITaggingFrameCount, defaultAITaggingFrameCount),
		SubtitleCharLimit: envInt(envAITaggingSubtitleCharLimit, defaultAITaggingSubtitleCharLimit),
		StartupBatchSize:  envInt(envAITaggingStartupBatchSize, defaultAITaggingStartupBatchSize),
	}

	config := envConfig
	if database.DB != nil {
		var settings models.Settings
		if err := database.DB.First(&settings).Error; err == nil {
			if value := strings.TrimSpace(settings.AIBackendMode); value != "" {
				config.Mode = normalizeAIBackendMode(value)
			}
			if value := strings.TrimSpace(settings.LocalMLModel); value != "" {
				config.LocalMLModel = value
			}
			if value := strings.TrimSpace(settings.LocalMLDevice); value != "" {
				config.LocalMLDevice = normalizeLocalMLDevice(value)
			}
			if value := strings.TrimSpace(settings.AITaggingBaseURL); value != "" {
				config.BaseURL = value
			}
			if value := strings.TrimSpace(settings.AITaggingAPIKey); value != "" {
				config.APIKey = value
			}
			if value := strings.TrimSpace(settings.AITaggingModel); value != "" {
				config.Model = value
			}
			if value := strings.TrimSpace(settings.AIEmbeddingModel); value != "" {
				config.EmbeddingModel = value
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

	config.Mode = normalizeAIBackendMode(string(config.Mode))
	config.LocalMLModel = localMLModelOrDefault(config.LocalMLModel)
	config.LocalMLDevice = normalizeLocalMLDevice(config.LocalMLDevice)
	if err := validateAITaggingConfig(config); err != nil {
		return config, err
	}
	return config, nil
}

func validateAITaggingConfig(config AITaggingConfig) error {
	switch normalizeAIBackendMode(string(config.Mode)) {
	case AIBackendModeLocal, AIBackendModeOff:
		return nil
	default:
		if config.BaseURL == "" || config.Model == "" {
			return fmt.Errorf("AI tagging config unavailable")
		}
		return nil
	}
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
