package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

type fakeSubtitleTranslator struct {
	texts      []string
	sourceLang string
	targetLang string
	result     []string
}

func (t *fakeSubtitleTranslator) Translate(ctx context.Context, texts []string, sourceLang, targetLang string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.texts = append([]string(nil), texts...)
	t.sourceLang = sourceLang
	t.targetLang = targetLang
	return append([]string(nil), t.result...), nil
}

func TestOpenAICompatibleSubtitleTranslatorUsesChatCompletionsAndAllowsLocalEndpointWithoutAPIKey(t *testing.T) {
	var seenAuth string
	var seenPath string
	var seenModel string
	var seenPrompt string
	var hasResponseFormat bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("解析请求体失败: %v", err)
		}
		if model, ok := body["model"].(string); ok {
			seenModel = model
		}
		_, hasResponseFormat = body["response_format"]
		payload, _ := json.Marshal(body["messages"])
		seenPrompt = string(payload)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": `{"translations":["你好，世界","第二句"]}`}},
			},
		})
	}))
	defer srv.Close()

	translator := NewOpenAICompatibleSubtitleTranslator(SubtitleTranslationConfig{
		BaseURL: srv.URL,
		Model:   "local-qwen",
	})
	got, err := translator.Translate(context.Background(), []string{"Hello world", "Second line"}, "en", "zh")
	if err != nil {
		t.Fatalf("LLM 字幕翻译失败: %v", err)
	}
	if seenPath != "/v1/chat/completions" {
		t.Fatalf("应请求 OpenAI-compatible chat completions，实际 path=%q", seenPath)
	}
	if seenAuth != "" {
		t.Fatalf("本地 API Key 为空时不应发送 Authorization，实际 %q", seenAuth)
	}
	if seenModel != "local-qwen" {
		t.Fatalf("模型名不正确: %q", seenModel)
	}
	if hasResponseFormat {
		t.Fatalf("不应发送本地兼容端点可能不支持的 response_format 字段")
	}
	for _, want := range []string{"translations", "\\\"index\\\":1", "目标语言: zh", "Hello world", "Second line"} {
		if !strings.Contains(seenPrompt, want) {
			t.Fatalf("翻译提示缺少 %q: %s", want, seenPrompt)
		}
	}
	if !reflect.DeepEqual(got, []string{"你好，世界", "第二句"}) {
		t.Fatalf("翻译结果不正确: %#v", got)
	}
}

func TestOpenAICompatibleSubtitleTranslatorSendsRemoteAPIKey(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": `{"translations":["你好"]}`}},
			},
		})
	}))
	defer srv.Close()

	translator := NewOpenAICompatibleSubtitleTranslator(SubtitleTranslationConfig{
		BaseURL: srv.URL,
		APIKey:  "subtitle-remote-key",
		Model:   "remote-model",
	})
	if _, err := translator.Translate(context.Background(), []string{"Hello"}, "en", "zh"); err != nil {
		t.Fatalf("远程 LLM 字幕翻译失败: %v", err)
	}
	if seenAuth != "Bearer subtitle-remote-key" {
		t.Fatalf("远程字幕翻译未使用独立 API Key，实际 Authorization=%q", seenAuth)
	}
}

