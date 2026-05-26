package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type SubtitleTranslationProvider string

const (
	SubtitleTranslationProviderDeepL SubtitleTranslationProvider = "deepl"
	SubtitleTranslationProviderLLM   SubtitleTranslationProvider = "llm"
)

type SubtitleTranslationConfig struct {
	Provider    string
	DeepLAPIKey string
	BaseURL     string
	APIKey      string
	Model       string
}

type SubtitleTranslator interface {
	Translate(ctx context.Context, texts []string, sourceLang, targetLang string) ([]string, error)
}

const subtitleTranslationRequestTimeout = 5 * time.Minute

type deepLSubtitleTranslator struct {
	service *SubtitleService
	apiKey  string
}

func newDeepLSubtitleTranslator(service *SubtitleService, apiKey string) SubtitleTranslator {
	return &deepLSubtitleTranslator{
		service: service,
		apiKey:  strings.TrimSpace(apiKey),
	}
}

func (t *deepLSubtitleTranslator) Translate(ctx context.Context, texts []string, sourceLang, targetLang string) ([]string, error) {
	if t.service == nil {
		return nil, fmt.Errorf("subtitle service unavailable")
	}
	if strings.TrimSpace(t.apiKey) == "" {
		return nil, fmt.Errorf("DeepL API Key 未配置")
	}
	return t.service.translateDeepL(ctx, texts, sourceLang, targetLang, t.apiKey)
}

type OpenAICompatibleSubtitleTranslator struct {
	config SubtitleTranslationConfig
	client *http.Client
}

func NewOpenAICompatibleSubtitleTranslator(config SubtitleTranslationConfig) SubtitleTranslator {
	config.Provider = string(normalizeSubtitleTranslationProvider(config.Provider))
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.Model = strings.TrimSpace(config.Model)
	return &OpenAICompatibleSubtitleTranslator{
		config: config,
		client: &http.Client{Timeout: subtitleTranslationRequestTimeout},
	}
}

func (s *SubtitleService) subtitleTranslator(provider SubtitleTranslationProvider, config SubtitleTranslationConfig) (SubtitleTranslator, error) {
	switch provider {
	case SubtitleTranslationProviderLLM:
		if strings.TrimSpace(config.BaseURL) == "" || strings.TrimSpace(config.Model) == "" {
			return nil, fmt.Errorf("LLM 字幕翻译需要配置接口地址和模型")
		}
		config.Provider = string(SubtitleTranslationProviderLLM)
		return NewOpenAICompatibleSubtitleTranslator(config), nil
	default:
		if strings.TrimSpace(config.DeepLAPIKey) == "" {
			return nil, fmt.Errorf("DeepL API Key 未配置")
		}
		return newDeepLSubtitleTranslator(s, config.DeepLAPIKey), nil
	}
}

func subtitleTranslationProgressMessage(provider SubtitleTranslationProvider) string {
	if provider == SubtitleTranslationProviderLLM {
		return "通过 LLM API 翻译字幕..."
	}
	return "通过 DeepL 翻译字幕..."
}

func (c *OpenAICompatibleSubtitleTranslator) Translate(ctx context.Context, texts []string, sourceLang, targetLang string) ([]string, error) {
	if len(texts) == 0 {
		return []string{}, nil
	}
	if strings.TrimSpace(c.config.BaseURL) == "" {
		return nil, fmt.Errorf("subtitle translation base url is required")
	}
	if strings.TrimSpace(c.config.Model) == "" {
		return nil, fmt.Errorf("subtitle translation model is required")
	}

	body := c.buildRequest(texts, sourceLang, targetLang)
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	url := openAIChatCompletionsURL(c.config.BaseURL)
	log.Printf("[Subtitle] llm translation request model=%q url=%q lines=%d payload_bytes=%d",
		c.config.Model,
		url,
		len(texts),
		len(payload),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := strings.TrimSpace(c.config.APIKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		log.Printf("[Subtitle] llm translation request failed err=%v", err)
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("[Subtitle] llm translation response status=%d bytes=%d",
		resp.StatusCode,
		len(respBody),
	)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("subtitle translation API returned %d: %s", resp.StatusCode, truncateLogSnippet(string(respBody), 300))
	}

	content, err := parseOpenAIChatCompletionContent(respBody)
	if err != nil {
		return nil, err
	}
	translations, err := parseSubtitleTranslations(content)
	if err != nil {
		return nil, err
	}
	if len(translations) != len(texts) {
		return nil, fmt.Errorf("subtitle translation returned %d items for %d subtitle lines", len(translations), len(texts))
	}
	return translations, nil
}

func (c *OpenAICompatibleSubtitleTranslator) buildRequest(texts []string, sourceLang, targetLang string) map[string]interface{} {
	return map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": "你是字幕翻译助手。你只能输出 JSON，不要输出 Markdown。",
			},
			{
				"role":    "user",
				"content": buildSubtitleTranslationPrompt(texts, sourceLang, targetLang),
			},
		},
		"temperature": 0.1,
	}
}

func buildSubtitleTranslationPrompt(texts []string, sourceLang, targetLang string) string {
	var builder strings.Builder
	builder.WriteString("请逐条翻译以下字幕，只输出严格 JSON，格式为 {\"translations\":[\"...\"]}。\n")
	builder.WriteString("目标语言: ")
	builder.WriteString(strings.TrimSpace(targetLang))
	builder.WriteString("\n")
	if trimmed := strings.TrimSpace(sourceLang); trimmed != "" {
		builder.WriteString("源语言: ")
		builder.WriteString(trimmed)
		builder.WriteString("\n")
	}
	builder.WriteString("要求：保持条目数量与顺序一致，不要添加解释、序号或 Markdown。\n")
	builder.WriteString("字幕条目:\n")
	for i, text := range texts {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, text)
	}
	return builder.String()
}

func parseOpenAIChatCompletionContent(respBody []byte) (string, error) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("subtitle translation API returned empty choices")
	}
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("subtitle translation API returned empty content")
	}
	return content, nil
}

func parseSubtitleTranslations(content string) ([]string, error) {
	content = normalizeAITaggingJSONContent(content)

	var wrapped struct {
		Translations []string `json:"translations"`
	}
	if err := json.Unmarshal([]byte(content), &wrapped); err == nil && wrapped.Translations != nil {
		return wrapped.Translations, nil
	}

	var direct []string
	if err := json.Unmarshal([]byte(content), &direct); err == nil {
		return direct, nil
	}

	return nil, fmt.Errorf("subtitle translation response did not contain translations")
}

func normalizeSubtitleTranslationProvider(value string) SubtitleTranslationProvider {
	switch SubtitleTranslationProvider(stringsTrimLower(value)) {
	case SubtitleTranslationProviderLLM:
		return SubtitleTranslationProviderLLM
	default:
		return SubtitleTranslationProviderDeepL
	}
}

func normalizeSubtitleLanguageCode(value string) string {
	switch stringsTrimLower(value) {
	case "chinese":
		return "zh"
	case "english":
		return "en"
	case "japanese":
		return "ja"
	case "korean":
		return "ko"
	case "french":
		return "fr"
	case "german":
		return "de"
	case "spanish":
		return "es"
	case "portuguese":
		return "pt"
	case "russian":
		return "ru"
	case "italian":
		return "it"
	default:
		return strings.TrimSpace(strings.ToLower(value))
	}
}
