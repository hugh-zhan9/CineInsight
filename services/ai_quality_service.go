package services

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
)

const (
	AIQualityWindowAll       = "all"
	AIQualityWindowThirtyDay = "30d"
	AIQualityWindowSevenDay  = "7d"
	AIQualityUnknown         = "historical_unknown"
)

// AIQualityFilter limits quality samples without triggering any AI or media work.
type AIQualityFilter struct {
	Window                  string `json:"window"`
	TagID                   uint   `json:"tag_id"`
	Confidence              string `json:"confidence"`
	ModelIdentifier         string `json:"model_identifier"`
	PromptSchemaVersion     string `json:"prompt_schema_version"`
	ComparisonPromptVersion string `json:"comparison_prompt_version"`
	DetectionVersion        string `json:"detection_version"`
}

type AIQualityDecisionMetrics struct {
	Decided       int64    `json:"decided"`
	Approved      int64    `json:"approved"`
	Rejected      int64    `json:"rejected"`
	ApprovalRate  *float64 `json:"approval_rate"`
	RejectionRate *float64 `json:"rejection_rate"`
}

type AIQualityTagGroup struct {
	TagID               uint   `json:"tag_id"`
	TagName             string `json:"tag_name"`
	Confidence          string `json:"confidence"`
	ModelIdentifier     string `json:"model_identifier"`
	PromptSchemaVersion string `json:"prompt_schema_version"`
	AIQualityDecisionMetrics
}

type AIQualitySameSourceGroup struct {
	Confidence              string `json:"confidence"`
	ModelIdentifier         string `json:"model_identifier"`
	ComparisonPromptVersion string `json:"comparison_prompt_version"`
	DetectionVersion        string `json:"detection_version"`
	AIQualityDecisionMetrics
}

type AIQualityRunMetrics struct {
	Total            int64    `json:"total"`
	Completed        int64    `json:"completed"`
	Skipped          int64    `json:"skipped"`
	Failed           int64    `json:"failed"`
	Processing       int64    `json:"processing"`
	FailureRate      *float64 `json:"failure_rate"`
	DurationP50MS    *float64 `json:"duration_p50_ms"`
	DurationP95MS    *float64 `json:"duration_p95_ms"`
	AverageRequests  *float64 `json:"average_requests"`
	AverageToolCalls *float64 `json:"average_tool_calls"`
}

type AIQualityRunGroup struct {
	ModelIdentifier     string `json:"model_identifier"`
	PromptSchemaVersion string `json:"prompt_schema_version"`
	AIQualityRunMetrics
}

type AIQualityReport struct {
	Window            string                     `json:"window"`
	From              *time.Time                 `json:"from,omitempty" ts_type:"string"`
	GeneratedAt       time.Time                  `json:"generated_at" ts_type:"string"`
	TagSummary        AIQualityDecisionMetrics   `json:"tag_summary"`
	TagGroups         []AIQualityTagGroup        `json:"tag_groups"`
	SameSourceSummary AIQualityDecisionMetrics   `json:"same_source_summary"`
	SameSourceGroups  []AIQualitySameSourceGroup `json:"same_source_groups"`
	RunSummary        AIQualityRunMetrics        `json:"run_summary"`
	RunGroups         []AIQualityRunGroup        `json:"run_groups"`
}

type AIQualityService struct {
	now func() time.Time
}

func NewAIQualityService() *AIQualityService {
	return &AIQualityService{now: time.Now}
}

type aiQualityTagSample struct {
	TagID               uint
	TagName             string
	Confidence          string
	Status              string
	ModelIdentifier     string
	PromptSchemaVersion string
}

type aiQualitySameSourceSample struct {
	Confidence              string
	Status                  string
	ModelIdentifier         string
	ComparisonPromptVersion string
	DetectionVersion        string
}

type aiQualityRunSample struct {
	Status              string
	ModelIdentifier     string
	PromptSchemaVersion string
	DurationMS          int64
	RequestCount        int
	ToolCallCount       int
	Finished            bool
}