func TestParseSubtitleTranslationsAcceptsIndexedObjects(t *testing.T) {
	got, err := parseSubtitleTranslations(`{"translations":[{"index":2,"text":"第二句"},{"index":1,"text":"第一句"}]}`)
	if err != nil {
		t.Fatalf("解析带 index 的翻译结果失败: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"第一句", "第二句"}) {
		t.Fatalf("翻译结果未按 index 对齐: %#v", got)
	}
}

func TestTranslateSRTUsesInjectedSubtitleTranslator(t *testing.T) {
	svc := NewSubtitleService(t.TempDir())
	inputPath := filepath.Join(t.TempDir(), "input.srt")
	outputPath := filepath.Join(t.TempDir(), "output.srt")
	if err := os.WriteFile(inputPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nHello world\n\n2\n00:00:01,000 --> 00:00:02,000\nSecond line\n\n"), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}
	translator := &fakeSubtitleTranslator{result: []string{"你好，世界", "第二句"}}

	if err := svc.translateSRT(context.Background(), inputPath, outputPath, "en", "zh", translator, nil); err != nil {
		t.Fatalf("翻译 SRT 失败: %v", err)
	}

	if !reflect.DeepEqual(translator.texts, []string{"Hello world", "Second line"}) {
		t.Fatalf("传给 translator 的文本不正确: %#v", translator.texts)
	}
	if translator.sourceLang != "en" || translator.targetLang != "zh" {
		t.Fatalf("语言参数不正确 source=%q target=%q", translator.sourceLang, translator.targetLang)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("读取翻译字幕失败: %v", err)
	}
	got := string(output)
	for _, want := range []string{
		"1\n00:00:00,000 --> 00:00:01,000\n你好，世界",
		"2\n00:00:01,000 --> 00:00:02,000\n第二句",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("翻译 SRT 缺少 %q，实际:\n%s", want, got)
		}
	}
}

func TestSettingsServicePersistsSubtitleTranslationLLMConfig(t *testing.T) {
	setupVideoServiceTestDB(t)
	input := models.Settings{
		VideoExtensions:             ".mp4",
		PlayWeight:                  2.0,
		BilingualEnabled:            true,
		BilingualLang:               "zh",
		SubtitleTranslationProvider: string(SubtitleTranslationProviderLLM),
		SubtitleTranslationBaseURL:  "http://127.0.0.1:1234/v1",
		SubtitleTranslationAPIKey:   "",
		SubtitleTranslationModel:    "qwen2.5-7b-instruct",
		SubtitleWhisperXModel:       "large-v3",
		SubtitleWhisperXBatchSize:   4,
		ShortFeedMaxDurationMinutes: DefaultShortFeedMaxDurationMinutes,
		AITaggingFrameCount:         0,
		AITaggingSubtitleCharLimit:  defaultAITaggingSubtitleCharLimit,
		AITaggingStartupBatchSize:   defaultAITaggingStartupBatchSize,
	}

	if err := (&SettingsService{}).UpdateSettings(input); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	var got models.Settings
	if err := database.DB.First(&got).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if got.SubtitleTranslationProvider != string(SubtitleTranslationProviderLLM) {
		t.Fatalf("字幕翻译 provider 未保存: %+v", got)
	}
	if got.SubtitleTranslationBaseURL != "http://127.0.0.1:1234/v1" || got.SubtitleTranslationAPIKey != "" || got.SubtitleTranslationModel != "qwen2.5-7b-instruct" {
		t.Fatalf("字幕翻译 LLM 配置未正确保存: %+v", got)
	}
	if got.SubtitleWhisperXModel != "large-v3" || got.SubtitleWhisperXBatchSize != 4 {
		t.Fatalf("字幕识别质量配置未正确保存: %+v", got)
	}
}

func TestSettingsServiceNormalizesSubtitleWhisperXQualityConfig(t *testing.T) {
	setupVideoServiceTestDB(t)
	input := models.Settings{
		VideoExtensions:             ".mp4",
		PlayWeight:                  2.0,
		SubtitleWhisperXModel:       "too-large",
		SubtitleWhisperXBatchSize:   99,
		ShortFeedMaxDurationMinutes: DefaultShortFeedMaxDurationMinutes,
		AITaggingFrameCount:         0,
		AITaggingSubtitleCharLimit:  defaultAITaggingSubtitleCharLimit,
		AITaggingStartupBatchSize:   defaultAITaggingStartupBatchSize,
	}

	if err := (&SettingsService{}).UpdateSettings(input); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	var got models.Settings
	if err := database.DB.First(&got).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if got.SubtitleWhisperXModel != defaultSubtitleWhisperXModel {
		t.Fatalf("无效模型应回退默认值，got=%q", got.SubtitleWhisperXModel)
	}
	if got.SubtitleWhisperXBatchSize != maxSubtitleWhisperXBatchSize {
		t.Fatalf("过大 batch size 应被钳制，got=%d", got.SubtitleWhisperXBatchSize)
	}
}

// ===== P-010 基线：DeepL 请求体快照 =====
//
// 术语表与滑动窗口只注入 OpenAI 兼容翻译器（D-034），DeepL 路径必须逐字节不变。
// 这条测试在 P-010 动任何翻译代码之前写就，快照落在基线目录，之后所有改动都要它保持绿。

type capturedHTTPRequest struct {
	label         string
	method        string
	url           string
	contentLength int64
	headers       []string
	body          string
}

type recordingRoundTripper struct {
	label     string
	responses func() (string, error)
	captured  []capturedHTTPRequest
}

func (rt *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body := []byte{}
	if req.Body != nil {
		read, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		body = read
	}
	headers := make([]string, 0, len(req.Header))
	for name := range req.Header {
		value := req.Header.Get(name)
		if name == "Authorization" {
			// API Key 本身不进快照，只保留鉴权方案前缀。
			value = strings.SplitN(value, " ", 2)[0] + " <redacted>"
		}
		headers = append(headers, name+": "+value)
	}
	sort.Strings(headers)
	rt.captured = append(rt.captured, capturedHTTPRequest{
		label:         rt.label,
		method:        req.Method,
		url:           req.URL.String(),
		contentLength: req.ContentLength,
		headers:       headers,
		body:          string(body),
	})
	payload, err := rt.responses()
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}, nil
}

