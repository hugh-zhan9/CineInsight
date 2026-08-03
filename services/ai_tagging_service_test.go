package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestAutomaticTagsDoNotMakeVideoIneligibleForAITagging(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "automatic-only.mp4", Path: "/tmp/automatic-only.mp4", Duration: 30}
	automaticTag := models.Tag{Name: "短视频", Color: "#111111", AutomaticKind: shortVideoAutomaticTagKind, IsActive: true}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Create(&automaticTag).Error; err != nil {
		t.Fatalf("创建自动标签失败: %v", err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&automaticTag); err != nil {
		t.Fatalf("关联自动标签失败: %v", err)
	}

	svc := NewAITaggingService()
	videos, err := svc.findUntaggedVideos(10)
	if err != nil {
		t.Fatalf("查询待打标视频失败: %v", err)
	}
	if len(videos) != 1 || videos[0].ID != video.ID {
		t.Fatalf("只有自动标签的视频仍应进入 AI 打标: %+v", videos)
	}
	if hasNonAutomaticTags(videos[0].Tags) {
		t.Fatalf("自动标签不应被识别为人工/正式标签: %+v", videos[0].Tags)
	}
	if manual, err := svc.hasManualOfficialTagsInTx(database.DB, video.ID); err != nil || manual {
		t.Fatalf("自动标签不应使候选审批冲突: manual=%v err=%v", manual, err)
	}
}

type fakeAITaggingConfigProvider struct {
	config AITaggingConfig
	err    error
}

type switchableAITaggingConfigProvider struct {
	mu          sync.RWMutex
	config      AITaggingConfig
	err         error
	firstLoad   chan struct{}
	firstLoadMu sync.Once
}

func (p *switchableAITaggingConfigProvider) Load() (AITaggingConfig, error) {
	p.firstLoadMu.Do(func() { close(p.firstLoad) })
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config, p.err
}

func (p *switchableAITaggingConfigProvider) set(config AITaggingConfig, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config = config
	p.err = err
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
			ImagesPerRequest:  10,
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

func configuredAITag(name, color string) models.Tag {
	return models.Tag{Name: name, Color: color, Namespace: "custom", IsSystem: true, IsActive: true}
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
	if !database.DB.Migrator().HasTable(&models.AITagAgentStep{}) ||
		!database.DB.Migrator().HasTable(&models.VideoVisualFingerprint{}) ||
		!database.DB.Migrator().HasTable(&models.VideoSameSourceRelation{}) {
		t.Fatalf("期望创建 Agent 决策、视觉指纹和同源关系表")
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

	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(models.Settings{
		AITaggingBaseURL:           "http://db.example/v1",
		AITaggingAPIKey:            "db-key",
		AITaggingModel:             "db-model",
		AITaggingImagesPerRequest:  3,
		AITaggingSubtitleCharLimit: 1200,
		AITaggingStartupBatchSize:  5,
		AITaggingMaxExtraFrames:    37,
		SubtitleWhisperXModel:      "large-v3",
		SubtitleWhisperXBatchSize:  4,
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
	if config.ImagesPerRequest != 3 || config.SubtitleCharLimit != 1200 || config.StartupBatchSize != 5 || config.MaxExtraFrames != 37 {
		t.Fatalf("期望读取数据库数值配置，实际: %+v", config)
	}
	if config.SubtitleWhisperXModel != "large-v3" || config.SubtitleWhisperXBatchSize != 4 {
		t.Fatalf("临时字幕应复用用户 WhisperX 配置，实际: %+v", config)
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

func TestOpenAICompatibleClientBatchesFramesAndMergesHighestConfidence(t *testing.T) {
	requestImageCounts := make([]int, 0)
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("读取请求失败: %v", err)
		}
		requestImageCounts = append(requestImageCounts, strings.Count(string(payload), `"type":"image_url"`))
		call++
		content := `{"suggestions":[]}`
		switch call {
		case 1:
			content = `{"suggestions":[{"label":"动作","confidence":"medium","matched_existing_name":"动作"}]}`
		case 2:
			content = `{"suggestions":[{"label":"动作","confidence":"high","matched_existing_name":"动作"},{"label":"站立","confidence":"medium","matched_existing_name":"站立"}]}`
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"content": content}}},
		})
	}))
	defer srv.Close()

	frames := make([]AITaggingFrame, 23)
	for i := range frames {
		frames[i] = AITaggingFrame{Index: i + 1, Position: float64(i * 60), DataURL: "data:image/jpeg;base64,abc"}
	}
	client := NewOpenAICompatibleAITaggingClient(AITaggingConfig{
		BaseURL:          srv.URL + "/v1",
		Model:            "vision-model",
		ImagesPerRequest: 10,
	})
	suggestions, err := client.AnalyzeTags(context.Background(), AITaggingRequest{
		Video:    models.Video{ID: 1, Name: "long.mp4"},
		Evidence: AITaggingEvidence{Frames: frames},
	})
	if err != nil {
		t.Fatalf("分批分析失败: %v", err)
	}
	if fmt.Sprint(requestImageCounts) != "[10 10 3]" {
		t.Fatalf("请求图片分批不正确: %v", requestImageCounts)
	}
	if len(suggestions) != 2 || suggestions[0].Label != "动作" || suggestions[0].Confidence != "high" || suggestions[1].Label != "站立" {
		t.Fatalf("候选合并结果不正确: %+v", suggestions)
	}
	if usage := client.(AITaggingUsageReporter).AITaggingUsage(); usage.RequestCount != 3 {
		t.Fatalf("请求计数应记录实际 HTTP 调用，实际 %+v", usage)
	}
}

