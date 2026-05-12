package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestSettingsAITaggingConfigProviderLoadsDatabaseSettings(t *testing.T) {
	setupVideoServiceTestDB(t)
	t.Setenv(envAITaggingBaseURL, "http://env.example/v1")
	t.Setenv(envAITaggingAPIKey, "env-key")
	t.Setenv(envAITaggingModel, "env-model")

	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(map[string]interface{}{
		"ai_tagging_base_url":            "http://db.example/v1",
		"ai_tagging_api_key":             "db-key",
		"ai_tagging_model":               "db-model",
		"ai_tagging_frame_count":         3,
		"ai_tagging_subtitle_char_limit": 1200,
		"ai_tagging_startup_batch_size":  5,
	}).Error; err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}

	config, err := SettingsAITaggingConfigProvider{}.Load()
	if err != nil {
		t.Fatalf("读取 AI 配置失败: %v", err)
	}
	if config.BaseURL != "http://db.example/v1" || config.APIKey != "db-key" || config.Model != "db-model" {
		t.Fatalf("期望优先读取数据库配置，实际: %+v", config)
	}
	if config.FrameCount != 3 || config.SubtitleCharLimit != 1200 || config.StartupBatchSize != 5 {
		t.Fatalf("期望读取数据库数值配置，实际: %+v", config)
	}
}

func TestSettingsAITaggingConfigProviderAllowsLocalEndpointWithoutAPIKey(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(map[string]interface{}{
		"ai_tagging_base_url": "http://127.0.0.1:1234/v1",
		"ai_tagging_api_key":  "",
		"ai_tagging_model":    "local-model",
	}).Error; err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}

	config, err := SettingsAITaggingConfigProvider{}.Load()
	if err != nil {
		t.Fatalf("本地兼容接口不应强制要求 API Key: %v", err)
	}
	if config.APIKey != "" || config.BaseURL == "" || config.Model == "" {
		t.Fatalf("本地配置读取异常: %+v", config)
	}
}

func TestOpenAICompatibleClientSkipsAuthorizationHeaderWhenAPIKeyEmpty(t *testing.T) {
	var seenAuth string
	var seenModel string
	var hasResponseFormat bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("解析请求体失败: %v", err)
		}
		if model, ok := body["model"].(string); ok {
			seenModel = model
		}
		_, hasResponseFormat = body["response_format"]
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": `{"suggestions":[]}`}},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAICompatibleAITaggingClient(AITaggingConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "demo-model",
	})
	_, err := client.AnalyzeTags(context.Background(), AITaggingRequest{
		Video: models.Video{ID: 1, Name: "demo.mp4", Path: "/tmp/demo.mp4"},
	})
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if seenAuth != "" {
		t.Fatalf("空 API Key 时不应发送 Authorization，实际 %q", seenAuth)
	}
	if seenModel != "demo-model" {
		t.Fatalf("模型名不正确: %q", seenModel)
	}
	if hasResponseFormat {
		t.Fatalf("不应发送 LM Studio 不兼容的 response_format 字段")
	}
}

func TestOpenAICompatibleClientUsesLongTimeoutForLocalVisionModels(t *testing.T) {
	client, ok := NewOpenAICompatibleAITaggingClient(AITaggingConfig{
		BaseURL: "http://127.0.0.1:1234/v1",
		Model:   "vision-model",
	}).(*OpenAICompatibleAITaggingClient)
	if !ok {
		t.Fatalf("客户端类型不正确")
	}
	if client.client.Timeout < 5*time.Minute {
		t.Fatalf("本地视觉模型请求超时时间过短: %s", client.client.Timeout)
	}
}

func TestAITaggingFrameCountSupportsFiveFrameDefault(t *testing.T) {
	if defaultAITaggingFrameCount != 5 {
		t.Fatalf("默认抽帧数量应为 5，实际 %d", defaultAITaggingFrameCount)
	}
	if got := normalizedAITaggingFrameCount(5); got != 5 {
		t.Fatalf("应支持一次抽取 5 帧，实际 %d", got)
	}
	if got := normalizedAITaggingFrameCount(99); got != aiTaggingFrameMaxCount {
		t.Fatalf("抽帧数量应限制到上限 %d，实际 %d", aiTaggingFrameMaxCount, got)
	}
}