func renderDeepLRequestSnapshot(captured []capturedHTTPRequest) string {
	var builder strings.Builder
	for _, request := range captured {
		builder.WriteString("case: " + request.label + "\n")
		builder.WriteString("method: " + request.method + "\n")
		builder.WriteString("url: " + request.url + "\n")
		builder.WriteString(fmt.Sprintf("content_length: %d\n", request.contentLength))
		for _, header := range request.headers {
			builder.WriteString("header: " + header + "\n")
		}
		builder.WriteString("body: " + request.body + "\n")
		builder.WriteString("\n")
	}
	return builder.String()
}

func TestDeepLSubtitleTranslatorRequestMatchesBaselineSnapshot(t *testing.T) {
	recorder := &recordingRoundTripper{}
	original := deeplHTTPClient
	deeplHTTPClient = &http.Client{Transport: recorder}
	t.Cleanup(func() { deeplHTTPClient = original })

	service := NewSubtitleService(t.TempDir())

	cases := []struct {
		label      string
		apiKey     string
		texts      []string
		sourceLang string
		targetLang string
		reply      string
	}{
		{
			label:      "free key, source en, target zh",
			apiKey:     "test-free-key:fx",
			texts:      []string{"Hello world", "Second line"},
			sourceLang: "en",
			targetLang: "zh",
			reply:      `{"translations":[{"text":"你好，世界"},{"text":"第二句"}]}`,
		},
		{
			label:      "pro key, no source lang, target en",
			apiKey:     "test-pro-key",
			texts:      []string{"你好，世界"},
			sourceLang: "",
			targetLang: "en",
			reply:      `{"translations":[{"text":"Hello world"}]}`,
		},
	}

	for _, testCase := range cases {
		recorder.label = testCase.label
		reply := testCase.reply
		recorder.responses = func() (string, error) { return reply, nil }
		translator := newDeepLSubtitleTranslator(service, testCase.apiKey)
		if _, err := translator.Translate(context.Background(), testCase.texts, testCase.sourceLang, testCase.targetLang); err != nil {
			t.Fatalf("DeepL 翻译失败 case=%s: %v", testCase.label, err)
		}
	}

	got := renderDeepLRequestSnapshot(recorder.captured)
	baselinePath := filepath.Join("..", ".loopx", "workspace", "2026-09-02-capability-batch", "baseline", "deepl-request-before-P-010.txt")
	want, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("读取 DeepL 请求体基线快照失败: %v\n实际快照:\n%s", err, got)
	}
	if got != string(want) {
		t.Fatalf("DeepL 请求体与基线快照不一致。\n期望:\n%s\n实际:\n%s", string(want), got)
	}
}

// ===== P-010 术语表与滑动窗口 =====

type recordingContextualTranslator struct {
	requests []TranslationRequest
	reply    func(TranslationRequest) ([]string, error)
}

func (t *recordingContextualTranslator) Translate(ctx context.Context, texts []string, sourceLang, targetLang string) ([]string, error) {
	return t.TranslateWithContext(ctx, TranslationRequest{Texts: texts, SourceLang: sourceLang, TargetLang: targetLang})
}

func (t *recordingContextualTranslator) TranslateWithContext(ctx context.Context, request TranslationRequest) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.requests = append(t.requests, request)
	return t.reply(request)
}

func echoTranslationReply(prefix string) func(TranslationRequest) ([]string, error) {
	return func(request TranslationRequest) ([]string, error) {
		translations := make([]string, 0, len(request.Texts))
		for _, text := range request.Texts {
			translations = append(translations, prefix+text)
		}
		return translations, nil
	}
}

