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

// ContextPair 是滑动窗口里的一条只读上文：上一批的原文与它的译文（D-035）。
type ContextPair struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// TranslationRequest 是带术语表与上文的翻译请求。返回条数校验只看 Texts，
// PrecedingContext 明确不计入返回（D-035）。
type TranslationRequest struct {
	Texts            []string
	SourceLang       string
	TargetLang       string
	Glossary         []GlossaryTerm
	PrecedingContext []ContextPair
}

// ContextualTranslator 是 SubtitleTranslator 的可选扩展：只有 OpenAI 兼容翻译器
// 实现它，DeepL 保持旧接口与旧请求体不变（D-034）。调用方用类型断言判定。
type ContextualTranslator interface {
	TranslateWithContext(ctx context.Context, request TranslationRequest) ([]string, error)
}

type subtitleTranslationItem struct {
	Index       int    `json:"index"`
	Text        string `json:"text"`
	Translation string `json:"translation"`
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
	return c.TranslateWithContext(ctx, TranslationRequest{Texts: texts, SourceLang: sourceLang, TargetLang: targetLang})
}

func (c *OpenAICompatibleSubtitleTranslator) TranslateWithContext(ctx context.Context, request TranslationRequest) ([]string, error) {
	texts := request.Texts
	if len(texts) == 0 {
		return []string{}, nil
	}
	if strings.TrimSpace(c.config.BaseURL) == "" {
		return nil, fmt.Errorf("subtitle translation base url is required")
	}
	if strings.TrimSpace(c.config.Model) == "" {
		return nil, fmt.Errorf("subtitle translation model is required")
	}

	body := c.buildRequest(request)
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	url := openAIChatCompletionsURL(c.config.BaseURL)
	log.Printf("[Subtitle] llm translation request model=%q url=%q lines=%d glossary_terms=%d context_pairs=%d payload_bytes=%d",
		c.config.Model,
		url,
		len(texts),
		len(request.Glossary),
		len(request.PrecedingContext),
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

func (c *OpenAICompatibleSubtitleTranslator) buildRequest(request TranslationRequest) map[string]interface{} {
	return map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": "你是字幕翻译助手。你只能输出 JSON，不要输出 Markdown。",
			},
			{
				"role":    "user",
				"content": buildSubtitleTranslationPrompt(request),
			},
		},
		"temperature": 0.1,
	}
}

// matchedGlossaryTerms 只留命中本批原文的术语（大小写不敏感子串匹配）：一张几千条的
// 表全量注入会挤掉字幕本身的预算，而模型也用不上没出现过的词（D-034）。
func matchedGlossaryTerms(glossary []GlossaryTerm, texts []string) []GlossaryTerm {
	if len(glossary) == 0 || len(texts) == 0 {
		return nil
	}
	lowered := make([]string, 0, len(texts))
	for _, text := range texts {
		lowered = append(lowered, strings.ToLower(text))
	}
	matched := make([]GlossaryTerm, 0, len(glossary))
	for _, term := range glossary {
		needle := strings.ToLower(strings.TrimSpace(term.SourceTerm))
		if needle == "" {
			continue
		}
		for _, text := range lowered {
			if strings.Contains(text, needle) {
				matched = append(matched, term)
				break
			}
		}
	}
	return matched
}

type glossaryPromptLine struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Note   string `json:"note,omitempty"`
}

type contextPromptLine struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

func buildSubtitleTranslationPrompt(request TranslationRequest) string {
	var builder strings.Builder
	builder.WriteString("请逐条翻译以下字幕，只输出严格 JSON，格式为 {\"translations\":[{\"index\":1,\"text\":\"...\"}]}。\n")
	builder.WriteString("目标语言: ")
	builder.WriteString(strings.TrimSpace(request.TargetLang))
	builder.WriteString("\n")
	if trimmed := strings.TrimSpace(request.SourceLang); trimmed != "" {
		builder.WriteString("源语言: ")
		builder.WriteString(trimmed)
		builder.WriteString("\n")
	}
	builder.WriteString("要求：保持条目数量、顺序和原有换行；结合相邻条目的上下文；人名、片名、专有名词在不确定时保留原文；不要添加解释、序号或 Markdown。\n")
	if matched := matchedGlossaryTerms(request.Glossary, request.Texts); len(matched) > 0 {
		builder.WriteString("术语表（必须遵循，JSON Lines，每行包含 source / target，可能带 note）:\n")
		for _, term := range matched {
			payload, _ := json.Marshal(glossaryPromptLine{Source: term.SourceTerm, Target: term.TargetTerm, Note: term.Note})
			builder.Write(payload)
			builder.WriteString("\n")
		}
	}
	if len(request.PrecedingContext) > 0 {
		builder.WriteString("上文（只读，勿翻译，勿计入返回；JSON Lines，每行包含 source / target）:\n")
		for _, pair := range request.PrecedingContext {
			payload, _ := json.Marshal(contextPromptLine{Source: pair.Source, Target: pair.Target})
			builder.Write(payload)
			builder.WriteString("\n")
		}
	}
	builder.WriteString("字幕条目（JSON Lines，每行包含 index 和 text）:\n")
	for i, text := range request.Texts {
		payload, _ := json.Marshal(map[string]interface{}{
			"index": i + 1,
			"text":  text,
		})
		builder.Write(payload)
		builder.WriteString("\n")
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
		Translations json.RawMessage `json:"translations"`
	}
	if err := json.Unmarshal([]byte(content), &wrapped); err == nil && len(wrapped.Translations) > 0 && string(wrapped.Translations) != "null" {
		return parseSubtitleTranslationItems(wrapped.Translations)
	}

	return parseSubtitleTranslationItems([]byte(content))
}

