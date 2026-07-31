package services

import (
	"bytes"
	"context"
	"log"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestAIQualityReportAggregatesDecidedSamplesAndLegacyAttribution(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	video := models.Video{Name: "quality.mp4", Path: "/private/quality.mp4"}
	tag := configuredAITag("动作", "#111111")
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	run := models.AITaggingRun{
		VideoID: video.ID, Status: models.AITaggingStateStatusCompleted,
		ModelIdentifier: "model-a", PromptSchemaVersion: "prompt-v2",
		DurationMS: 100, RequestCount: 2, ToolCallCount: 1,
		StartedAt: now.Add(-time.Minute), CompletedAt: timePointer(now),
	}
	if err := database.DB.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	candidates := []models.AITagCandidate{
		{VideoID: video.ID, SuggestedName: tag.Name, NormalizedName: "动作", MatchedTagID: &tag.ID, RunID: &run.ID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusApproved, ApprovedAt: timePointer(now)},
		{VideoID: video.ID, SuggestedName: tag.Name, NormalizedName: "动作", MatchedTagID: &tag.ID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusRejected, RejectedAt: timePointer(now)},
		{VideoID: video.ID, SuggestedName: tag.Name, NormalizedName: "动作", MatchedTagID: &tag.ID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending},
	}
	if err := database.DB.Create(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}

	service := NewAIQualityService()
	service.now = func() time.Time { return now }
	report, err := service.Report(AIQualityFilter{Window: AIQualityWindowThirtyDay})
	if err != nil {
		t.Fatalf("quality report failed: %v", err)
	}
	if report.TagSummary.Decided != 2 || report.TagSummary.Approved != 1 || report.TagSummary.Rejected != 1 || report.TagSummary.ApprovalRate == nil || math.Abs(*report.TagSummary.ApprovalRate-0.5) > 0.0001 {
		t.Fatalf("unexpected tag summary: %+v", report.TagSummary)
	}
	if len(report.TagGroups) != 2 {
		t.Fatalf("run-attributed and legacy samples must remain separate: %+v", report.TagGroups)
	}
	seenUnknown := false
	for _, group := range report.TagGroups {
		if group.ModelIdentifier == AIQualityUnknown && group.PromptSchemaVersion == AIQualityUnknown {
			seenUnknown = true
		}
	}
	if !seenUnknown {
		t.Fatalf("legacy sample must use stable unknown attribution: %+v", report.TagGroups)
	}
	if report.RunSummary.Total != 1 || report.RunSummary.Completed != 1 || report.RunSummary.DurationP50MS == nil || *report.RunSummary.DurationP50MS != 100 {
		t.Fatalf("unexpected run summary: %+v", report.RunSummary)
	}
}

func TestAIQualityReportWindowsFiltersAndNilRates(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	video := models.Video{Name: "filter.mp4", Path: "/tmp/filter.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	oldRun := models.AITaggingRun{VideoID: video.ID, Status: models.AITaggingStateStatusFailed, ModelIdentifier: "old", PromptSchemaVersion: "v1", StartedAt: now.AddDate(0, 0, -40), CompletedAt: timePointer(now.AddDate(0, 0, -40))}
	newRun := models.AITaggingRun{VideoID: video.ID, Status: models.AITaggingStateStatusFailed, ModelIdentifier: "new", PromptSchemaVersion: "v2", StartedAt: now.Add(-time.Hour), CompletedAt: timePointer(now.Add(-time.Hour))}
	if err := database.DB.Create(&[]models.AITaggingRun{oldRun, newRun}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAIQualityService()
	service.now = func() time.Time { return now }
	report, err := service.Report(AIQualityFilter{Window: AIQualityWindowSevenDay, ModelIdentifier: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if report.RunSummary.Total != 1 || report.RunSummary.Failed != 1 {
		t.Fatalf("window/model filter failed: %+v", report.RunSummary)
	}
	if report.TagSummary.ApprovalRate != nil || report.TagSummary.RejectionRate != nil || report.SameSourceSummary.ApprovalRate != nil || report.SameSourceSummary.RejectionRate != nil {
		t.Fatalf("zero denominators must return null rates: %+v", report)
	}
	if _, err := service.Report(AIQualityFilter{Window: "week"}); err == nil {
		t.Fatal("invalid window must fail")
	}
}

func TestAIQualityReportSameSourceUsesOneDistinctFingerprintSample(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	left := models.Video{Name: "left.mp4", Path: "/tmp/left.mp4"}
	right := models.Video{Name: "right.mp4", Path: "/tmp/right.mp4"}
	if err := database.DB.Create(&left).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&right).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAISameSourceService(nil)
	service.now = func() time.Time { return now }
	attribution := aiRunAttribution{RunID: 9, ModelIdentifier: "model-x", ComparisonPromptVersion: "compare-v1"}
	comparison := AISameSourceComparison{SameSource: true, Confidence: models.AITagConfidenceHigh, Reasoning: "same"}
	relation, err := service.persistDetectedRelation(left, right,
		models.VideoVisualFingerprint{ContentFingerprint: "left-1"}, models.VideoVisualFingerprint{ContentFingerprint: "right-1"}, comparison, attribution)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.persistDetectedRelation(left, right,
		models.VideoVisualFingerprint{ContentFingerprint: "left-1"}, models.VideoVisualFingerprint{ContentFingerprint: "right-1"}, comparison, attribution); err != nil {
		t.Fatal(err)
	}
	if count := countRows(t, "ai_same_source_evaluations"); count != 1 {
		t.Fatalf("same fingerprint rerun duplicated sample: %d", count)
	}
	if err := service.RejectRelation(relation.ID); err != nil {
		t.Fatal(err)
	}
	var evaluation models.AISameSourceEvaluation
	if err := database.DB.First(&evaluation, *relation.CurrentEvaluationID).Error; err != nil {
		t.Fatal(err)
	}
	if evaluation.Status != models.VideoSameSourceStatusRejected || evaluation.RejectedAt == nil {
		t.Fatalf("reject must update current sample: %+v", evaluation)
	}

	relation, err = service.persistDetectedRelation(left, right,
		models.VideoVisualFingerprint{ContentFingerprint: "left-2"}, models.VideoVisualFingerprint{ContentFingerprint: "right-1"}, comparison, attribution)
	if err != nil {
		t.Fatal(err)
	}
	if count := countRows(t, "ai_same_source_evaluations"); count != 2 {
		t.Fatalf("changed fingerprint must create a sample: %d", count)
	}
	quality := NewAIQualityService()
	quality.now = func() time.Time { return now }
	report, err := quality.Report(AIQualityFilter{Window: AIQualityWindowAll})
	if err != nil {
		t.Fatal(err)
	}
	if report.SameSourceSummary.Decided != 2 || report.SameSourceSummary.Approved != 1 || report.SameSourceSummary.Rejected != 1 {
		t.Fatalf("unexpected same-source metrics: %+v", report.SameSourceSummary)
	}
}

func TestSameSourceFingerprintRevertReusesExistingSample(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	left := models.Video{Name: "left.mp4", Path: "/tmp/left.mp4"}
	right := models.Video{Name: "right.mp4", Path: "/tmp/right.mp4"}
	if err := database.DB.Create(&left).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&right).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAISameSourceService(nil)
	service.now = func() time.Time { return now }
	attribution := aiRunAttribution{RunID: 3, ModelIdentifier: "model-x", ComparisonPromptVersion: "compare-v1"}
	comparison := AISameSourceComparison{SameSource: true, Confidence: models.AITagConfidenceHigh, Reasoning: "same"}
	fingerprint := func(value string) models.VideoVisualFingerprint {
		return models.VideoVisualFingerprint{ContentFingerprint: value}
	}

	first, err := service.persistDetectedRelation(left, right, fingerprint("left-1"), fingerprint("right-1"), comparison, attribution)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.persistDetectedRelation(left, right, fingerprint("left-2"), fingerprint("right-1"), comparison, attribution); err != nil {
		t.Fatal(err)
	}
	if count := countRows(t, "ai_same_source_evaluations"); count != 2 {
		t.Fatalf("changed fingerprint should add a sample: %d", count)
	}

	// The file is restored, so the fingerprint pair returns to a combination that already
	// has a sample. That must reuse it, not violate the uniqueness index.
	reverted, err := service.persistDetectedRelation(left, right, fingerprint("left-1"), fingerprint("right-1"), comparison, attribution)
	if err != nil {
		t.Fatalf("reverting to a known fingerprint pair must not fail: %v", err)
	}
	if count := countRows(t, "ai_same_source_evaluations"); count != 2 {
		t.Fatalf("revert duplicated a sample: %d", count)
	}
	if reverted.CurrentEvaluationID == nil || *reverted.CurrentEvaluationID != *first.CurrentEvaluationID {
		t.Fatalf("relation should point back at the original sample: %#v", reverted.CurrentEvaluationID)
	}
}

func TestAIQualityRunMetricsUsesFinishedRunsAndLinearPercentiles(t *testing.T) {
	samples := []aiQualityRunSample{
		{Status: models.AITaggingStateStatusCompleted, DurationMS: 100, RequestCount: 1, ToolCallCount: 0, Finished: true},
		{Status: models.AITaggingStateStatusFailed, DurationMS: 200, RequestCount: 3, ToolCallCount: 2, Finished: true},
		{Status: models.AITaggingStateStatusProcessing, DurationMS: 0, RequestCount: 0, ToolCallCount: 0, Finished: false},
	}
	metrics := calculateAIQualityRunMetrics(samples)
	if metrics.Total != 3 || metrics.Completed != 1 || metrics.Failed != 1 || metrics.Processing != 1 {
		t.Fatalf("unexpected counts: %+v", metrics)
	}
	if metrics.FailureRate == nil || *metrics.FailureRate != 0.5 {
		t.Fatalf("processing runs must not enter failure denominator: %+v", metrics)
	}
	if metrics.DurationP50MS == nil || *metrics.DurationP50MS != 150 || metrics.DurationP95MS == nil || *metrics.DurationP95MS != 195 {
		t.Fatalf("unexpected percentiles: %+v", metrics)
	}
	if metrics.AverageRequests == nil || *metrics.AverageRequests != 2 || metrics.AverageToolCalls == nil || *metrics.AverageToolCalls != 1 {
		t.Fatalf("unexpected averages: %+v", metrics)
	}
}

func TestAIQualityExcludesInterruptedRunsFromDurationAndRequestStats(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	video := models.Video{Name: "runs.mp4", Path: "/library/runs.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	finishedAt := now.Add(-time.Hour)
	completed := models.AITaggingRun{
		VideoID: video.ID, Status: models.AITaggingStateStatusCompleted, ModelIdentifier: "m",
		DurationMS: 4000, RequestCount: 4, StartedAt: finishedAt.Add(-4 * time.Second), CompletedAt: &finishedAt,
	}
	if err := database.DB.Create(&completed).Error; err != nil {
		t.Fatal(err)
	}
	// A run the process never finished; recovery stamps completed_at but no real timings.
	interrupted := models.AITaggingRun{
		VideoID: video.ID, Status: models.AITaggingStateStatusFailed, FailureCode: aiRunFailureInterrupted,
		ModelIdentifier: "m", DurationMS: 0, RequestCount: 0, StartedAt: finishedAt, CompletedAt: &finishedAt,
	}
	if err := database.DB.Create(&interrupted).Error; err != nil {
		t.Fatal(err)
	}

	service := NewAIQualityService()
	service.now = func() time.Time { return now }
	report, err := service.Report(AIQualityFilter{Window: AIQualityWindowAll})
	if err != nil {
		t.Fatal(err)
	}
	if report.RunSummary.Total != 2 || report.RunSummary.Failed != 1 {
		t.Fatalf("interrupted runs still count as runs: %+v", report.RunSummary)
	}
	if report.RunSummary.DurationP50MS == nil || *report.RunSummary.DurationP50MS != 4000 {
		t.Fatalf("interrupted run polluted duration percentiles: %+v", report.RunSummary)
	}
	if report.RunSummary.AverageRequests == nil || *report.RunSummary.AverageRequests != 4 {
		t.Fatalf("interrupted run diluted request average: %+v", report.RunSummary)
	}
}

func TestAIQualityLegacySameSourceWindowUsesDetectionTimeNotLastTouch(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(-2, 0, 0)
	relation := models.VideoSameSourceRelation{
		VideoAID: 1, VideoBID: 2, VideoAFingerprint: "fa", VideoBFingerprint: "fb",
		Status: models.VideoSameSourceStatusDetected, Confidence: models.AITagConfidenceHigh,
		DetectionVersion: "legacy-v1",
	}
	if err := database.DB.Create(&relation).Error; err != nil {
		t.Fatal(err)
	}
	// Detected two years ago, but merely marked read today.
	if err := database.DB.Model(&models.VideoSameSourceRelation{}).Where("id = ?", relation.ID).
		UpdateColumns(map[string]any{"created_at": old, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}

	service := NewAIQualityService()
	service.now = func() time.Time { return now }
	report, err := service.Report(AIQualityFilter{Window: AIQualityWindowSevenDay})
	if err != nil {
		t.Fatal(err)
	}
	if report.SameSourceSummary.Decided != 0 {
		t.Fatalf("reading an old relation must not make it a recent sample: %+v", report.SameSourceSummary)
	}
	allTime, err := service.Report(AIQualityFilter{Window: AIQualityWindowAll})
	if err != nil {
		t.Fatal(err)
	}
	if allTime.SameSourceSummary.Decided != 1 {
		t.Fatalf("legacy sample must still be counted overall: %+v", allTime.SameSourceSummary)
	}
}

func TestAITaggingRunRecoveryAndAttributionSanitization(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	video := models.Video{Name: "interrupted.mp4", Path: "/secret/interrupted.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	run := models.AITaggingRun{VideoID: video.ID, Status: models.AITaggingStateStatusProcessing, ModelIdentifier: "model", StartedAt: now.Add(-time.Minute)}
	state := models.AITaggingState{VideoID: video.ID, Status: models.AITaggingStateStatusProcessing}
	if err := database.DB.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAITaggingService()
	service.now = func() time.Time { return now }
	if err := service.recoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&run, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&state, state.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != models.AITaggingStateStatusFailed || run.FailureCode != "interrupted" || run.CompletedAt == nil {
		t.Fatalf("run was not recovered: %+v", run)
	}
	if state.Status != models.AITaggingStateStatusSkipped || state.SkipReason != "interrupted" {
		t.Fatalf("state must not auto-rerun: %+v", state)
	}
	if got := sanitizeAIAttribution("  model\nname\t"); got != "modelname" {
		t.Fatalf("control characters must be removed: %q", got)
	}
}

func TestAIQualityAttributionModelsAndLogsExcludeSensitiveEvidence(t *testing.T) {
	for _, model := range []any{models.AITaggingRun{}, models.AISameSourceEvaluation{}} {
		typeValue := reflect.TypeOf(model)
		for index := 0; index < typeValue.NumField(); index++ {
			field := typeValue.Field(index)
			value := strings.ToLower(field.Name + " " + field.Tag.Get("json"))
			for _, forbidden := range []string{"api_key", "apikey", "baseurl", "base_url", "path", "subtitle", "frame", "payload", "request_body", "response_body"} {
				if strings.Contains(value, forbidden) {
					t.Fatalf("%s contains forbidden attribution field %q", typeValue.Name(), field.Name)
				}
			}
		}
	}

	setupVideoServiceTestDB(t)
	tag := configuredAITag("动作", "#fff")
	video := models.Video{Name: "private-name.mp4", Path: "/secret/library/private-name.mp4", Directory: "/secret/library"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{Label: tag.Name, Confidence: models.AITagConfidenceHigh, MatchedExistingName: tag.Name}}}
	service := newTestAITaggingService(client, fakeAITaggingConfigProvider{config: AITaggingConfig{
		BaseURL: "https://secret.example/v1", APIKey: "top-secret-key", Model: "model-safe", ImagesPerRequest: 10, SubtitleCharLimit: 1000,
	}})
	var output bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previousWriter) })
	if err := service.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatal(err)
	}
	logged := output.String()
	for _, forbidden := range []string{"secret.example", "top-secret-key", "/secret/library", "private-name.mp4"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("AI quality-related log leaked %q: %s", forbidden, logged)
		}
	}
}
