package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"video-master/models"
)

const imageAITaggingRequestTimeout = 5 * time.Minute

// 提示词契约（设计 4.6.6）：图片 AI 只产标签候选，且只能从闭合标签库里选。
// 与视频侧共用 AITagSuggestion 的解析结构，因此输出形状必须一致。
const imageAITaggingSystemPrompt = "你是本地图片库的打标助手。你只输出 JSON 对象本身，" +
	"禁止输出 Markdown、代码块围栏，禁止任何前缀或后缀说明。"

// ImageTaggingClient 把单图打标请求与传输细节解耦，便于测试注入。
type ImageTaggingClient interface {
	AnalyzeImageTags(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error)
}

// OpenAICompatibleImageTaggingClient 默认实现：chat 多模态单图请求
// （temperature 0.1、5 分钟超时、无客户端重试，重试语义由 attempt_count 承担）。
type OpenAICompatibleImageTaggingClient struct {
	config AITaggingConfig
	client *http.Client
}

// NewOpenAICompatibleImageTaggingClient 创建 OpenAI 兼容 chat completions 客户端。
func NewOpenAICompatibleImageTaggingClient(config AITaggingConfig) ImageTaggingClient {
	return &OpenAICompatibleImageTaggingClient{
		config: config,
		client: &http.Client{Timeout: imageAITaggingRequestTimeout},
	}
}

// buildImageAITaggingPrompt 构造闭合词表提示词。tags 已由调用方过滤为
// is_system AND is_active，这里复用视频侧的词表呈现（按 namespace 分组）。
func buildImageAITaggingPrompt(tags []models.Tag) string {
	library := formatClosedTagLibraryForPrompt(tags)
	var b strings.Builder
	b.WriteString("请为这张图片选择标签。\n\n")
	b.WriteString("只能从下面这份标签库里选，禁止输出标签库以外的任何标签：\n")
	b.WriteString(library)
	b.WriteString("\n\n输出这样一个 JSON 对象：\n")
	b.WriteString(`{"suggestions":[{"label":"标签库里的标签名","confidence":"high|medium|low","reasoning":"一句话依据"}]}`)
	b.WriteString("\n没有任何标签合适时输出 {\"suggestions\":[]}。")
	return b.String()
}

func (c *OpenAICompatibleImageTaggingClient) AnalyzeImageTags(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
	body := map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": imageAITaggingSystemPrompt},
			{"role": "user", "content": []map[string]interface{}{
				{"type": "text", "text": prompt},
				{"type": "image_url", "image_url": map[string]string{
					"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegData),
				}},
			}},
		},
		"temperature": 0.1,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	log.Printf("[ImageAITagging] request image_id=%d model=%q payload_bytes=%d", imageID, c.config.Model, len(payload))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIChatCompletionsURL(c.config.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(c.config.APIKey); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		log.Printf("[ImageAITagging] request failed image_id=%d", imageID)
		return nil, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	log.Printf("[ImageAITagging] response image_id=%d status=%d bytes=%d", imageID, resp.StatusCode, len(respBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("图片打标 API 返回 %d", resp.StatusCode)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("解析图片打标 API 响应失败: %w", err)
	}
	// 无 choices 视为"模型没给出任何建议"，与空 suggestions 同义，交由服务层记 completed/0 候选。
	if len(parsed.Choices) == 0 {
		return nil, nil
	}
	return parseAITagSuggestions(parsed.Choices[0].Message.Content)
}