func parseSubtitleTranslationItems(raw []byte) ([]string, error) {
	var direct []string
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, nil
	}

	var indexed []subtitleTranslationItem
	if err := json.Unmarshal(raw, &indexed); err == nil && indexed != nil {
		return subtitleTranslationsFromIndexedItems(indexed)
	}

	return nil, fmt.Errorf("subtitle translation response did not contain translations")
}

func subtitleTranslationsFromIndexedItems(items []subtitleTranslationItem) ([]string, error) {
	if len(items) == 0 {
		return []string{}, nil
	}

	hasIndex := false
	for _, item := range items {
		if item.Index > 0 {
			hasIndex = true
			break
		}
	}
	if !hasIndex {
		translations := make([]string, 0, len(items))
		for _, item := range items {
			translations = append(translations, subtitleTranslationItemText(item.Text, item.Translation))
		}
		return translations, nil
	}

	translations := make([]string, len(items))
	seen := make(map[int]struct{}, len(items))
	for _, item := range items {
		if item.Index < 1 || item.Index > len(items) {
			return nil, fmt.Errorf("subtitle translation index %d out of range 1..%d", item.Index, len(items))
		}
		if _, ok := seen[item.Index]; ok {
			return nil, fmt.Errorf("subtitle translation duplicate index %d", item.Index)
		}
		seen[item.Index] = struct{}{}
		translations[item.Index-1] = subtitleTranslationItemText(item.Text, item.Translation)
	}
	if len(seen) != len(items) {
		return nil, fmt.Errorf("subtitle translation indexes are incomplete")
	}
	return translations, nil
}

func subtitleTranslationItemText(text, fallback string) string {
	if text != "" {
		return text
	}
	return fallback
}

func normalizeSubtitleTranslationProvider(value string) SubtitleTranslationProvider {
	switch SubtitleTranslationProvider(strings.ToLower(strings.TrimSpace(value))) {
	case SubtitleTranslationProviderLLM:
		return SubtitleTranslationProviderLLM
	default:
		return SubtitleTranslationProviderDeepL
	}
}

func normalizeSubtitleLanguageCode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
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

// subtitleTranslationContextWindow 是滑动窗口保留的上文条数：前一批尾部 5 条（D-035）。
const subtitleTranslationContextWindow = 5

// translateSubtitleBatch 走可选的 ContextualTranslator，拿不到就退回旧接口。
// DeepL 只实现旧接口，因此它的请求体不会被术语表与上文影响（D-034）。
func translateSubtitleBatch(ctx context.Context, translator SubtitleTranslator, contextual ContextualTranslator, request TranslationRequest) ([]string, error) {
	if contextual != nil {
		return contextual.TranslateWithContext(ctx, request)
	}
	return translator.Translate(ctx, request.Texts, request.SourceLang, request.TargetLang)
}

// trailingContextPairs 取本批尾部 limit 条 (原文, 译文) 作为下一批的只读上文。
func trailingContextPairs(texts, translations []string, limit int) []ContextPair {
	if limit <= 0 || len(texts) == 0 || len(translations) != len(texts) {
		return nil
	}
	start := len(texts) - limit
	if start < 0 {
		start = 0
	}
	pairs := make([]ContextPair, 0, len(texts)-start)
	for index := start; index < len(texts); index++ {
		pairs = append(pairs, ContextPair{Source: texts[index], Target: translations[index]})
	}
	return pairs
}
