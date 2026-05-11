package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type AITaggingAIClient interface {
	AnalyzeTags(ctx context.Context, req AITaggingRequest) ([]AITagSuggestion, error)
}

type OpenAICompatibleAITaggingClient struct {
	config AITaggingConfig
	client *http.Client
}

const aiTaggingRequestTimeout = 5 * time.Minute

var aiTaggingDataURLPattern = regexp.MustCompile(`"url":"data:image/[^"]+"`)

func NewOpenAICompatibleAITaggingClient(config AITaggingConfig) AITaggingAIClient {
	return &OpenAICompatibleAITaggingClient{
		config: config,
		client: &http.Client{Timeout: aiTaggingRequestTimeout},
	}
}

func (c *OpenAICompatibleAITaggingClient) AnalyzeTags(ctx context.Context, req AITaggingRequest) ([]AITagSuggestion, error) {
	body := c.buildRequest(req)
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	log.Printf("[AITagging] request video_id=%d model=%q base_url=%q payload_bytes=%d payload=%s",
		req.Video.ID,
		c.config.Model,
		openAIChatCompletionsURL(c.config.BaseURL),
		len(payload),
		redactAITaggingPayload(payload),
	)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIChatCompletionsURL(c.config.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(c.config.APIKey) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		log.Printf("[AITagging] request failed video_id=%d err=%v", req.Video.ID, err)
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("[AITagging] response video_id=%d status=%d bytes=%d body=%s",
		req.Video.ID,
		resp.StatusCode,
		len(respBody),
		truncateLogSnippet(string(respBody), 4000),
	)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("AI tagging API returned %d: %s", resp.StatusCode, truncateLogSnippet(string(respBody), 300))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		log.Printf("[AITagging] response parse failed video_id=%d err=%v body=%s", req.Video.ID, err, truncateLogSnippet(string(respBody), 4000))
		return nil, err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		log.Printf("[AITagging] response empty content video_id=%d body=%s", req.Video.ID, truncateLogSnippet(string(respBody), 4000))
		return nil, fmt.Errorf("AI tagging API returned empty content")
	}
	content := parsed.Choices[0].Message.Content
	suggestions, err := parseAITagSuggestions(content)
	if err != nil {
		log.Printf("[AITagging] response content parse failed video_id=%d err=%v content=%s", req.Video.ID, err, truncateLogSnippet(content, 4000))
		return nil, err
	}
	log.Printf("[AITagging] parsed suggestions video_id=%d count=%d suggestions=%s",
		req.Video.ID,
		len(suggestions),
		summarizeAITagSuggestions(suggestions),
	)
	return suggestions, nil
}

