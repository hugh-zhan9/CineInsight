package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// 外部视觉描述（场景检索合同「外部视觉描述（可选）」）：只在设置 scene_visual_provider
// 为 external 时由建索引任务调用。复用 AI 打标已配置的 OpenAI 兼容接口地址、Key 与模型；
// 每 8 帧一次请求，让模型为每帧输出不超过 30 字的中文描述。日志只记批量大小、状态码与
// 字节数，不记接口地址、Key、描述文本或任何路径。
const (
	sceneCaptionFramesPerRequest = 8
	sceneCaptionMaxRunes         = 30
	sceneCaptionRequestTimeout   = 5 * time.Minute
	sceneExternalModelPrefix     = "external-caption:"
	sceneExternalModelSuffix     = "@1"
	sceneExternalModelNameLimit  = 150
)

var (
	// ErrSceneExternalNotConfigured：AI 打标接口地址或模型没有配置。
	ErrSceneExternalNotConfigured = errors.New("scene_external_not_configured")
	// ErrSceneExternalNotEnabled：设置里没有显式选择外部提供方，后端拒绝外发。
	ErrSceneExternalNotEnabled = errors.New("scene_external_not_enabled")
)

const sceneCaptionSystemPrompt = "你是视频画面描述助手。你只输出 JSON 对象本身，禁止输出 Markdown、代码块围栏或任何说明。"

// SceneCaptionClient 为一批采样帧生成中文短描述；返回条数必须与输入帧数相同。
type SceneCaptionClient interface {
	CaptionFrames(ctx context.Context, jpegs [][]byte) ([]string, error)
	ModelName() string
}

// sceneCaptionClientFactory 构造外部描述客户端；默认实现读 AI 打标设置。
type sceneCaptionClientFactory func() (SceneCaptionClient, error)

// sceneExternalModelID 是外部描述写进 model_id 的标识：external-caption:<模型名>@1。
func sceneExternalModelID(model string) string {
	model = strings.TrimSpace(model)
	if runes := []rune(model); len(runes) > sceneExternalModelNameLimit {
		model = string(runes[:sceneExternalModelNameLimit])
	}
	return sceneExternalModelPrefix + model + sceneExternalModelSuffix
}

func defaultSceneCaptionClientFactory() (SceneCaptionClient, error) {
	config, err := SettingsAITaggingConfigProvider{}.Load()
	if err != nil || strings.TrimSpace(config.BaseURL) == "" || strings.TrimSpace(config.Model) == "" {
		return nil, ErrSceneExternalNotConfigured
	}
	return &openAICompatibleSceneCaptionClient{
		config: config,
		client: &http.Client{Timeout: sceneCaptionRequestTimeout},
	}, nil
}

type openAICompatibleSceneCaptionClient struct {
	config AITaggingConfig
	client *http.Client
}

func (c *openAICompatibleSceneCaptionClient) ModelName() string {
	return strings.TrimSpace(c.config.Model)
}

func buildSceneCaptionPrompt(count int) string {
	return fmt.Sprintf("下面按时间顺序给出 %d 张视频采样帧。请为每一帧写一句不超过 %d 个汉字的中文画面描述，"+
		"写清楚人物、动作、场景与显著物体，只描述画面本身。输出这样一个 JSON 对象："+
		`{"captions":["第1帧的描述","第2帧的描述"]}`+"，captions 的长度必须等于 %d。", count, sceneCaptionMaxRunes, count)
}

func (c *openAICompatibleSceneCaptionClient) CaptionFrames(ctx context.Context, jpegs [][]byte) ([]string, error) {
	if len(jpegs) == 0 || len(jpegs) > sceneCaptionFramesPerRequest {
		return nil, fmt.Errorf("scene_caption_batch_size: %d", len(jpegs))
	}
	content := []map[string]any{{"type": "text", "text": buildSceneCaptionPrompt(len(jpegs))}}
	for _, jpeg := range jpegs {
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]string{"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)},
		})
	}
	body := map[string]any{
		"model": c.config.Model,
		"messages": []map[string]any{
			{"role": "system", "content": sceneCaptionSystemPrompt},
			{"role": "user", "content": content},
		},
		"temperature": 0.1,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIChatCompletionsURL(c.config.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("scene_external_request_invalid")
	}
	request.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(c.config.APIKey); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := c.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 传输错误里带接口地址，只回固定代码。
		log.Printf("[Scene] external caption request failed frames=%d", len(jpegs))
		return nil, errors.New("scene_external_request_failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, errors.New("scene_external_request_failed")
	}
	log.Printf("[Scene] external caption response frames=%d status=%d bytes=%d", len(jpegs), response.StatusCode, len(raw))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("scene_external_http_%d", response.StatusCode)
	}
	return parseSceneCaptionResponse(raw, len(jpegs))
}

// parseSceneCaptionResponse 解析 chat completions 应答里的 {"captions":[...]}，条数必须对上；
// 每条去空白并截到 30 个字符。
func parseSceneCaptionResponse(raw []byte, want int) ([]string, error) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
		return nil, errors.New("scene_external_response_invalid")
	}
	var captions struct {
		Captions []string `json:"captions"`
	}
	if err := json.Unmarshal([]byte(normalizeAITaggingJSONContent(parsed.Choices[0].Message.Content)), &captions); err != nil {
		return nil, errors.New("scene_external_response_invalid")
	}
	if len(captions.Captions) != want {
		return nil, fmt.Errorf("scene_external_caption_count: %d/%d", len(captions.Captions), want)
	}
	out := make([]string, len(captions.Captions))
	for index, caption := range captions.Captions {
		caption = strings.Join(strings.Fields(caption), " ")
		if runes := []rune(caption); len(runes) > sceneCaptionMaxRunes {
			caption = string(runes[:sceneCaptionMaxRunes])
		}
		out[index] = caption
	}
	return out, nil
}
