package services

import (
	"context"
	"fmt"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

type fakeAITaggingConfigProvider struct {
	config AITaggingConfig
	err    error
}

func (p fakeAITaggingConfigProvider) Load() (AITaggingConfig, error) {
	return p.config, p.err
}

type fakeAITaggingClient struct {
	calls       int
	suggestions []AITagSuggestion
	err         error
}

func (c *fakeAITaggingClient) AnalyzeTags(ctx context.Context, req AITaggingRequest) ([]AITagSuggestion, error) {
	c.calls++
	return c.suggestions, c.err
}

func newTestAITaggingService(client *fakeAITaggingClient, provider AITaggingConfigProvider) *AITaggingService {
	if provider == nil {
		provider = fakeAITaggingConfigProvider{config: AITaggingConfig{
			BaseURL:           "http://127.0.0.1:9999/v1",
			APIKey:            "test-key",
			Model:             "test-model",
			FrameCount:        0,
			SubtitleCharLimit: 1000,
			StartupBatchSize:  10,
		}}
	}
	return &AITaggingService{
		configProvider: provider,
		clientFactory: func(AITaggingConfig) AITaggingAIClient {
			return client
		},
		extractor: NewAITaggingExtractor(),
		now:       time.Now,
	}
}

func countRows(t *testing.T, table string) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Table(table).Count(&count).Error; err != nil {
		t.Fatalf("统计表 %s 失败: %v", table, err)
	}
	return count
}

func TestAITaggingSchemaCreatesTablesAndIndexes(t *testing.T) {
	setupVideoServiceTestDB(t)
	if !database.DB.Migrator().HasTable(&models.AITagCandidate{}) {
		t.Fatalf("期望创建 ai_tag_candidates 表")
	}
	if !database.DB.Migrator().HasTable(&models.AITaggingState{}) {
		t.Fatalf("期望创建 ai_tagging_states 表")
	}
	if !database.DB.Migrator().HasTable(&models.AITagApprovalRecord{}) {
		t.Fatalf("期望创建 ai_tag_approval_records 表")
	}
	if !database.DB.Migrator().HasIndex(&models.AITagCandidate{}, "idx_ai_tag_candidates_video_status") {
		t.Fatalf("期望创建候选 video/status 索引")
	}
	if !database.DB.Migrator().HasIndex(&models.AITaggingState{}, "idx_ai_tagging_states_status_processed") {
		t.Fatalf("期望创建状态 status/processed 索引")
	}
}

func TestAITaggingDropsLowConfidenceBeforePersistence(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "quiet.mp4", Path: "/tmp/quiet.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{Label: "未知", Confidence: "low"}}}
	svc := newTestAITaggingService(client, nil)

	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("处理视频失败: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("期望调用 AI 1 次，实际 %d", client.calls)
	}
	if got := countRows(t, "ai_tag_candidates"); got != 0 {
		t.Fatalf("低置信候选不应落库，实际 %d", got)
	}
	if got := countRows(t, "video_tags"); got != 0 {
		t.Fatalf("未审批前不应写 video_tags，实际 %d", got)
	}
}

func TestAITaggingPersistsCandidateButDoesNotWriteOfficialTablesBeforeApproval(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "动作", Color: "#fff"}
	video := models.Video{Name: "fight.mp4", Path: "/tmp/fight.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{Label: "动作", Confidence: "high", MatchedExistingName: "动作", Reasoning: "文件名暗示打斗"}}}
	svc := newTestAITaggingService(client, nil)

	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("处理视频失败: %v", err)
	}
	if got := countRows(t, "ai_tag_candidates"); got != 1 {
		t.Fatalf("期望 1 条候选，实际 %d", got)
	}
	if got := countRows(t, "tags"); got != 1 {
		t.Fatalf("审批前不应新增正式标签，实际 %d", got)
	}
	if got := countRows(t, "video_tags"); got != 0 {
		t.Fatalf("审批前不应写 video_tags，实际 %d", got)
	}
}

func TestApproveAITagCandidateExistingTagWritesOfficialAssociationOnlyAfterConfirmation(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "动作", Color: "#fff"}
	video := models.Video{Name: "fight.mp4", Path: "/tmp/fight.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{Label: "动作", Confidence: "medium", MatchedExistingName: "动作"}}}
	svc := newTestAITaggingService(client, nil)
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("处理视频失败: %v", err)
	}
	var candidate models.AITagCandidate
	if err := database.DB.First(&candidate).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}

	if _, err := svc.ApproveCandidate(candidate.ID); err != nil {
		t.Fatalf("审批候选失败: %v", err)
	}
	if got := countRows(t, "tags"); got != 1 {
		t.Fatalf("匹配已有标签审批不应新增标签，实际 %d", got)
	}
	if got := countRows(t, "video_tags"); got != 1 {
		t.Fatalf("审批后应写入 1 条 video_tags，实际 %d", got)
	}
	if got := countRows(t, "ai_tag_approval_records"); got != 1 {
		t.Fatalf("审批后应记录 1 条 AI 来源，实际 %d", got)
	}
	var approved models.AITagCandidate
	if err := database.DB.First(&approved, candidate.ID).Error; err != nil {
		t.Fatalf("读取审批候选失败: %v", err)
	}
	if approved.Status != models.AITagCandidateStatusApproved {
		t.Fatalf("候选状态错误: %s", approved.Status)
	}
}

