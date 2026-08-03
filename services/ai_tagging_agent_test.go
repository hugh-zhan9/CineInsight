package services

import (
	"context"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

type scriptedAgentClient struct {
	decisions []AITagAgentDecision
	index     int
}

func (c *scriptedAgentClient) AnalyzeTags(context.Context, AITaggingRequest) ([]AITagSuggestion, error) {
	return nil, nil
}

func (c *scriptedAgentClient) DecideNextAction(context.Context, AITagAgentDecisionRequest) (AITagAgentDecision, error) {
	decision := c.decisions[c.index]
	c.index++
	return decision, nil
}

type fakeTemporaryTranscriptProvider struct{}

func (fakeTemporaryTranscriptProvider) GenerateTemporaryTranscript(context.Context, models.Video, int, SubtitleRecognitionConfig) (TemporaryTranscriptEvidence, error) {
	return TemporaryTranscriptEvidence{Text: "private temporary transcript", DetectedLang: "zh", Engine: "whisperx"}, nil
}

type fakeSameSourceProvider struct{}

func (fakeSameSourceProvider) FindSameSource(context.Context, models.Video, AITaggingAIClient) (AISameSourceEvidence, error) {
	return AISameSourceEvidence{Summary: "发现同源视频 candidate.mp4", Relations: []VideoSameSourceReviewItem{{ID: 9}}}, nil
}

func TestAgentEvidenceLoopUsesEachExpensiveToolOnceAndDoesNotPersistTranscript(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "agent.mp4", Path: "/tmp/agent.mp4", Duration: 60}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAITaggingService()
	service.transcript = fakeTemporaryTranscriptProvider{}
	service.sameSource = fakeSameSourceProvider{}
	if err := service.setProcessing(video.ID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	client := &scriptedAgentClient{decisions: []AITagAgentDecision{
		{Action: models.AITagAgentActionTranscript},
		{Action: models.AITagAgentActionFindSameSource},
		{Action: models.AITagAgentActionFinalize},
	}}
	evidence, err := service.runAgentEvidenceLoop(context.Background(), video, nil, AITaggingEvidence{}, strings.Repeat("a", 64), AITaggingConfig{MaxExtraFrames: 20, SubtitleCharLimit: 1000}, client)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.SubtitleText != "private temporary transcript" || !evidence.SubtitleTemporary {
		t.Fatalf("temporary transcript missing from in-memory evidence: %+v", evidence)
	}
	if !strings.Contains(evidence.AdditionalProperties["same_source_evidence"], "candidate.mp4") {
		t.Fatalf("same-source evidence missing: %+v", evidence.AdditionalProperties)
	}
	if strings.Contains(evidence.SummaryJSON(), "private temporary transcript") {
		t.Fatal("temporary transcript must not be persisted in evidence summary")
	}
	if _, err := service.persistSuggestions(video, nil, evidence, []AITagSuggestion{{
		Label: "temporary-evidence", Confidence: models.AITagConfidenceHigh,
		Reasoning: "private temporary transcript",
	}}); err != nil {
		t.Fatal(err)
	}
	var candidate models.AITagCandidate
	if err := database.DB.First(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(candidate.Reasoning, "private temporary transcript") || !strings.Contains(candidate.Reasoning, "正文未保存") {
		t.Fatalf("temporary transcript must not leak through model reasoning: %q", candidate.Reasoning)
	}
	var steps []models.AITagAgentStep
	if err := database.DB.Order("round").Find(&steps).Error; err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 || steps[0].Action != models.AITagAgentActionTranscript || steps[1].ActualCount != 1 || steps[2].FinishReason != "model_finalize" {
		t.Fatalf("unexpected persisted agent steps: %+v", steps)
	}
}

func TestAgentEvidenceLoopForcesFinalizeAtDecisionBudget(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "budget.mp4", Path: "/tmp/budget.mp4", Duration: 60}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAITaggingService()
	service.transcript = fakeTemporaryTranscriptProvider{}
	if err := service.setProcessing(video.ID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	client := &scriptedAgentClient{decisions: []AITagAgentDecision{
		{Action: models.AITagAgentActionTranscript},
		{Action: models.AITagAgentActionTranscript},
		{Action: models.AITagAgentActionTranscript},
		{Action: models.AITagAgentActionTranscript},
	}}
	if _, err := service.runAgentEvidenceLoop(context.Background(), video, nil, AITaggingEvidence{}, strings.Repeat("b", 64), AITaggingConfig{MaxExtraFrames: 20}, client); err != nil {
		t.Fatal(err)
	}
	var final models.AITagAgentStep
	if err := database.DB.Order("round DESC").First(&final).Error; err != nil {
		t.Fatal(err)
	}
	if final.Round != aiTagAgentMaxDecisions || final.FinishReason != "decision_budget_exhausted" || final.ToolStatus != models.AITagToolStatusRejected {
		t.Fatalf("fourth decision must force finalization: %+v", final)
	}
}

func TestAgentEvidenceLoopRejectsWholeOverBudgetFrameRequest(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "over-budget.mp4", Path: "/tmp/over-budget.mp4", Duration: 60}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAITaggingService()
	fingerprint := strings.Repeat("c", 64)
	if err := service.setProcessing(video.ID, fingerprint); err != nil {
		t.Fatal(err)
	}
	client := &scriptedAgentClient{decisions: []AITagAgentDecision{
		{Action: models.AITagAgentActionMoreFrames, RequestedFrameCount: 21},
		{Action: models.AITagAgentActionFinalize},
	}}
	evidence, err := service.runAgentEvidenceLoop(context.Background(), video, nil, AITaggingEvidence{}, fingerprint, AITaggingConfig{MaxExtraFrames: 20}, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Frames) != 0 {
		t.Fatalf("over-budget request must be rejected atomically, got %d frames", len(evidence.Frames))
	}
	var first models.AITagAgentStep
	if err := database.DB.Order("round").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if first.ToolStatus != models.AITagToolStatusRejected || first.ObservationCode != "extra_frame_budget_exceeded" {
		t.Fatalf("unexpected over-budget step: %+v", first)
	}
}

func TestAgentEvidenceLoopRejectsFrameCountOnNonFrameAction(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "invalid-count.mp4", Path: "/tmp/invalid-count.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAITaggingService()
	service.transcript = fakeTemporaryTranscriptProvider{}
	fingerprint := strings.Repeat("f", 64)
	if err := service.setProcessing(video.ID, fingerprint); err != nil {
		t.Fatal(err)
	}
	client := &scriptedAgentClient{decisions: []AITagAgentDecision{
		{Action: models.AITagAgentActionTranscript, RequestedFrameCount: 3},
		{Action: models.AITagAgentActionFinalize},
	}}
	evidence, err := service.runAgentEvidenceLoop(context.Background(), video, nil, AITaggingEvidence{}, fingerprint, AITaggingConfig{MaxExtraFrames: 20}, client)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.SubtitleText != "" {
		t.Fatal("non-frame action with frame count must not execute")
	}
	var first models.AITagAgentStep
	if err := database.DB.Order("round").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if first.ObservationCode != "unexpected_frame_count" || first.ToolStatus != models.AITagToolStatusRejected {
		t.Fatalf("unexpected rejected step: %+v", first)
	}
}

func TestParseAITagAgentDecisionAndRepresentativeFrameBoundary(t *testing.T) {
	decision, err := parseAITagAgentDecision("```json\n{\"action\":\"request_more_frames\",\"requested_frame_count\":7}\n```")
	if err != nil || decision.Action != models.AITagAgentActionMoreFrames || decision.RequestedFrameCount != 7 {
		t.Fatalf("unexpected decision=%+v err=%v", decision, err)
	}
	if _, err := parseAITagAgentDecision(`{"action":"unknown"}`); err == nil {
		t.Fatal("unknown action must fail")
	}
	legacy, err := parseAITagAgentDecision(`{"reasoning":"enough evidence"}`)
	if err != nil || legacy.Action != models.AITagAgentActionFinalize {
		t.Fatalf("missing action must preserve legacy finalize compatibility: %+v err=%v", legacy, err)
	}
	frames := []AITaggingFrame{{Index: 1}, {Index: 2}, {Index: 3}}
	selected := selectRepresentativeAITaggingFrames(frames, 1)
	if len(selected) != 1 || selected[0].Index != 2 {
		t.Fatalf("single representative frame must use the middle frame: %+v", selected)
	}
}

func TestRejectedSameSourcePairBlocksOnlyUnchangedFingerprints(t *testing.T) {
	setupVideoServiceTestDB(t)
	videos := []models.Video{{Name: "a.mp4", Path: "/tmp/a.mp4"}, {Name: "b.mp4", Path: "/tmp/b.mp4"}}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	relation := models.VideoSameSourceRelation{
		VideoAID: videos[0].ID, VideoBID: videos[1].ID,
		VideoAFingerprint: "left-v1", VideoBFingerprint: "right-v1",
		Status: models.VideoSameSourceStatusRejected, Confidence: models.AITagConfidenceHigh,
		DetectionVersion: sameSourceFingerprintVersion, IsUnread: false, RejectedAt: &now,
	}
	if err := database.DB.Create(&relation).Error; err != nil {
		t.Fatal(err)
	}
	blocked, err := rejectedSameSourcePairStillCurrent(videos[1].ID, videos[0].ID, "right-v1", "left-v1")
	if err != nil || !blocked {
		t.Fatalf("unchanged rejected fingerprints must remain blocked: blocked=%v err=%v", blocked, err)
	}
	blocked, err = rejectedSameSourcePairStillCurrent(videos[0].ID, videos[1].ID, "left-v2", "right-v1")
	if err != nil || blocked {
		t.Fatalf("changed content fingerprint must allow re-confirmation: blocked=%v err=%v", blocked, err)
	}
}

func TestTemporaryTranscriptSlotWaitIsContextAware(t *testing.T) {
	service := NewSubtitleService(t.TempDir())
	if err := service.acquireTranscriptionSlot(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.acquireTranscriptionSlot(ctx); err == nil {
		t.Fatal("cancelled waiter must not block on the shared transcription slot")
	}
	service.releaseTranscriptionSlot()
	if !isSubtitleLocalOnlyASR(withSubtitleLocalOnlyASR(context.Background())) {
		t.Fatal("temporary ASR context must force offline model lookup")
	}
}

func TestSameSourceRelationReviewLifecycleIsNormalizedAndIdempotent(t *testing.T) {
	setupVideoServiceTestDB(t)
	videos := []models.Video{{Name: "left.mp4", Path: "/tmp/left.mp4"}, {Name: "right.mp4", Path: "/tmp/right.mp4"}}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAISameSourceService(nil)
	leftFingerprint := models.VideoVisualFingerprint{VideoID: videos[0].ID, ContentFingerprint: "left-content"}
	rightFingerprint := models.VideoVisualFingerprint{VideoID: videos[1].ID, ContentFingerprint: "right-content"}
	relation, err := service.persistDetectedRelation(videos[1], videos[0], rightFingerprint, leftFingerprint, AISameSourceComparison{
		SameSource: true, Confidence: models.AITagConfidenceHigh, Reasoning: "matching frames",
	})
	if err != nil {
		t.Fatal(err)
	}
	if relation.VideoAID != videos[0].ID || relation.VideoBID != videos[1].ID || !relation.IsUnread {
		t.Fatalf("relation pair must be normalized and unread: %+v", relation)
	}
	if err := service.MarkRelationRead(relation.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkRelationRead(relation.ID); err != nil {
		t.Fatalf("mark read must be idempotent: %v", err)
	}
	if err := service.RejectRelation(relation.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.RejectRelation(relation.ID); err != nil {
		t.Fatalf("reject must be idempotent: %v", err)
	}
	protected, err := service.persistDetectedRelation(videos[0], videos[1], leftFingerprint, rightFingerprint, AISameSourceComparison{
		SameSource: true, Confidence: models.AITagConfidenceHigh, Reasoning: "late model response",
	})
	if err != nil {
		t.Fatal(err)
	}
	if protected.Status != models.VideoSameSourceStatusRejected {
		t.Fatalf("late detection must not overwrite a current user rejection: %+v", protected)
	}
	blocked, err := rejectedSameSourcePairStillCurrent(videos[0].ID, videos[1].ID, "left-content", "right-content")
	if err != nil || !blocked {
		t.Fatalf("rejected current relation must block re-confirmation: blocked=%v err=%v", blocked, err)
	}
}

func TestSameSourceReviewCanBeConfirmedAndExcludesDeletedVideos(t *testing.T) {
	setupVideoServiceTestDB(t)
	videos := []models.Video{{Name: "left.mp4", Path: "/tmp/left-review.mp4"}, {Name: "right.mp4", Path: "/tmp/right-review.mp4"}}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAISameSourceService(nil)
	relation, err := service.persistDetectedRelation(
		videos[0], videos[1],
		models.VideoVisualFingerprint{VideoID: videos[0].ID, ContentFingerprint: "left-review"},
		models.VideoVisualFingerprint{VideoID: videos[1].ID, ContentFingerprint: "right-review"},
		AISameSourceComparison{SameSource: true, Confidence: models.AITagConfidenceHigh, Reasoning: "same"},
	)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.ListRelations(models.VideoSameSourceStatusDetected, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("unreviewed relation should be visible: items=%+v err=%v", items, err)
	}
	if err := service.ConfirmRelation(relation.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmRelation(relation.ID); err != nil {
		t.Fatalf("confirm must be idempotent: %v", err)
	}
	items, err = service.ListRelations(models.VideoSameSourceStatusDetected, false)
	if err != nil || len(items) != 0 {
		t.Fatalf("confirmed relation should leave the workbench: items=%+v err=%v", items, err)
	}

	if err := database.DB.Model(&relation).Updates(map[string]interface{}{"reviewed_at": nil, "is_unread": true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&videos[0]).Error; err != nil {
		t.Fatal(err)
	}
	items, err = service.ListRelations(models.VideoSameSourceStatusDetected, false)
	if err != nil || len(items) != 0 {
		t.Fatalf("relations containing a deleted video should be hidden: items=%+v err=%v", items, err)
	}
}
