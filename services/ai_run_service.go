package services

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

const sameSourceComparisonPromptVersion = "same-source-comparison-v1"

// aiRunFailureInterrupted marks runs the process never finished. They carry no real
// timings, so quality aggregation must keep them out of duration and request statistics.
const aiRunFailureInterrupted = "interrupted"

type aiRunAttribution struct {
	RunID                   uint
	ModelIdentifier         string
	ComparisonPromptVersion string
}

type aiRunContextKey struct{}

func withAIRunAttribution(ctx context.Context, run models.AITaggingRun) context.Context {
	return context.WithValue(ctx, aiRunContextKey{}, aiRunAttribution{
		RunID: run.ID, ModelIdentifier: run.ModelIdentifier, ComparisonPromptVersion: sameSourceComparisonPromptVersion,
	})
}

func aiRunAttributionFromContext(ctx context.Context) aiRunAttribution {
	if ctx == nil {
		return aiRunAttribution{}
	}
	value, _ := ctx.Value(aiRunContextKey{}).(aiRunAttribution)
	return value
}

func sanitizeAIAttribution(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			continue
		}
		if builder.Len()+utf8.RuneLen(r) > 255 {
			break
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func (s *AITaggingService) createRun(videoID uint, config AITaggingConfig) (models.AITaggingRun, error) {
	now := s.now()
	run := models.AITaggingRun{
		VideoID: videoID, Status: models.AITaggingStateStatusProcessing,
		ModelIdentifier: sanitizeAIAttribution(config.Model), PromptSchemaVersion: sanitizeAIAttribution(aiTaggingPromptSchemaVersion),
		StartedAt: now,
	}
	return run, database.DB.Create(&run).Error
}

func (s *AITaggingService) finishRun(run models.AITaggingRun, status, failureCode string, client AITaggingAIClient) error {
	completedAt := s.now()
	duration := completedAt.Sub(run.StartedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	requestCount := 0
	if reporter, ok := client.(AITaggingUsageReporter); ok {
		requestCount = reporter.AITaggingUsage().RequestCount
	}
	var toolCallCount int64
	if err := database.DB.Model(&models.AITagAgentStep{}).
		Where("run_id = ? AND action <> ? AND tool_status IN ?", run.ID, models.AITagAgentActionFinalize,
			[]string{models.AITagToolStatusSuccess, models.AITagToolStatusPartial, models.AITagToolStatusFailed}).
		Count(&toolCallCount).Error; err != nil {
		return err
	}
	return database.DB.Model(&models.AITaggingRun{}).Where("id = ? AND status = ?", run.ID, models.AITaggingStateStatusProcessing).
		Updates(map[string]any{
			"status": status, "failure_code": sanitizeAIAttribution(failureCode), "duration_ms": duration,
			"request_count": requestCount, "tool_call_count": int(toolCallCount), "completed_at": &completedAt,
		}).Error
}

func (s *AITaggingService) recoverInterruptedRuns() error {
	now := s.now()
	return database.Transaction(func(tx *gorm.DB) error {
		var videoIDs []uint
		if err := tx.Model(&models.AITaggingRun{}).Where("status = ?", models.AITaggingStateStatusProcessing).Pluck("video_id", &videoIDs).Error; err != nil {
			return err
		}
		if len(videoIDs) == 0 {
			return nil
		}
		if err := tx.Model(&models.AITaggingRun{}).Where("status = ?", models.AITaggingStateStatusProcessing).
			Updates(map[string]any{
				"status": models.AITaggingStateStatusFailed, "failure_code": aiRunFailureInterrupted, "completed_at": &now,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&models.AITaggingState{}).
			Where("video_id IN ? AND status = ?", videoIDs, models.AITaggingStateStatusProcessing).
			Updates(map[string]any{
				"status": models.AITaggingStateStatusSkipped, "skip_reason": aiRunFailureInterrupted, "last_error": "", "last_processed_at": &now,
			}).Error
	})
}