func (s *AIQualityService) Report(filter AIQualityFilter) (*AIQualityReport, error) {
	if s == nil {
		return nil, errors.New("AI quality service is unavailable")
	}
	filter, from, err := normalizeAIQualityFilter(filter, s.now())
	if err != nil {
		return nil, err
	}
	tagSamples, err := loadAIQualityTagSamples(filter, from)
	if err != nil {
		return nil, err
	}
	sameSourceSamples, err := loadAIQualitySameSourceSamples(filter, from)
	if err != nil {
		return nil, err
	}
	runSamples, err := loadAIQualityRunSamples(filter, from)
	if err != nil {
		return nil, err
	}
	report := &AIQualityReport{
		Window: filter.Window, From: from, GeneratedAt: s.now(),
		TagGroups: make([]AIQualityTagGroup, 0), SameSourceGroups: make([]AIQualitySameSourceGroup, 0), RunGroups: make([]AIQualityRunGroup, 0),
	}
	report.TagSummary, report.TagGroups = aggregateAIQualityTags(tagSamples)
	report.SameSourceSummary, report.SameSourceGroups = aggregateAIQualitySameSource(sameSourceSamples)
	report.RunSummary, report.RunGroups = aggregateAIQualityRuns(runSamples)
	return report, nil
}

func normalizeAIQualityFilter(filter AIQualityFilter, now time.Time) (AIQualityFilter, *time.Time, error) {
	filter.Window = strings.TrimSpace(strings.ToLower(filter.Window))
	if filter.Window == "" {
		filter.Window = AIQualityWindowThirtyDay
	}
	filter.Confidence = normalizeAIConfidence(filter.Confidence)
	filter.ModelIdentifier = sanitizeAIAttribution(filter.ModelIdentifier)
	filter.PromptSchemaVersion = sanitizeAIAttribution(filter.PromptSchemaVersion)
	filter.ComparisonPromptVersion = sanitizeAIAttribution(filter.ComparisonPromptVersion)
	filter.DetectionVersion = sanitizeAIAttribution(filter.DetectionVersion)
	var from time.Time
	switch filter.Window {
	case AIQualityWindowAll:
		return filter, nil, nil
	case AIQualityWindowThirtyDay:
		from = now.AddDate(0, 0, -30)
	case AIQualityWindowSevenDay:
		from = now.AddDate(0, 0, -7)
	default:
		return filter, nil, errors.New("AI quality window must be all, 30d, or 7d")
	}
	return filter, &from, nil
}

func loadAIQualityTagSamples(filter AIQualityFilter, from *time.Time) ([]aiQualityTagSample, error) {
	query := database.DB.Table("ai_tag_candidates AS candidate").
		Select(`candidate.matched_tag_id AS tag_id,
			COALESCE(tag.name, candidate.suggested_name) AS tag_name,
			candidate.confidence, candidate.status,
			COALESCE(run.model_identifier, '') AS model_identifier,
			COALESCE(run.prompt_schema_version, '') AS prompt_schema_version`).
		Joins("LEFT JOIN ai_tagging_runs AS run ON run.id = candidate.run_id").
		Joins("LEFT JOIN tags AS tag ON tag.id = candidate.matched_tag_id").
		Where("candidate.status IN ?", []string{models.AITagCandidateStatusApproved, models.AITagCandidateStatusRejected})
	if from != nil {
		query = query.Where("COALESCE(candidate.approved_at, candidate.rejected_at, candidate.updated_at) >= ?", *from)
	}
	if filter.TagID > 0 {
		query = query.Where("candidate.matched_tag_id = ?", filter.TagID)
	}
	if filter.Confidence != "" {
		query = query.Where("candidate.confidence = ?", filter.Confidence)
	}
	if filter.ModelIdentifier != "" {
		query = query.Where("COALESCE(NULLIF(run.model_identifier, ''), ?) = ?", AIQualityUnknown, filter.ModelIdentifier)
	}
	if filter.PromptSchemaVersion != "" {
		query = query.Where("COALESCE(NULLIF(run.prompt_schema_version, ''), ?) = ?", AIQualityUnknown, filter.PromptSchemaVersion)
	}
	var rows []aiQualityTagSample
	if err := query.Order("candidate.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for index := range rows {
		rows[index].ModelIdentifier = qualityDimension(rows[index].ModelIdentifier)
		rows[index].PromptSchemaVersion = qualityDimension(rows[index].PromptSchemaVersion)
	}
	return rows, nil
}