func (c *OpenAICompatibleAITaggingClient) buildRequest(req AITaggingRequest) map[string]interface{} {
	existingTagNames := make([]string, 0, len(req.ExistingTags))
	for _, tag := range req.ExistingTags {
		existingTagNames = append(existingTagNames, tag.Name)
	}
	evidence := req.Evidence
	frameContents := make([]map[string]interface{}, 0, len(evidence.Frames)+1)
	text := fmt.Sprintf(`请为本地视频生成标签候选。当前请求包含 %d 张视频抽帧；如果抽帧可用，必须优先根据画面内容判断，文件名和路径只能作为辅助证据。必须优先从现有标签库中选择，只有画面证据非常明确且现有标签库没有合适标签时，才提出新标签。

输出 JSON，格式为 {"suggestions":[{"label":"标签名","confidence":"high|medium|low","match_type":"existing_exact|existing_semantic|new_candidate","matched_existing_name":"若匹配已有标签则填写","reasoning":"简短理由"}]}。

证据优先级：
1. 视频抽帧中的稳定视觉内容优先，尤其是跨多帧重复出现的主体、场景、服装、画质、拍摄方式。
2. 已有标签库优先。能映射到已有标签时，label 必须使用已有标签的原始名称，matched_existing_name 也填写该已有标签名称。
3. 文件名、路径、字幕只能用于补充画面判断；不得只因为标题包含某个词就给 high。
4. 如果画面不可用，再退化为文件名、路径、字幕和已有标签库判断，并在 reasoning 里说明依据不足。
5. 同义词不要新增标签。例如已有 "4K" 时，不要输出 "4K超清"；已有 "舞蹈" 时，不要输出 "舞蹈表演"。

置信度规则：
- high: 多帧画面证据明确，且能匹配已有标签，或文件名和画面共同强确认。
- medium: 画面证据较强但不是多帧稳定出现，或能语义匹配已有标签但不够直接。
- low: 主要来自标题/路径、画面证据不足，或与现有标签库风格差别大。

视频文件名：%s
视频路径：%s
现有标签库：%s
字幕摘要：%s
采样警告：%s`, len(evidence.Frames), req.Video.Name, req.Video.Path, strings.Join(existingTagNames, ", "), truncateLogSnippet(evidence.SubtitleText, c.config.SubtitleCharLimit), strings.Join(evidence.Warnings, "; "))
	frameContents = append(frameContents, map[string]interface{}{"type": "text", "text": text})
	for _, frame := range evidence.Frames {
		frameContents = append(frameContents, map[string]interface{}{
			"type": "text",
			"text": fmt.Sprintf("视频抽帧 %d/%d，约 %.1f 秒。请把这张图与其他抽帧综合比较，不要单独依赖文件名。", frame.Index, len(evidence.Frames), frame.Position),
		})
		frameContents = append(frameContents, map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]string{
				"url": frame.DataURL,
			},
		})
	}
	return map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": "你是视频库标签审核助手。你只能输出 JSON，不要输出 Markdown。"},
			{"role": "user", "content": frameContents},
		},
		"temperature": 0.1,
	}
}

func openAIChatCompletionsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

func redactAITaggingPayload(payload []byte) string {
	redacted := aiTaggingDataURLPattern.ReplaceAllString(string(payload), `"url":"<data_url_redacted>"`)
	return truncateLogSnippet(redacted, 4000)
}

func summarizeAITagSuggestions(suggestions []AITagSuggestion) string {
	if len(suggestions) == 0 {
		return "[]"
	}
	limit := len(suggestions)
	if limit > 8 {
		limit = 8
	}
	parts := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		s := suggestions[i]
		label := strings.TrimSpace(s.Label)
		if label == "" {
			label = "<empty>"
		}
		parts = append(parts, fmt.Sprintf("{label:%q confidence:%q match_type:%q matched:%q}", label, s.Confidence, s.MatchType, s.MatchedExistingName))
	}
	if len(suggestions) > limit {
		parts = append(parts, fmt.Sprintf("...+%d more", len(suggestions)-limit))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func parseAITagSuggestions(content string) ([]AITagSuggestion, error) {
	content = strings.TrimSpace(content)
	content = normalizeAITaggingJSONContent(content)
	var wrapped struct {
		Suggestions []AITagSuggestion `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(content), &wrapped); err == nil && wrapped.Suggestions != nil {
		return wrapped.Suggestions, nil
	}
	var direct []AITagSuggestion
	if err := json.Unmarshal([]byte(content), &direct); err != nil {
		return nil, err
	}
	return direct, nil
}

func normalizeAITaggingJSONContent(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		lines := strings.Split(content, "\n")
		if len(lines) >= 2 {
			if strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
				lines = lines[1:]
			}
			if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				lines = lines[:len(lines)-1]
			}
			content = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	if strings.HasPrefix(content, "{") || strings.HasPrefix(content, "[") {
		return content
	}
	if extracted, ok := extractFirstJSONValue(content); ok {
		return extracted
	}
	return content
}

func extractFirstJSONValue(content string) (string, bool) {
	for start, r := range content {
		var close rune
		switch r {
		case '{':
			close = '}'
		case '[':
			close = ']'
		default:
			continue
		}
		if end, ok := findJSONValueEnd(content[start:], r, close); ok {
			return strings.TrimSpace(content[start : start+end+1]), true
		}
	}
	return "", false
}

func findJSONValueEnd(content string, open rune, close rune) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i, r := range content {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch r {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}
