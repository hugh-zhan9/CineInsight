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
	"video-master/models"
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
	batches := splitAITaggingFrames(req.Evidence.Frames, c.config.ImagesPerRequest)
	merged := make([]AITagSuggestion, 0)
	positionsByKey := make(map[string]int)
	for i, frames := range batches {
		batchReq := req
		batchReq.Evidence.Frames = frames
		batchReq.BatchIndex = i + 1
		batchReq.BatchCount = len(batches)
		batchReq.TotalFrames = len(req.Evidence.Frames)
		suggestions, err := c.analyzeBatch(ctx, batchReq)
		if err != nil {
			return nil, fmt.Errorf("AI tagging batch %d/%d: %w", i+1, len(batches), err)
		}
		merged = mergeAITagSuggestions(merged, positionsByKey, suggestions)
	}
	return merged, nil
}

func (c *OpenAICompatibleAITaggingClient) analyzeBatch(ctx context.Context, req AITaggingRequest) ([]AITagSuggestion, error) {
	body := c.buildRequest(req)
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	log.Printf("[AITagging] request video_id=%d batch=%d/%d model=%q base_url=%q payload_bytes=%d payload=%s",
		req.Video.ID,
		req.BatchIndex,
		req.BatchCount,
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

func splitAITaggingFrames(frames []AITaggingFrame, limit int) [][]AITaggingFrame {
	if len(frames) == 0 {
		return [][]AITaggingFrame{nil}
	}
	if limit <= 0 {
		limit = defaultAITaggingImagesPerRequest
	}
	batches := make([][]AITaggingFrame, 0, (len(frames)+limit-1)/limit)
	for start := 0; start < len(frames); start += limit {
		end := start + limit
		if end > len(frames) {
			end = len(frames)
		}
		batches = append(batches, frames[start:end])
	}
	return batches
}

func mergeAITagSuggestions(merged []AITagSuggestion, positionsByKey map[string]int, incoming []AITagSuggestion) []AITagSuggestion {
	for _, suggestion := range incoming {
		key := normalizeAITagName(suggestion.MatchedExistingName)
		if key == "" {
			key = normalizeAITagName(suggestion.Label)
		}
		if key == "" {
			continue
		}
		if position, exists := positionsByKey[key]; exists {
			if aiConfidenceRank(suggestion.Confidence) > aiConfidenceRank(merged[position].Confidence) {
				merged[position] = suggestion
			}
			continue
		}
		positionsByKey[key] = len(merged)
		merged = append(merged, suggestion)
	}
	return merged
}

func aiConfidenceRank(confidence string) int {
	switch normalizeAIConfidence(confidence) {
	case models.AITagConfidenceHigh:
		return 3
	case models.AITagConfidenceMedium:
		return 2
	case models.AITagConfidenceLow:
		return 1
	default:
		return 0
	}
}

func (c *OpenAICompatibleAITaggingClient) buildRequest(req AITaggingRequest) map[string]interface{} {
	evidence := req.Evidence
	totalFrames := req.TotalFrames
	if totalFrames <= 0 {
		totalFrames = len(evidence.Frames)
	}
	frameContents := make([]map[string]interface{}, 0, len(evidence.Frames)+1)
	text := buildAITaggingPromptText(req, c.config.SubtitleCharLimit)
	frameContents = append(frameContents, map[string]interface{}{"type": "text", "text": text})
	for _, frame := range evidence.Frames {
		frameContents = append(frameContents, map[string]interface{}{
			"type": "text",
			"text": fmt.Sprintf("视频抽帧 %d/%d，约 %.1f 秒。请把这张图与当前批次其他抽帧综合比较，不要单独依赖文件名。", frame.Index, totalFrames, frame.Position),
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

func buildAITaggingPromptText(req AITaggingRequest, subtitleCharLimit int) string {
	evidence := req.Evidence
	batchContext := ""
	if req.BatchCount > 1 {
		batchContext = fmt.Sprintf("这是第 %d/%d 批画面，本视频共抽取 %d 帧。请只判断当前批次可见的内容；服务端会合并各批结果。\n", req.BatchIndex, req.BatchCount, req.TotalFrames)
	}
	closedLibrary := formatClosedTagLibraryForPrompt(req.ExistingTags)
	if closedLibrary != "" {
		return fmt.Sprintf(`请为本地视频生成标签候选。当前请求包含 %d 张视频抽帧；如果抽帧可用，必须优先根据画面内容判断，文件名和路径只能作为辅助证据。
%s

你只能从下列闭集标签库中选择，禁止输出候选集之外的标签，禁止同义改写。

闭集标签库：
%s

输出 JSON，格式为 {"suggestions":[{"label":"标签名","confidence":"high|medium|low","match_type":"existing_exact|existing_semantic","matched_existing_name":"必须填写闭集中的原始名称","reasoning":"简短理由"}]}。

规则：
1. label 与 matched_existing_name 都必须原样使用闭集中的标签名称。
2. 只标注画面中实际出现的内容；不确定就不要输出。
3. 文件名/路径/字幕仅作辅助，不得仅因标题给 high。
4. 不得对闭集中的标签名称做同义改写、扩写或缩写。

置信度：
- high: 多帧稳定出现或画面与文件名共同强确认
- medium: 画面证据较强但不够稳定
- low: 依据不足（服务端会丢弃）

视频文件名：%s
视频路径：%s
字幕摘要：%s
采样警告：%s`, len(evidence.Frames), batchContext, closedLibrary, req.Video.Name, req.Video.Path, truncateLogSnippet(evidence.SubtitleText, subtitleCharLimit), strings.Join(evidence.Warnings, "; "))
	}

	existingTagNames := make([]string, 0, len(req.ExistingTags))
	for _, tag := range req.ExistingTags {
		existingTagNames = append(existingTagNames, tag.Name)
	}
	return fmt.Sprintf(`请为本地视频生成标签候选。当前请求包含 %d 张视频抽帧；如果抽帧可用，必须优先根据画面内容判断，文件名和路径只能作为辅助证据。必须优先从现有标签库中选择，只有画面证据非常明确且现有标签库没有合适标签时，才提出新标签。
%s

输出 JSON，格式为 {"suggestions":[{"label":"标签名","confidence":"high|medium|low","match_type":"existing_exact|existing_semantic|new_candidate","matched_existing_name":"若匹配已有标签则填写","reasoning":"简短理由"}]}。

证据优先级：
1. 视频抽帧中的稳定视觉内容优先，尤其是跨多帧重复出现的主体、场景、服装、画质、拍摄方式。
2. 已有标签库优先。能映射到已有标签时，label 必须使用已有标签的原始名称，matched_existing_name 也填写该已有标签名称。
3. 文件名、路径、字幕只能用于补充画面判断；不得只因为标题包含某个词就给 high。
4. 如果画面不可用，再退化为文件名、路径、字幕和已有标签库判断，并在 reasoning 里说明依据不足。
5. 不要为已有标签创建同义、扩写或缩写的新标签。

置信度规则：
- high: 多帧画面证据明确，且能匹配已有标签，或文件名和画面共同强确认。
- medium: 画面证据较强但不是多帧稳定出现，或能语义匹配已有标签但不够直接。
- low: 主要来自标题/路径、画面证据不足，或与现有标签库风格差别大。

视频文件名：%s
视频路径：%s
现有标签库：%s
字幕摘要：%s
采样警告：%s`, len(evidence.Frames), batchContext, req.Video.Name, req.Video.Path, strings.Join(existingTagNames, ", "), truncateLogSnippet(evidence.SubtitleText, subtitleCharLimit), strings.Join(evidence.Warnings, "; "))
}

func formatClosedTagLibraryForPrompt(tags []models.Tag) string {
	if len(tags) == 0 {
		return ""
	}
	grouped := map[string][]string{}
	order := make([]string, 0)
	systemCount := 0
	for _, tag := range tags {
		if !tag.IsSystem || !tag.IsActive {
			continue
		}
		systemCount++
		ns := strings.TrimSpace(tag.Namespace)
		if ns == "" {
			ns = "other"
		}
		if _, ok := grouped[ns]; !ok {
			order = append(order, ns)
		}
		grouped[ns] = append(grouped[ns], tag.Name)
	}
	if systemCount == 0 {
		return ""
	}
	var b strings.Builder
	for _, ns := range order {
		names := grouped[ns]
		if len(names) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(ns)
		b.WriteString(": ")
		b.WriteString(strings.Join(names, " / "))
	}
	return b.String()
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