func loadAIQualitySameSourceSamples(filter AIQualityFilter, from *time.Time) ([]aiQualitySameSourceSample, error) {
	query := database.DB.Model(&models.AISameSourceEvaluation{})
	if from != nil {
		query = query.Where("COALESCE(rejected_at, detected_at) >= ?", *from)
	}
	if filter.Confidence != "" {
		query = query.Where("confidence = ?", filter.Confidence)
	}
	if filter.ModelIdentifier != "" {
		query = query.Where("COALESCE(NULLIF(model_identifier, ''), ?) = ?", AIQualityUnknown, filter.ModelIdentifier)
	}
	if filter.ComparisonPromptVersion != "" {
		query = query.Where("COALESCE(NULLIF(comparison_prompt_version, ''), ?) = ?", AIQualityUnknown, filter.ComparisonPromptVersion)
	}
	if filter.DetectionVersion != "" {
		query = query.Where("COALESCE(NULLIF(detection_version, ''), ?) = ?", AIQualityUnknown, filter.DetectionVersion)
	}
	var evaluations []models.AISameSourceEvaluation
	if err := query.Order("id ASC").Find(&evaluations).Error; err != nil {
		return nil, err
	}
	rows := make([]aiQualitySameSourceSample, 0, len(evaluations))
	for _, evaluation := range evaluations {
		rows = append(rows, aiQualitySameSourceSample{
			Confidence: evaluation.Confidence, Status: evaluation.Status,
			ModelIdentifier:         qualityDimension(evaluation.ModelIdentifier),
			ComparisonPromptVersion: qualityDimension(evaluation.ComparisonPromptVersion),
			DetectionVersion:        qualityDimension(evaluation.DetectionVersion),
		})
	}

	legacy := database.DB.Model(&models.VideoSameSourceRelation{}).Where("current_evaluation_id IS NULL")
	if from != nil {
		// created_at, not updated_at: marking a relation read must not turn an old
		// detection into a recent sample.
		legacy = legacy.Where("COALESCE(rejected_at, created_at) >= ?", *from)
	}
	if filter.Confidence != "" {
		legacy = legacy.Where("confidence = ?", filter.Confidence)
	}
	if filter.ModelIdentifier != "" && filter.ModelIdentifier != AIQualityUnknown {
		return rows, nil
	}
	if filter.ComparisonPromptVersion != "" && filter.ComparisonPromptVersion != AIQualityUnknown {
		return rows, nil
	}
	if filter.DetectionVersion != "" {
		legacy = legacy.Where("COALESCE(NULLIF(detection_version, ''), ?) = ?", AIQualityUnknown, filter.DetectionVersion)
	}
	var relations []models.VideoSameSourceRelation
	if err := legacy.Order("id ASC").Find(&relations).Error; err != nil {
		return nil, err
	}
	for _, relation := range relations {
		rows = append(rows, aiQualitySameSourceSample{
			Confidence: relation.Confidence, Status: relation.Status,
			ModelIdentifier: AIQualityUnknown, ComparisonPromptVersion: AIQualityUnknown,
			DetectionVersion: qualityDimension(relation.DetectionVersion),
		})
	}
	return rows, nil
}