func TestAITaggingFramePolicyUsesOneFramePerMinuteWithMinimumTen(t *testing.T) {
	if got := planAITaggingFrameCount(45); got != 10 {
		t.Fatalf("短视频应至少 10 帧，实际 %d", got)
	}
	if got := planAITaggingFrameCount(5 * 60); got != 10 {
		t.Fatalf("5 分钟应至少 10 帧，实际 %d", got)
	}
	if got := planAITaggingFrameCount(20 * 60); got != 20 {
		t.Fatalf("20 分钟应抽 20 帧，实际 %d", got)
	}
	if got := planAITaggingFrameCount(90 * 60); got != 90 {
		t.Fatalf("90 分钟应抽 90 帧，实际 %d", got)
	}
	if got := planAITaggingFrameCount(10*60 + 1); got != 11 {
		t.Fatalf("不足整分钟的尾段也应覆盖，实际 %d", got)
	}
	positions := planAITaggingFramePositions(100, 5)
	if len(positions) != 5 {
		t.Fatalf("期望 5 个采样点，实际 %d", len(positions))
	}
	if positions[0] < 4.9 || positions[0] > 5.1 {
		t.Fatalf("首帧应避开片头约 5%%，实际 %.2f", positions[0])
	}
	if positions[len(positions)-1] < 94.9 || positions[len(positions)-1] > 95.1 {
		t.Fatalf("末帧应避开片尾约 5%%，实际 %.2f", positions[len(positions)-1])
	}
}

func TestPlanAdditionalAITaggingFramePositionsFillsLargestGaps(t *testing.T) {
	positions := planAdditionalAITaggingFramePositions(100, []float64{5, 50, 95}, 3)
	if len(positions) != 3 {
		t.Fatalf("expected 3 additional positions, got %d", len(positions))
	}
	want := []float64{16.25, 27.5, 72.5}
	for index := range want {
		if math.Abs(positions[index]-want[index]) > 0.001 {
			t.Fatalf("position %d = %.2f, want %.2f", index, positions[index], want[index])
		}
	}
}