func TestApproveAITagCandidateNewTagCreatesOfficialTagInTransaction(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "mystery.mp4", Path: "/tmp/mystery.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{Label: "悬疑", Confidence: "high", MatchType: "new_candidate"}}}
	svc := newTestAITaggingService(client, nil)
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("处理视频失败: %v", err)
	}
	var candidate models.AITagCandidate
	if err := database.DB.First(&candidate).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if _, err := svc.ApproveCandidate(candidate.ID); err != nil {
		t.Fatalf("审批新标签候选失败: %v", err)
	}
	if got := countRows(t, "tags"); got != 1 {
		t.Fatalf("审批新标签后应创建 1 个正式标签，实际 %d", got)
	}
	if got := countRows(t, "video_tags"); got != 1 {
		t.Fatalf("审批后应创建 1 条关联，实际 %d", got)
	}
}

func TestApproveAITagCandidateRollsBackWhenMatchedTagMissing(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "bad.mp4", Path: "/tmp/bad.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	missingTagID := uint(999)
	candidate := models.AITagCandidate{
		VideoID:        video.ID,
		SuggestedName:  "不存在",
		NormalizedName: "不存在",
		MatchedTagID:   &missingTagID,
		Confidence:     models.AITagConfidenceHigh,
		Status:         models.AITagCandidateStatusPending,
	}
	if err := database.DB.Create(&candidate).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}
	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	if _, err := svc.ApproveCandidate(candidate.ID); err == nil {
		t.Fatalf("期望缺失 matched tag 时审批失败")
	}
	if got := countRows(t, "video_tags"); got != 0 {
		t.Fatalf("审批失败应回滚 video_tags，实际 %d", got)
	}
	var loaded models.AITagCandidate
	if err := database.DB.First(&loaded, candidate.ID).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if loaded.Status != models.AITagCandidateStatusPending {
		t.Fatalf("审批失败应保留 pending 状态，实际 %s", loaded.Status)
	}
}

func TestApproveAITagCandidateSupersedesWhenVideoWasManuallyTagged(t *testing.T) {
	setupVideoServiceTestDB(t)
	existingTag := models.Tag{Name: "动作", Color: "#fff"}
	newTag := models.Tag{Name: "悬疑", Color: "#000"}
	video := models.Video{Name: "manual.mp4", Path: "/tmp/manual.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&existingTag).Error; err != nil {
		t.Fatalf("创建已有标签失败: %v", err)
	}
	if err := database.DB.Create(&newTag).Error; err != nil {
		t.Fatalf("创建新标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", video.ID, existingTag.ID).Error; err != nil {
		t.Fatalf("写入人工标签失败: %v", err)
	}
	candidate := models.AITagCandidate{
		VideoID:        video.ID,
		SuggestedName:  "悬疑",
		NormalizedName: "悬疑",
		MatchedTagID:   &newTag.ID,
		Confidence:     models.AITagConfidenceHigh,
		Status:         models.AITagCandidateStatusPending,
	}
	if err := database.DB.Create(&candidate).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}
	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	item, err := svc.ApproveCandidate(candidate.ID)
	if err != nil {
		t.Fatalf("已有人工标签时应过期候选而非失败: %v", err)
	}
	if item.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("候选应标记为 superseded，实际 %s", item.Status)
	}
	if got := countRows(t, "video_tags"); got != 1 {
		t.Fatalf("已有人工标签时不应新增正式关联，实际 %d", got)
	}
	if got := countRows(t, "ai_tag_approval_records"); got != 0 {
		t.Fatalf("已有人工标签时不应记录 AI 来源，实际 %d", got)
	}
}