func TestOpenAICompatibleClientPromptPrioritizesFramesAndExistingTags(t *testing.T) {
	client := NewOpenAICompatibleAITaggingClient(AITaggingConfig{
		BaseURL:           "http://127.0.0.1:1234/v1",
		Model:             "vision-model",
		SubtitleCharLimit: 1000,
	}).(*OpenAICompatibleAITaggingClient)

	body := client.buildRequest(AITaggingRequest{
		Video: models.Video{ID: 1, Name: "4K超清舞蹈.mp4", Path: "/tmp/4K超清舞蹈.mp4"},
		ExistingTags: []models.Tag{
			{Name: "4K"},
			{Name: "舞蹈"},
		},
		Evidence: AITaggingEvidence{
			Frames: []AITaggingFrame{
				{DataURL: "data:image/jpeg;base64,abc", Index: 1, Position: 12.3},
			},
		},
	})
	messages := body["messages"].([]map[string]interface{})
	userContent := messages[1]["content"].([]map[string]interface{})
	text := userContent[0]["text"].(string)
	if !strings.Contains(text, "必须优先根据画面内容判断") || !strings.Contains(text, "label 必须使用已有标签的原始名称") {
		t.Fatalf("prompt 未强调画面优先和已有标签优先: %s", text)
	}
	if len(userContent) != 3 {
		t.Fatalf("期望文本、帧说明、图片三段内容，实际 %d", len(userContent))
	}
}

func TestParseAITagSuggestionsAllowsMarkdownWrappedJSON(t *testing.T) {
	suggestions, err := parseAITagSuggestions("这里是结果：\n```json\n{\"suggestions\":[{\"label\":\"动作\",\"confidence\":\"high\",\"match_type\":\"existing_exact\"}]}\n```")
	if err != nil {
		t.Fatalf("解析带代码块的 JSON 失败: %v", err)
	}
	if len(suggestions) != 1 || suggestions[0].Label != "动作" || suggestions[0].Confidence != "high" {
		t.Fatalf("解析结果不正确: %+v", suggestions)
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

func TestAITaggingPersistsMatchedExistingTagNameInsteadOfModelSynonym(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "4K", Color: "#fff"}
	video := models.Video{Name: "demo.mp4", Path: "/tmp/demo.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{Label: "4K超清", Confidence: "high", MatchedExistingName: "4K"}}}
	svc := newTestAITaggingService(client, nil)

	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("处理视频失败: %v", err)
	}
	var candidate models.AITagCandidate
	if err := database.DB.First(&candidate).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if candidate.SuggestedName != "4K" || candidate.NormalizedName != normalizeAITagName("4K") {
		t.Fatalf("应使用已有标签原名落库，实际 %+v", candidate)
	}
	if candidate.MatchedTagID == nil || *candidate.MatchedTagID != tag.ID {
		t.Fatalf("应关联已有标签，实际 %+v", candidate.MatchedTagID)
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

func TestRejectPendingCandidatesByVideoRejectsOnlyThatVideosPendingCandidates(t *testing.T) {
	setupVideoServiceTestDB(t)
	videoA := models.Video{Name: "a.mp4", Path: "/tmp/ai-reject-a.mp4", Directory: "/tmp"}
	videoB := models.Video{Name: "b.mp4", Path: "/tmp/ai-reject-b.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&videoA).Error; err != nil {
		t.Fatalf("创建视频A失败: %v", err)
	}
	if err := database.DB.Create(&videoB).Error; err != nil {
		t.Fatalf("创建视频B失败: %v", err)
	}
	candidates := []models.AITagCandidate{
		{VideoID: videoA.ID, SuggestedName: "动作", NormalizedName: "动作", Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending},
		{VideoID: videoA.ID, SuggestedName: "剧情", NormalizedName: "剧情", Confidence: models.AITagConfidenceMedium, Status: models.AITagCandidateStatusPending},
		{VideoID: videoA.ID, SuggestedName: "旧", NormalizedName: "旧", Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusRejected},
		{VideoID: videoB.ID, SuggestedName: "保留", NormalizedName: "保留", Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending},
	}
	if err := database.DB.Create(&candidates).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}
	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	rejected, err := svc.RejectPendingCandidatesByVideo(videoA.ID)
	if err != nil {
		t.Fatalf("批量拒绝失败: %v", err)
	}
	if rejected != 2 {
		t.Fatalf("应拒绝 2 条待审候选，实际 %d", rejected)
	}
	var videoAPending int64
	if err := database.DB.Model(&models.AITagCandidate{}).Where("video_id = ? AND status = ?", videoA.ID, models.AITagCandidateStatusPending).Count(&videoAPending).Error; err != nil {
		t.Fatalf("统计视频A待审失败: %v", err)
	}
	if videoAPending != 0 {
		t.Fatalf("视频A不应再有待审候选，实际 %d", videoAPending)
	}
	var videoBPending int64
	if err := database.DB.Model(&models.AITagCandidate{}).Where("video_id = ? AND status = ?", videoB.ID, models.AITagCandidateStatusPending).Count(&videoBPending).Error; err != nil {
		t.Fatalf("统计视频B待审失败: %v", err)
	}
	if videoBPending != 1 {
		t.Fatalf("视频B待审候选不应受影响，实际 %d", videoBPending)
	}
}

func TestListAITagCandidatesExcludesSoftDeletedVideos(t *testing.T) {
	setupVideoServiceTestDB(t)
	activeVideo := models.Video{Name: "active.mp4", Path: "/tmp/ai-active.mp4", Directory: "/tmp"}
	deletedVideo := models.Video{Name: "deleted.mp4", Path: "/tmp/ai-deleted.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&activeVideo).Error; err != nil {
		t.Fatalf("创建有效视频失败: %v", err)
	}
	if err := database.DB.Create(&deletedVideo).Error; err != nil {
		t.Fatalf("创建待删除视频失败: %v", err)
	}
	candidates := []models.AITagCandidate{
		{VideoID: activeVideo.ID, SuggestedName: "保留", NormalizedName: "保留", Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending},
		{VideoID: deletedVideo.ID, SuggestedName: "隐藏", NormalizedName: "隐藏", Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending},
	}
	if err := database.DB.Create(&candidates).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}
	if err := database.DB.Delete(&deletedVideo).Error; err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}

	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	items, err := svc.ListCandidates(0, "", models.AITagCandidateStatusPending)
	if err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("审阅列表应只包含有效视频候选，实际 %d: %+v", len(items), items)
	}
	if items[0].VideoID != activeVideo.ID || items[0].Video == nil || items[0].Video.Name != activeVideo.Name {
		t.Fatalf("返回候选不正确: %+v", items[0])
	}
}