// startPromptCapturingLLMServer 起一个 OpenAI 兼容桩，捕获每次请求的用户提示词。
func startPromptCapturingLLMServer(t *testing.T, reply func(prompt string) string) (*httptest.Server, *[]string) {
	t.Helper()
	prompts := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解析请求体失败: %v", err)
			return
		}
		prompt := ""
		for _, message := range body.Messages {
			if message.Role == "user" {
				prompt = message.Content
			}
		}
		*prompts = append(*prompts, prompt)
		_, _ = w.Write([]byte(reply(prompt)))
	}))
	t.Cleanup(server.Close)
	return server, prompts
}

func TestOpenAITranslatorInjectsOnlyMatchedGlossaryTermsAndReadOnlyContext(t *testing.T) {
	server, prompts := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"尼奥来了\",\"第二句\"]}"}}]}`
	})
	translator := NewOpenAICompatibleSubtitleTranslator(SubtitleTranslationConfig{BaseURL: server.URL, Model: "local-qwen"})
	contextual, ok := translator.(ContextualTranslator)
	if !ok {
		t.Fatalf("OpenAI 兼容翻译器应当实现 ContextualTranslator")
	}

	got, err := contextual.TranslateWithContext(context.Background(), TranslationRequest{
		Texts:      []string{"NEO arrives", "Second line"},
		SourceLang: "en",
		TargetLang: "zh",
		Glossary: []GlossaryTerm{
			{SourceTerm: "Neo", TargetTerm: "尼奥", Note: "主角"},
			{SourceTerm: "Trinity", TargetTerm: "崔妮蒂"},
		},
		PrecedingContext: []ContextPair{{Source: "Wake up", Target: "醒醒"}},
	})
	if err != nil {
		t.Fatalf("带上下文翻译失败: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"尼奥来了", "第二句"}) {
		t.Fatalf("翻译结果不正确: %#v", got)
	}
	if len(*prompts) != 1 {
		t.Fatalf("应当只发一次请求: %d", len(*prompts))
	}
	prompt := (*prompts)[0]
	for _, want := range []string{
		"术语表（必须遵循",
		`{"source":"Neo","target":"尼奥","note":"主角"}`,
		"上文（只读，勿翻译，勿计入返回",
		`{"source":"Wake up","target":"醒醒"}`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("提示词缺少 %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Trinity") || strings.Contains(prompt, "崔妮蒂") {
		t.Fatalf("未命中本批原文的术语不应注入:\n%s", prompt)
	}
}

func TestOpenAITranslatorOmitsGlossaryAndContextBlocksWhenEmpty(t *testing.T) {
	server, prompts := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"你好\"]}"}}]}`
	})
	translator := NewOpenAICompatibleSubtitleTranslator(SubtitleTranslationConfig{BaseURL: server.URL, Model: "local-qwen"})
	if _, err := translator.Translate(context.Background(), []string{"Hello"}, "en", "zh"); err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	prompt := (*prompts)[0]
	if strings.Contains(prompt, "术语表") || strings.Contains(prompt, "上文（只读") {
		t.Fatalf("没有术语与上文时不应出现这两段:\n%s", prompt)
	}
}

func TestOpenAITranslatorCountValidationCountsOnlyCurrentBatch(t *testing.T) {
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		// 模型把上文也翻回来了：3 条 = 本批 2 条 + 上文 1 条。
		return `{"choices":[{"message":{"content":"{\"translations\":[\"醒醒\",\"尼奥来了\",\"第二句\"]}"}}]}`
	})
	translator := NewOpenAICompatibleSubtitleTranslator(SubtitleTranslationConfig{BaseURL: server.URL, Model: "local-qwen"})
	contextual := translator.(ContextualTranslator)
	_, err := contextual.TranslateWithContext(context.Background(), TranslationRequest{
		Texts:            []string{"NEO arrives", "Second line"},
		TargetLang:       "zh",
		PrecedingContext: []ContextPair{{Source: "Wake up", Target: "醒醒"}},
	})
	if err == nil {
		t.Fatalf("返回条数应当按本批 2 条校验，多出的上文条目必须报错")
	}
	if !strings.Contains(err.Error(), "returned 3 items for 2 subtitle lines") {
		t.Fatalf("条数校验错误信息不正确: %v", err)
	}
}

func TestDeepLTranslatorDoesNotImplementContextualTranslator(t *testing.T) {
	translator := newDeepLSubtitleTranslator(NewSubtitleService(t.TempDir()), "test-key:fx")
	if _, ok := translator.(ContextualTranslator); ok {
		t.Fatalf("DeepL 翻译器不得实现 ContextualTranslator，否则请求体会被术语表改变")
	}
}