func TestPlanAdditionalAITaggingFramePositionsHandlesEmptyAndSingle(t *testing.T) {
	if got := planAdditionalAITaggingFramePositions(10, nil, 0); got != nil {
		t.Fatalf("zero count must return nil, got %#v", got)
	}
	got := planAdditionalAITaggingFramePositions(10, nil, 1)
	if len(got) != 1 || math.Abs(got[0]-5) > 0.001 {
		t.Fatalf("single additional frame must use midpoint, got %#v", got)
	}
}

func TestClosedTagLibraryPromptAndPersistDropsOutOfSet(t *testing.T) {
	setupVideoServiceTestDB(t)
	closed := models.Tag{
		Name:      "后入",
		Color:     "#3b82f6",
		Namespace: "position",
		IsSystem:  true,
		IsActive:  true,
		SortOrder: 1,
	}
	if err := database.DB.Create(&closed).Error; err != nil {
		t.Fatalf("创建闭集标签失败: %v", err)
	}
	video := models.Video{Name: "demo.mp4", Path: "/tmp/demo-closed.mp4", Directory: "/tmp", Duration: 120}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{
		{Label: "后入", Confidence: "high", MatchType: "existing_exact", MatchedExistingName: "后入"},
		{Label: "不存在的标签", Confidence: "high", MatchType: "new_candidate"},
	}}
	svc := newTestAITaggingService(client, nil)
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("处理视频失败: %v", err)
	}
	if got := countRows(t, "ai_tag_candidates"); got != 1 {
		t.Fatalf("闭集外标签应被丢弃，候选数期望 1，实际 %d", got)
	}
	var candidate models.AITagCandidate
	if err := database.DB.First(&candidate).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if candidate.SuggestedName != "后入" {
		t.Fatalf("应保留闭集标签，实际 %q", candidate.SuggestedName)
	}

	promptClient := NewOpenAICompatibleAITaggingClient(AITaggingConfig{
		BaseURL:           "http://127.0.0.1:1234/v1",
		Model:             "vision-model",
		SubtitleCharLimit: 1000,
	}).(*OpenAICompatibleAITaggingClient)
	body := promptClient.buildRequest(AITaggingRequest{
		Video:        video,
		ExistingTags: []models.Tag{closed},
		Evidence: AITaggingEvidence{
			Frames: []AITaggingFrame{{DataURL: "data:image/jpeg;base64,abc", Index: 1, Position: 12}},
		},
	})
	messages := body["messages"].([]map[string]interface{})
	userContent := messages[1]["content"].([]map[string]interface{})
	text := userContent[0]["text"].(string)
	if !strings.Contains(text, "闭集标签库") || !strings.Contains(text, "position: 后入") {
		t.Fatalf("闭集 prompt 未包含命名空间标签库: %s", text)
	}
	if strings.Contains(text, "new_candidate") {
		t.Fatalf("闭集 prompt 不应鼓励 new_candidate")
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
	tag := configuredAITag("未知", "#fff")
	video := models.Video{Name: "quiet.mp4", Path: "/tmp/quiet.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建用户配置标签失败: %v", err)
	}
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
	tag := configuredAITag("动作", "#fff")
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
	var candidate models.AITagCandidate
	if err := database.DB.First(&candidate).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if candidate.RunID == nil {
		t.Fatalf("候选必须关联实际 AI 任务")
	}
	var run models.AITaggingRun
	if err := database.DB.First(&run, *candidate.RunID).Error; err != nil {
		t.Fatalf("读取 AI 任务失败: %v", err)
	}
	if run.Status != models.AITaggingStateStatusCompleted || run.ModelIdentifier != "test-model" || run.PromptSchemaVersion != aiTaggingPromptSchemaVersion || run.CompletedAt == nil {
		t.Fatalf("AI 任务归因不完整: %+v", run)
	}
}

