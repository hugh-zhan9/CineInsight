package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

func TestTranslateSRTUsesInjectedSubtitleTranslator(t *testing.T) {
	svc := NewSubtitleService(t.TempDir())
	inputPath := filepath.Join(t.TempDir(), "input.srt")
	outputPath := filepath.Join(t.TempDir(), "output.srt")
	if err := os.WriteFile(inputPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nHello world\n\n2\n00:00:01,000 --> 00:00:02,000\nSecond line\n\n"), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}
	translator := &fakeSubtitleTranslator{result: []string{"你好，世界", "第二句"}}

	if _, err := svc.translateSRTWithProgress(context.Background(), inputPath, outputPath, "en", "zh", translator, nil); err != nil {
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

// Historical reply fixtures remain only in tests. These regressions exercise file/queue behavior,
// independently of both the retired HTTP provider and the native Apple bridge.
type subtitleFixtureTranslator struct{ endpoint string }

func (t subtitleFixtureTranslator) Translate(ctx context.Context, texts []string, source, target string) ([]string, error) {
	var lines []string
	for i, text := range texts {
		line, _ := json.Marshal(map[string]any{"index": i + 1, "text": text})
		lines = append(lines, string(line))
	}
	payload, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": strings.Join(lines, "\n")}}})
	req, err := http.NewRequestWithContext(ctx, "POST", t.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Choices []struct{ Message struct{ Content string } }
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if len(body.Choices) != 1 {
		return nil, fmt.Errorf("fixture reply missing")
	}
	var result struct {
		Translations []string `json:"translations"`
	}
	if err = json.Unmarshal([]byte(body.Choices[0].Message.Content), &result); err != nil {
		return nil, err
	}
	return result.Translations, nil
}
func newSubtitleFixtureService(t *testing.T) *SubtitleService {
	t.Helper()
	s := NewSubtitleService(t.TempDir())
	s.translatorFactory = func(config SubtitleTranslationConfig) (SubtitleTranslator, error) {
		return subtitleFixtureTranslator{endpoint: config.BaseURL}, nil
	}
	return s
}

type recordedTranslation struct{ Texts []string }
type recordingSubtitleTranslator struct {
	requests []recordedTranslation
	reply    func(recordedTranslation) ([]string, error)
}

func (t *recordingSubtitleTranslator) Translate(ctx context.Context, texts []string, source, target string) ([]string, error) {
	r := recordedTranslation{Texts: append([]string(nil), texts...)}
	t.requests = append(t.requests, r)
	return t.reply(r)
}
func echoTranslationReply(prefix string) func(recordedTranslation) ([]string, error) {
	return func(r recordedTranslation) ([]string, error) {
		result := make([]string, len(r.Texts))
		for i, text := range r.Texts {
			result[i] = prefix + text
		}
		return result, nil
	}
}