func writeNumberedSRT(t *testing.T, path string, count int) []string {
	t.Helper()
	var builder strings.Builder
	texts := make([]string, 0, count)
	for index := 1; index <= count; index++ {
		text := fmt.Sprintf("Line %d about NEO", index)
		texts = append(texts, text)
		builder.WriteString(fmt.Sprintf("%d\n00:00:%02d,000 --> 00:00:%02d,000\n%s\n\n", index, index-1, index, text))
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}
	return texts
}

func assertSlidingWindow(t *testing.T, requests []TranslationRequest, texts []string, prefix string) {
	t.Helper()
	if len(requests) != 2 {
		t.Fatalf("60 条应当按 50 条一批发两次，实际 %d 次", len(requests))
	}
	if len(requests[0].Texts) != 50 || len(requests[1].Texts) != 10 {
		t.Fatalf("批大小不应改变: %d / %d", len(requests[0].Texts), len(requests[1].Texts))
	}
	if len(requests[0].PrecedingContext) != 0 {
		t.Fatalf("首批不应带上文: %+v", requests[0].PrecedingContext)
	}
	if len(requests[1].PrecedingContext) != 5 {
		t.Fatalf("第二批应当带前一批尾部 5 条上文，实际 %d 条", len(requests[1].PrecedingContext))
	}
	for offset, pair := range requests[1].PrecedingContext {
		wantSource := texts[45+offset]
		if pair.Source != wantSource || pair.Target != prefix+wantSource {
			t.Fatalf("上文第 %d 条不是前一批尾部对应项: %+v", offset, pair)
		}
	}
}

func TestTranslateSRTKeepsGlossaryAndSlidesContextWindowAcrossBatches(t *testing.T) {
	service := NewSubtitleService(t.TempDir())
	inputPath := filepath.Join(t.TempDir(), "input.srt")
	outputPath := filepath.Join(t.TempDir(), "output.srt")
	texts := writeNumberedSRT(t, inputPath, 60)
	translator := &recordingContextualTranslator{reply: echoTranslationReply("译:")}
	glossary := []GlossaryTerm{{SourceTerm: "Neo", TargetTerm: "尼奥"}}

	if err := service.translateSRT(context.Background(), inputPath, outputPath, "en", "zh", translator, glossary); err != nil {
		t.Fatalf("翻译 SRT 失败: %v", err)
	}

	assertSlidingWindow(t, translator.requests, texts, "译:")
	for index, request := range translator.requests {
		if !reflect.DeepEqual(request.Glossary, glossary) {
			t.Fatalf("第 %d 批未带上术语生效集: %+v", index, request.Glossary)
		}
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("读取翻译字幕失败: %v", err)
	}
	if !strings.Contains(string(output), "译:Line 60 about NEO") {
		t.Fatalf("翻译结果未落盘:\n%s", string(output))
	}
}

