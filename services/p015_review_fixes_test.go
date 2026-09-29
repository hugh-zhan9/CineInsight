package services

import (
	"context"
	"testing"

	"video-master/database"
	"video-master/models"
)

// 本文件验收 P-015 独立评审的修复项（META-11 / IMG-08）。

// META-11：批量批准的错误信息是中文；同视频同标签被前一条批准作废的项标为 superseded，
// 不计入失败。
func TestApproveAITagCandidatesBatchChineseMessagesAndSupersededMETA11(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := p015Tag(t, "动作", "custom")
	video := p015Video(t, "dup.mp4")
	first := p015Candidate(t, video.ID, tag, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	second := p015Candidate(t, video.ID, tag, models.AITagConfidenceMedium, models.AITagCandidateStatusPending)
	other := p015Video(t, "other.mp4")
	rejected := p015Candidate(t, other.ID, tag, models.AITagConfidenceHigh, models.AITagCandidateStatusRejected)
	low := p015Candidate(t, other.ID, tag, models.AITagConfidenceLow, models.AITagCandidateStatusPending)

	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	result := svc.ApproveCandidates([]uint{first.ID, second.ID, rejected.ID, low.ID, 999999})
	byID := map[uint]AITagBatchItemResult{}
	for _, item := range result.Results {
		byID[item.ID] = item
	}
	if !byID[first.ID].OK {
		t.Fatalf("第一条应批准成功: %+v", byID[first.ID])
	}
	if got := byID[second.ID]; got.OK || !got.Superseded || got.Message != "候选已被同标签的其他候选替代" {
		t.Fatalf("被作废的项应标 superseded 且文案为中文: %+v", got)
	}
	if got := byID[rejected.ID]; got.OK || got.Superseded || got.Message != "候选已不在待审状态" {
		t.Fatalf("已拒绝的候选文案不符: %+v", got)
	}
	if got := byID[low.ID]; got.Message != "候选置信度过低，不可批准" {
		t.Fatalf("低置信度文案不符: %+v", got)
	}
	if got := byID[999999]; got.OK || got.Message != "候选不存在，或所属视频已被删除" {
		t.Fatalf("不存在的候选文案不符: %+v", got)
	}
	if result.Succeeded != 1 || result.Superseded != 1 || result.Failed != 3 || result.Requested != 5 {
		t.Fatalf("计数应为 成功1/作废1/失败3: %+v", result)
	}
	// 单条批准的英文文案保持不变（前端按子串判断）。
	if _, err := svc.ApproveCandidate(rejected.ID); err == nil || err.Error() != "candidate is not pending" {
		t.Fatalf("单条批准的错误文案不得改变: %v", err)
	}
}

// IMG-08：分析途中词表变化触发自动重排时，显式重试标记 manual_retry 不得被抹掉。
func TestAITaggingLibraryChangeRequeueKeepsManualRetryIMG08(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := p015Tag(t, "动作", "custom")
	video := p015Video(t, "retry-lib-change.mp4")
	p015AttachManualVideoTag(t, video.ID)

	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	client := &libraryMutatingClient{}
	svc.clientFactory = func(AITaggingConfig) AITaggingAIClient { return client }
	if err := svc.RetryVideo(video.ID); err != nil {
		t.Fatal(err)
	}
	svc.runWorkerOnce(context.Background())
	if client.calls != 1 {
		t.Fatalf("应分析一次: %d", client.calls)
	}
	var state models.AITaggingState
	if err := database.DB.Where("video_id = ?", video.ID).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.Status != models.AITaggingStateStatusPending || state.SkipReason != aiTaggingSkipReasonManualRetry {
		t.Fatalf("重排后应仍带 manual_retry: %+v", state)
	}
	_ = tag
}

// 自动路径（非显式）的重排不带标记，行为不变。
func TestAITaggingLibraryChangeRequeueAutomaticStaysUnmarkedIMG08(t *testing.T) {
	setupVideoServiceTestDB(t)
	p015Tag(t, "动作", "custom")
	video := p015Video(t, "auto-lib-change.mp4")

	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	client := &libraryMutatingClient{}
	svc.clientFactory = func(AITaggingConfig) AITaggingAIClient { return client }
	svc.runWorkerOnce(context.Background())
	var state models.AITaggingState
	if err := database.DB.Where("video_id = ?", video.ID).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.Status != models.AITaggingStateStatusPending || state.SkipReason != "" {
		t.Fatalf("自动重排不应带标记: %+v", state)
	}
}

// libraryMutatingClient 在分析途中新增一个词表标签，让词表哈希在分析前后不同。
type libraryMutatingClient struct {
	calls int
}

func (c *libraryMutatingClient) AnalyzeTags(context.Context, AITaggingRequest) ([]AITagSuggestion, error) {
	c.calls++
	tag := models.Tag{Name: "途中新增", Color: "#000", Namespace: "custom", IsSystem: true, IsActive: true}
	if err := database.DB.Create(&tag).Error; err != nil {
		return nil, err
	}
	return nil, nil
}