func loadAIQualityRunSamples(filter AIQualityFilter, from *time.Time) ([]aiQualityRunSample, error) {
	query := database.DB.Model(&models.AITaggingRun{})
	if from != nil {
		query = query.Where("COALESCE(completed_at, updated_at) >= ?", *from)
	}
	if filter.ModelIdentifier != "" {
		query = query.Where("COALESCE(NULLIF(model_identifier, ''), ?) = ?", AIQualityUnknown, filter.ModelIdentifier)
	}
	if filter.PromptSchemaVersion != "" {
		query = query.Where("COALESCE(NULLIF(prompt_schema_version, ''), ?) = ?", AIQualityUnknown, filter.PromptSchemaVersion)
	}
	var runs []models.AITaggingRun
	if err := query.Order("id ASC").Find(&runs).Error; err != nil {
		return nil, err
	}
	rows := make([]aiQualityRunSample, 0, len(runs))
	for _, run := range runs {
		rows = append(rows, aiQualityRunSample{
			Status: run.Status, ModelIdentifier: qualityDimension(run.ModelIdentifier), PromptSchemaVersion: qualityDimension(run.PromptSchemaVersion),
			DurationMS: run.DurationMS, RequestCount: run.RequestCount, ToolCallCount: run.ToolCallCount,
			// Recovery stamps completed_at on interrupted runs, but they never recorded
			// a duration or request count, so they must stay out of those statistics.
			Finished: run.CompletedAt != nil && run.FailureCode != aiRunFailureInterrupted,
		})
	}
	return rows, nil
}

func qualityDimension(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return AIQualityUnknown
}

func decisionMetrics(approved, rejected int64) AIQualityDecisionMetrics {
	metrics := AIQualityDecisionMetrics{Approved: approved, Rejected: rejected, Decided: approved + rejected}
	if metrics.Decided > 0 {
		approvalRate := float64(approved) / float64(metrics.Decided)
		rejectionRate := float64(rejected) / float64(metrics.Decided)
		metrics.ApprovalRate = &approvalRate
		metrics.RejectionRate = &rejectionRate
	}
	return metrics
}