func TestTranslateSRTFallsBackToPlainTranslatorWithoutContextualSupport(t *testing.T) {
	service := NewSubtitleService(t.TempDir())
	inputPath := filepath.Join(t.TempDir(), "input.srt")
	outputPath := filepath.Join(t.TempDir(), "output.srt")
	if err := os.WriteFile(inputPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nHello world\n\n"), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}
	translator := &fakeSubtitleTranslator{result: []string{"你好，世界"}}

	if err := service.translateSRT(context.Background(), inputPath, outputPath, "en", "zh", translator, []GlossaryTerm{{SourceTerm: "Hello", TargetTerm: "你好"}}); err != nil {
		t.Fatalf("翻译 SRT 失败: %v", err)
	}
	if !reflect.DeepEqual(translator.texts, []string{"Hello world"}) {
		t.Fatalf("旧接口翻译器应当照旧只收到文本: %#v", translator.texts)
	}
}

func TestFinalizeSubtitleArtifactResolvesGlossaryForVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, prompts := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"尼奥来了\"]}"}}]}`
	})

	collection := mustCreateGlossaryCollection(t, "黑客帝国")
	video := mustCreateGlossaryVideo(t, filepath.Join(t.TempDir(), "matrix.mp4"))
	mustLinkGlossaryCollectionVideo(t, collection.ID, video.ID, 1)
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "全局尼奥"})
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{CollectionID: collectionScope(collection.ID), SourceTerm: "Neo", TargetTerm: "作品集尼奥"})

	service := NewSubtitleService(t.TempDir())
	srtPath := filepath.Join(t.TempDir(), "matrix.srt")
	if err := os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nNEO arrives\n\n"), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}

	result, err := service.finalizeSubtitleArtifact(context.Background(), 1,
		SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX},
		srtPath, "en", SubtitleGenerateOptions{
			BilingualEnabled:  true,
			BilingualLang:     "zh",
			TranslationConfig: SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"},
		})
	if err != nil {
		t.Fatalf("双语收尾失败: %v", err)
	}
	if result.TranslationStatus != "translated" {
		t.Fatalf("双语翻译未成功: %+v", result)
	}
	if len(*prompts) != 1 {
		t.Fatalf("应当只发一次翻译请求: %d", len(*prompts))
	}
	prompt := (*prompts)[0]
	if !strings.Contains(prompt, `{"source":"Neo","target":"作品集尼奥"}`) {
		t.Fatalf("字幕生成路径未注入作品集级术语:\n%s", prompt)
	}
	if strings.Contains(prompt, "全局尼奥") {
		t.Fatalf("同源词的全局条目应被作品集级覆盖:\n%s", prompt)
	}
}

func TestFinalizeSubtitleArtifactSkipsGlossaryForDeepL(t *testing.T) {
	setupVideoServiceTestDB(t)
	recorder := &recordingRoundTripper{
		label:     "deepl bilingual finalize",
		responses: func() (string, error) { return `{"translations":[{"text":"尼奥来了"}]}`, nil },
	}
	original := deeplHTTPClient
	deeplHTTPClient = &http.Client{Transport: recorder}
	t.Cleanup(func() { deeplHTTPClient = original })

	video := mustCreateGlossaryVideo(t, filepath.Join(t.TempDir(), "matrix.mp4"))
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "尼奥"})

	service := NewSubtitleService(t.TempDir())
	resolved := 0
	service.glossaryResolver = func(uint) ([]GlossaryTerm, error) {
		resolved++
		return nil, nil
	}
	srtPath := filepath.Join(t.TempDir(), "matrix.srt")
	if err := os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nNEO arrives\n\n"), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}

	result, err := service.finalizeSubtitleArtifact(context.Background(), 1,
		SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX},
		srtPath, "en", SubtitleGenerateOptions{
			BilingualEnabled:  true,
			BilingualLang:     "zh",
			TranslationConfig: SubtitleTranslationConfig{Provider: "deepl", DeepLAPIKey: "test-free-key:fx"},
		})
	if err != nil {
		t.Fatalf("双语收尾失败: %v", err)
	}
	if result.TranslationStatus != "translated" {
		t.Fatalf("DeepL 双语翻译未成功: %+v", result)
	}
	if resolved != 0 {
		t.Fatalf("DeepL 用不上术语表，不应因此多读一次库（%d 次）", resolved)
	}
	if len(recorder.captured) != 1 {
		t.Fatalf("应当只发一次 DeepL 请求: %d", len(recorder.captured))
	}
	if body := recorder.captured[0].body; body != `{"text":["NEO arrives"],"target_lang":"ZH-HANS","source_lang":"EN"}` {
		t.Fatalf("DeepL 请求体被改变了: %s", body)
	}
}

func TestTrailingContextPairsHandlesShortAndMismatchedBatches(t *testing.T) {
	texts := []string{"a", "b", "c"}
	translations := []string{"甲", "乙", "丙"}

	got := trailingContextPairs(texts, translations, subtitleTranslationContextWindow)
	want := []ContextPair{{Source: "a", Target: "甲"}, {Source: "b", Target: "乙"}, {Source: "c", Target: "丙"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("不足 5 条时应当全部作为上文: %#v", got)
	}
	if pairs := trailingContextPairs(texts, []string{"甲"}, subtitleTranslationContextWindow); pairs != nil {
		t.Fatalf("条数不匹配时不应产出上文: %#v", pairs)
	}
	if pairs := trailingContextPairs(nil, nil, subtitleTranslationContextWindow); pairs != nil {
		t.Fatalf("空批不应产出上文: %#v", pairs)
	}
	last := trailingContextPairs([]string{"a", "b", "c", "d", "e", "f"}, []string{"1", "2", "3", "4", "5", "6"}, subtitleTranslationContextWindow)
	if len(last) != 5 || last[0].Source != "b" || last[4].Source != "f" {
		t.Fatalf("超过 5 条时只取尾部 5 条: %#v", last)
	}
}
