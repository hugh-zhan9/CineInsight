package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	for _, want := range []string{"translations", "目标语言: zh", "Hello world", "Second line"} {
		if !strings.Contains(seenPrompt, want) {
			t.Fatalf("翻译提示缺少 %q: %s", want, seenPrompt)
		}
	}
	if !reflect.DeepEqual(got, []string{"你好，世界", "第二句"}) {
		t.Fatalf("翻译结果不正确: %#v", got)
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

	if err := svc.translateSRT(context.Background(), inputPath, outputPath, "en", "zh", translator); err != nil {
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
		ShortFeedMaxDurationMinutes: DefaultShortFeedMaxDurationMinutes,
		AITaggingFrameCount:         defaultAITaggingFrameCount,
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
}