func aggregateAIQualityTags(samples []aiQualityTagSample) (AIQualityDecisionMetrics, []AIQualityTagGroup) {
	type key struct {
		tagID                           uint
		name, confidence, model, prompt string
	}
	counts := make(map[key][2]int64)
	var approved, rejected int64
	for _, sample := range samples {
		itemKey := key{sample.TagID, sample.TagName, sample.Confidence, sample.ModelIdentifier, sample.PromptSchemaVersion}
		count := counts[itemKey]
		if sample.Status == models.AITagCandidateStatusApproved {
			count[0]++
			approved++
		} else if sample.Status == models.AITagCandidateStatusRejected {
			count[1]++
			rejected++
		}
		counts[itemKey] = count
	}
	groups := make([]AIQualityTagGroup, 0, len(counts))
	for itemKey, count := range counts {
		groups = append(groups, AIQualityTagGroup{
			TagID: itemKey.tagID, TagName: itemKey.name, Confidence: itemKey.confidence,
			ModelIdentifier: itemKey.model, PromptSchemaVersion: itemKey.prompt,
			AIQualityDecisionMetrics: decisionMetrics(count[0], count[1]),
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Decided != groups[j].Decided {
			return groups[i].Decided > groups[j].Decided
		}
		if groups[i].TagName != groups[j].TagName {
			return groups[i].TagName < groups[j].TagName
		}
		if groups[i].Confidence != groups[j].Confidence {
			return groups[i].Confidence < groups[j].Confidence
		}
		if groups[i].ModelIdentifier != groups[j].ModelIdentifier {
			return groups[i].ModelIdentifier < groups[j].ModelIdentifier
		}
		return groups[i].PromptSchemaVersion < groups[j].PromptSchemaVersion
	})
	return decisionMetrics(approved, rejected), groups
}

func aggregateAIQualitySameSource(samples []aiQualitySameSourceSample) (AIQualityDecisionMetrics, []AIQualitySameSourceGroup) {
	type key struct{ confidence, model, prompt, detection string }
	counts := make(map[key][2]int64)
	var detected, rejected int64
	for _, sample := range samples {
		itemKey := key{sample.Confidence, sample.ModelIdentifier, sample.ComparisonPromptVersion, sample.DetectionVersion}
		count := counts[itemKey]
		if sample.Status == models.VideoSameSourceStatusDetected {
			count[0]++
			detected++
		} else if sample.Status == models.VideoSameSourceStatusRejected {
			count[1]++
			rejected++
		}
		counts[itemKey] = count
	}
	groups := make([]AIQualitySameSourceGroup, 0, len(counts))
	for itemKey, count := range counts {
		groups = append(groups, AIQualitySameSourceGroup{
			Confidence: itemKey.confidence, ModelIdentifier: itemKey.model,
			ComparisonPromptVersion: itemKey.prompt, DetectionVersion: itemKey.detection,
			AIQualityDecisionMetrics: decisionMetrics(count[0], count[1]),
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Decided != groups[j].Decided {
			return groups[i].Decided > groups[j].Decided
		}
		if groups[i].DetectionVersion != groups[j].DetectionVersion {
			return groups[i].DetectionVersion < groups[j].DetectionVersion
		}
		if groups[i].ModelIdentifier != groups[j].ModelIdentifier {
			return groups[i].ModelIdentifier < groups[j].ModelIdentifier
		}
		if groups[i].ComparisonPromptVersion != groups[j].ComparisonPromptVersion {
			return groups[i].ComparisonPromptVersion < groups[j].ComparisonPromptVersion
		}
		return groups[i].Confidence < groups[j].Confidence
	})
	return decisionMetrics(detected, rejected), groups
}

func aggregateAIQualityRuns(samples []aiQualityRunSample) (AIQualityRunMetrics, []AIQualityRunGroup) {
	type key struct{ model, prompt string }
	groupsByKey := make(map[key][]aiQualityRunSample)
	for _, sample := range samples {
		groupsByKey[key{sample.ModelIdentifier, sample.PromptSchemaVersion}] = append(groupsByKey[key{sample.ModelIdentifier, sample.PromptSchemaVersion}], sample)
	}
	groups := make([]AIQualityRunGroup, 0, len(groupsByKey))
	for itemKey, groupSamples := range groupsByKey {
		groups = append(groups, AIQualityRunGroup{ModelIdentifier: itemKey.model, PromptSchemaVersion: itemKey.prompt, AIQualityRunMetrics: calculateAIQualityRunMetrics(groupSamples)})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Total != groups[j].Total {
			return groups[i].Total > groups[j].Total
		}
		if groups[i].ModelIdentifier != groups[j].ModelIdentifier {
			return groups[i].ModelIdentifier < groups[j].ModelIdentifier
		}
		return groups[i].PromptSchemaVersion < groups[j].PromptSchemaVersion
	})
	return calculateAIQualityRunMetrics(samples), groups
}

func calculateAIQualityRunMetrics(samples []aiQualityRunSample) AIQualityRunMetrics {
	metrics := AIQualityRunMetrics{Total: int64(len(samples))}
	durations := make([]int64, 0, len(samples))
	var finished, requests, tools int64
	for _, sample := range samples {
		switch sample.Status {
		case models.AITaggingStateStatusCompleted:
			metrics.Completed++
		case models.AITaggingStateStatusSkipped:
			metrics.Skipped++
		case models.AITaggingStateStatusFailed:
			metrics.Failed++
		case models.AITaggingStateStatusProcessing:
			metrics.Processing++
		}
		if sample.Finished {
			finished++
			durations = append(durations, sample.DurationMS)
			requests += int64(sample.RequestCount)
			tools += int64(sample.ToolCallCount)
		}
	}
	decided := metrics.Completed + metrics.Skipped + metrics.Failed
	if decided > 0 {
		rate := float64(metrics.Failed) / float64(decided)
		metrics.FailureRate = &rate
	}
	if finished > 0 {
		requestAverage := float64(requests) / float64(finished)
		toolAverage := float64(tools) / float64(finished)
		metrics.AverageRequests = &requestAverage
		metrics.AverageToolCalls = &toolAverage
		p50 := percentileInt64(durations, 0.50)
		p95 := percentileInt64(durations, 0.95)
		metrics.DurationP50MS = &p50
		metrics.DurationP95MS = &p95
	}
	return metrics
}

func percentileInt64(values []int64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if len(sorted) == 1 {
		return float64(sorted[0])
	}
	position := percentile * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return float64(sorted[lower])
	}
	fraction := position - float64(lower)
	return float64(sorted[lower]) + (float64(sorted[upper])-float64(sorted[lower]))*fraction
}