func TestAITaggingStatusSummaryExcludesSoftDeletedVideos(t *testing.T) {
	setupVideoServiceTestDB(t)
	activeVideo := models.Video{Name: "active-summary.mp4", Path: "/tmp/ai-active-summary.mp4", Directory: "/tmp"}
	deletedVideo := models.Video{Name: "deleted-summary.mp4", Path: "/tmp/ai-deleted-summary.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&activeVideo).Error; err != nil {
		t.Fatalf("创建有效视频失败: %v", err)
	}
	if err := database.DB.Create(&deletedVideo).Error; err != nil {
		t.Fatalf("创建待删除视频失败: %v", err)
	}
	candidates := []models.AITagCandidate{
		{VideoID: activeVideo.ID, SuggestedName: "保留", NormalizedName: "保留", Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending},
		{VideoID: deletedVideo.ID, SuggestedName: "隐藏", NormalizedName: "隐藏", Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending},
	}
	if err := database.DB.Create(&candidates).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}
	states := []models.AITaggingState{
		{VideoID: activeVideo.ID, Status: models.AITaggingStateStatusCompleted},
		{VideoID: deletedVideo.ID, Status: models.AITaggingStateStatusCompleted},
	}
	if err := database.DB.Create(&states).Error; err != nil {
		t.Fatalf("创建状态失败: %v", err)
	}
	if err := database.DB.Delete(&deletedVideo).Error; err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}

	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	summary, err := svc.StatusSummary()
	if err != nil {
		t.Fatalf("读取状态汇总失败: %v", err)
	}
	if summary.Pending != 1 {
		t.Fatalf("待审汇总应只统计有效视频候选，实际 %d", summary.Pending)
	}
	if summary.Completed != 1 {
		t.Fatalf("完成汇总应只统计有效视频状态，实际 %d", summary.Completed)
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

func TestFindUntaggedVideosSkipsPendingCandidatesAndCompletedStates(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)

	pendingVideo := models.Video{Name: "pending.mp4", Path: "/tmp/pending.mp4", Directory: "/tmp"}
	completedVideo := models.Video{Name: "completed.mp4", Path: "/tmp/completed.mp4", Directory: "/tmp"}
	nextVideo := models.Video{Name: "next.mp4", Path: "/tmp/next.mp4", Directory: "/tmp"}
	for _, video := range []*models.Video{&pendingVideo, &completedVideo, &nextVideo} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
	}
	if err := database.DB.Create(&models.AITagCandidate{
		VideoID:        pendingVideo.ID,
		SuggestedName:  "剧情",
		NormalizedName: "剧情",
		Confidence:     models.AITagConfidenceHigh,
		Status:         models.AITagCandidateStatusPending,
	}).Error; err != nil {
		t.Fatalf("创建待审候选失败: %v", err)
	}
	if err := database.DB.Create(&models.AITaggingState{
		VideoID: completedVideo.ID,
		Status:  models.AITaggingStateStatusCompleted,
	}).Error; err != nil {
		t.Fatalf("创建已完成状态失败: %v", err)
	}

	videos, err := svc.findUntaggedVideos(10)
	if err != nil {
		t.Fatalf("查询未打标签视频失败: %v", err)
	}
	if len(videos) != 1 || videos[0].ID != nextVideo.ID {
		t.Fatalf("应只返回尚未分析的视频，实际: %#v", videos)
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
