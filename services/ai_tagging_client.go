package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func NewOpenAICompatibleAITaggingClient(config AITaggingConfig) AITaggingAIClient {
	return &OpenAICompatibleAITaggingClient{
		config: config,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *OpenAICompatibleAITaggingClient) AnalyzeTags(ctx context.Context, req AITaggingRequest) ([]AITagSuggestion, error) {
	body := c.buildRequest(req)
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIChatCompletionsURL(c.config.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
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
		return nil, err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return nil, fmt.Errorf("AI tagging API returned empty content")
	}
	return parseAITagSuggestions(parsed.Choices[0].Message.Content)
}

func (c *OpenAICompatibleAITaggingClient) buildRequest(req AITaggingRequest) map[string]interface{} {
	existingTagNames := make([]string, 0, len(req.ExistingTags))
	for _, tag := range req.ExistingTags {
		existingTagNames = append(existingTagNames, tag.Name)
	}
	evidence := req.Evidence
	frameContents := make([]map[string]interface{}, 0, len(evidence.Frames)+1)
	text := fmt.Sprintf(`请为本地视频生成标签候选。必须优先从现有标签库中选择，只有非常确定才提出新标签。

输出 JSON，格式为 {"suggestions":[{"label":"标签名","confidence":"high|medium|low","match_type":"existing_exact|existing_semantic|new_candidate","matched_existing_name":"若匹配已有标签则填写","reasoning":"简短理由"}]}。

置信度规则：
- high: 与现有标签直接一致或高度符合现有标签风格。
- medium: 与现有标签语义类似但不完全一致。
- low: 与现有标签库风格差别大或证据不足。

视频文件名：%s
视频路径：%s
现有标签库：%s
字幕摘要：%s
采样警告：%s`, req.Video.Name, req.Video.Path, strings.Join(existingTagNames, ", "), truncateLogSnippet(evidence.SubtitleText, c.config.SubtitleCharLimit), strings.Join(evidence.Warnings, "; "))
	frameContents = append(frameContents, map[string]interface{}{"type": "text", "text": text})
	for _, frame := range evidence.Frames {
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
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0.1,
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

func parseAITagSuggestions(content string) ([]AITagSuggestion, error) {
	content = strings.TrimSpace(content)
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