func TestAITaggingPersistsMatchedExistingTagNameInsteadOfModelSynonym(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := configuredAITag("4K", "#fff")
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
	tag := configuredAITag("动作", "#fff")
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

func TestApproveAITagCandidateRejectsUnmatchedLegacyCandidate(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "mystery.mp4", Path: "/tmp/mystery.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	candidate := models.AITagCandidate{
		VideoID:        video.ID,
		SuggestedName:  "悬疑",
		NormalizedName: "悬疑",
		Confidence:     models.AITagConfidenceHigh,
		Status:         models.AITagCandidateStatusPending,
	}
	if err := database.DB.Create(&candidate).Error; err != nil {
		t.Fatalf("创建旧候选失败: %v", err)
	}
	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	if _, err := svc.ApproveCandidate(candidate.ID); err == nil {
		t.Fatal("未匹配用户标签库的旧候选不应获批")
	}
	if got := countRows(t, "video_tags"); got != 0 {
		t.Fatalf("拒绝审批后不应创建关联，实际 %d", got)
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

func TestListAITagCandidatesIncludesSoftDeletedVideoMetadata(t *testing.T) {
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
	if len(items) != 2 {
		t.Fatalf("审阅列表应保留有效和已删除视频候选，实际 %d: %+v", len(items), items)
	}
	itemsByVideoID := make(map[uint]AITaggingReviewItem, len(items))
	for _, item := range items {
		itemsByVideoID[item.VideoID] = item
	}
	activeItem := itemsByVideoID[activeVideo.ID]
	if activeItem.Video == nil || activeItem.Video.Name != activeVideo.Name || activeItem.VideoDeleted {
		t.Fatalf("有效视频候选信息不正确: %+v", activeItem)
	}
	deletedItem := itemsByVideoID[deletedVideo.ID]
	if deletedItem.Video == nil || deletedItem.Video.Name != deletedVideo.Name || !deletedItem.VideoDeleted {
		t.Fatalf("已删除视频应保留名称并标记删除状态: %+v", deletedItem)
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
	firstTag := configuredAITag("动作", "#fff")
	secondTag := configuredAITag("悬疑", "#000")
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
	tag := configuredAITag("剧情", "#fff")
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

func TestAITaggingTriggerRunsWorkerImmediatelyAfterConfigBecomesAvailable(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := configuredAITag("剧情", "#fff")
	video := models.Video{Name: "trigger.mp4", Path: "/tmp/trigger.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	provider := &switchableAITaggingConfigProvider{
		err:       fmt.Errorf("missing config"),
		firstLoad: make(chan struct{}),
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{
		Label:               tag.Name,
		Confidence:          models.AITagConfidenceHigh,
		MatchedExistingName: tag.Name,
	}}}
	svc := newTestAITaggingService(client, provider)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	defer svc.Stop()

	select {
	case <-provider.firstLoad:
	case <-time.After(time.Second):
		t.Fatal("后台任务启动后未读取初始配置")
	}
	provider.set(AITaggingConfig{
		BaseURL:           "http://127.0.0.1:9999/v1",
		APIKey:            "test-key",
		Model:             "test-model",
		ImagesPerRequest:  10,
		SubtitleCharLimit: 1000,
		StartupBatchSize:  10,
	}, nil)
	if !svc.Trigger() {
		t.Fatal("运行中的后台任务应接受立即唤醒信号")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		if err := database.DB.Model(&models.AITagCandidate{}).
			Where("video_id = ? AND status = ?", video.ID, models.AITagCandidateStatusPending).
			Count(&count).Error; err != nil {
			t.Fatalf("查询 AI 标签候选失败: %v", err)
		}
		if count == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("保存配置后的立即唤醒未在预期时间内处理视频")
}

func TestAITaggingEmptyLibraryDoesNotCallAI(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "empty-library.mp4", Path: "/tmp/empty-library.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	client := &fakeAITaggingClient{}
	svc := newTestAITaggingService(client, nil)
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatalf("空标签库应记录跳过状态而非失败: %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("空标签库不应调用 AI，实际 %d", client.calls)
	}
	var state models.AITaggingState
	if err := database.DB.Where("video_id = ?", video.ID).First(&state).Error; err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state.Status != models.AITaggingStateStatusSkipped || state.SkipReason != "empty_tag_library" {
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
	tag := configuredAITag("剧情", "#fff")
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