func TestApproveAITagCandidateSupersedesAfterManualTagAddedFollowingAIApproval(t *testing.T) {
	setupVideoServiceTestDB(t)
	firstTag := models.Tag{Name: "动作", Color: "#fff"}
	secondTag := models.Tag{Name: "悬疑", Color: "#000"}
	manualTag := models.Tag{Name: "剧情", Color: "#333"}
	video := models.Video{Name: "mixed.mp4", Path: "/tmp/mixed.mp4", Directory: "/tmp"}
	for _, tag := range []*models.Tag{&firstTag, &secondTag, &manualTag} {
		if err := database.DB.Create(tag).Error; err != nil {
			t.Fatalf("创建标签失败: %v", err)
		}
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	firstCandidate := models.AITagCandidate{
		VideoID:        video.ID,
		SuggestedName:  "动作",
		NormalizedName: "动作",
		MatchedTagID:   &firstTag.ID,
		Confidence:     models.AITagConfidenceHigh,
		Status:         models.AITagCandidateStatusPending,
	}
	secondCandidate := models.AITagCandidate{
		VideoID:        video.ID,
		SuggestedName:  "悬疑",
		NormalizedName: "悬疑",
		MatchedTagID:   &secondTag.ID,
		Confidence:     models.AITagConfidenceHigh,
		Status:         models.AITagCandidateStatusPending,
	}
	if err := database.DB.Create(&firstCandidate).Error; err != nil {
		t.Fatalf("创建首个候选失败: %v", err)
	}
	if err := database.DB.Create(&secondCandidate).Error; err != nil {
		t.Fatalf("创建第二个候选失败: %v", err)
	}
	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	if _, err := svc.ApproveCandidate(firstCandidate.ID); err != nil {
		t.Fatalf("审批首个候选失败: %v", err)
	}
	if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", video.ID, manualTag.ID).Error; err != nil {
		t.Fatalf("写入人工标签失败: %v", err)
	}
	item, err := svc.ApproveCandidate(secondCandidate.ID)
	if err != nil {
		t.Fatalf("人工补标签后审批旧候选应过期而非失败: %v", err)
	}
	if item.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("第二个候选应标记为 superseded，实际 %s", item.Status)
	}
	if got := countRows(t, "video_tags"); got != 2 {
		t.Fatalf("人工补标签后不应新增第二个 AI 关联，实际 %d", got)
	}
	if got := countRows(t, "ai_tag_approval_records"); got != 1 {
		t.Fatalf("只应保留首个 AI 来源记录，实际 %d", got)
	}
}

func TestAITaggingFingerprintChangeAllowsSameLabelReanalysis(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "剧情", Color: "#fff"}
	video := models.Video{Name: "story.mp4", Path: "/tmp/story.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{Label: "剧情", Confidence: "high", MatchedExistingName: "剧情"}}}
	svc := newTestAITaggingService(client, nil)
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("首次处理失败: %v", err)
	}
	if err := database.DB.Model(&tag).Update("color", "#000").Error; err != nil {
		t.Fatalf("更新标签失败: %v", err)
	}
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("同名候选重分析失败: %v", err)
	}
	var superseded int64
	if err := database.DB.Model(&models.AITagCandidate{}).Where("status = ?", models.AITagCandidateStatusSuperseded).Count(&superseded).Error; err != nil {
		t.Fatalf("统计 superseded 失败: %v", err)
	}
	var pending int64
	if err := database.DB.Model(&models.AITagCandidate{}).Where("status = ?", models.AITagCandidateStatusPending).Count(&pending).Error; err != nil {
		t.Fatalf("统计 pending 失败: %v", err)
	}
	if superseded != 1 || pending != 1 {
		t.Fatalf("重分析后应保留 1 条 superseded 和 1 条 pending，实际 superseded=%d pending=%d", superseded, pending)
	}
}

func TestAITaggingMissingConfigDoesNotCallAI(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "no-config.mp4", Path: "/tmp/no-config.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	client := &fakeAITaggingClient{}
	svc := newTestAITaggingService(client, fakeAITaggingConfigProvider{err: fmt.Errorf("missing config")})
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("缺配置应记录跳过状态而非失败: %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("缺配置不应调用 AI，实际 %d", client.calls)
	}
	var state models.AITaggingState
	if err := database.DB.Where("video_id = ?", video.ID).First(&state).Error; err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state.Status != models.AITaggingStateStatusSkipped || state.SkipReason != "config_unavailable" {
		t.Fatalf("状态错误: %#v", state)
	}
}

func TestAITaggingFingerprintChangeAllowsReanalysis(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "剧情", Color: "#fff"}
	video := models.Video{Name: "story.mp4", Path: "/tmp/story.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{Label: "剧情", Confidence: "high", MatchedExistingName: "剧情"}}}
	svc := newTestAITaggingService(client, nil)
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("首次处理失败: %v", err)
	}
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("相同 fingerprint 再处理失败: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("相同 fingerprint 不应重复调用 AI，实际 %d", client.calls)
	}
	if err := database.DB.Model(&tag).Update("name", "故事").Error; err != nil {
		t.Fatalf("更新标签失败: %v", err)
	}
	client.suggestions = []AITagSuggestion{{Label: "故事", Confidence: "high", MatchedExistingName: "故事"}}
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("标签库变化后重分析失败: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("fingerprint 变化后应重新调用 AI，实际 %d", client.calls)
	}
	var pending int64
	if err := database.DB.Model(&models.AITagCandidate{}).Where("status = ?", models.AITagCandidateStatusPending).Count(&pending).Error; err != nil {
		t.Fatalf("统计 pending 失败: %v", err)
	}
	if pending != 1 {
		t.Fatalf("重分析后应只有 1 条 pending 候选，实际 %d", pending)
	}
}
