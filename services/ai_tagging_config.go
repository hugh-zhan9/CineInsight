package services

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	envAITaggingBaseURL = "AI_TAGGING_BASE_URL"
	envAITaggingAPIKey  = "AI_TAGGING_API_KEY"
	envAITaggingModel   = "AI_TAGGING_MODEL"

	envAITaggingFrameCount        = "AI_TAGGING_FRAME_COUNT"
	envAITaggingSubtitleCharLimit = "AI_TAGGING_SUBTITLE_CHAR_LIMIT"
	envAITaggingStartupBatchSize  = "AI_TAGGING_STARTUP_BATCH_SIZE"

	defaultAITaggingFrameCount        = 2
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
	if config.BaseURL == "" || config.APIKey == "" || config.Model == "" {
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
