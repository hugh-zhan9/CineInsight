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
	"video-master/models"
)

type AITaggingAIClient interface {
	AnalyzeTags(ctx context.Context, req AITaggingRequest) ([]AITagSuggestion, error)
}

type AITaggingDecisionClient interface {
	DecideNextAction(ctx context.Context, req AITagAgentDecisionRequest) (AITagAgentDecision, error)
}

type AITaggingSameSourceClient interface {
	CompareSameSource(ctx context.Context, req AISameSourceComparisonRequest) (AISameSourceComparison, error)
}

type OpenAICompatibleAITaggingClient struct {
	config AITaggingConfig
	client *http.Client
}

const aiTaggingRequestTimeout = 5 * time.Minute

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
	content, err := c.doChatCompletion(ctx, req.Video.ID, "tag_analysis", body)
	if err != nil {
		return nil, err
	}
	suggestions, err := parseAITagSuggestions(content)
	if err != nil {
		log.Printf("[AITagging] response content parse failed video_id=%d operation=tag_analysis err=%v", req.Video.ID, err)
		return nil, err
	}
	log.Printf("[AITagging] parsed suggestions video_id=%d count=%d", req.Video.ID, len(suggestions))
	return suggestions, nil
}

func (c *OpenAICompatibleAITaggingClient) DecideNextAction(ctx context.Context, req AITagAgentDecisionRequest) (AITagAgentDecision, error) {
	body := c.buildDecisionRequest(req)
	content, err := c.doChatCompletion(ctx, req.Video.ID, "agent_decision", body)
	if err != nil {
		return AITagAgentDecision{}, err
	}
	return parseAITagAgentDecision(content)
}

func (c *OpenAICompatibleAITaggingClient) CompareSameSource(ctx context.Context, req AISameSourceComparisonRequest) (AISameSourceComparison, error) {
	body := c.buildSameSourceRequest(req)
	content, err := c.doChatCompletion(ctx, req.Video.ID, "same_source_compare", body)
	if err != nil {
		return AISameSourceComparison{}, err
	}
	content = normalizeAITaggingJSONContent(content)
	var comparison AISameSourceComparison
	if err := json.Unmarshal([]byte(content), &comparison); err != nil {
		return comparison, fmt.Errorf("parse same-source comparison: %w", err)
	}
	comparison.Confidence = normalizeAIConfidence(comparison.Confidence)
	if comparison.Confidence == "" {
		comparison.Confidence = models.AITagConfidenceLow
	}
	comparison.Reasoning = strings.TrimSpace(comparison.Reasoning)
	return comparison, nil
}

func (c *OpenAICompatibleAITaggingClient) doChatCompletion(ctx context.Context, videoID uint, operation string, body map[string]interface{}) (string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	log.Printf("[AITagging] request video_id=%d operation=%s model=%q base_url=%q payload_bytes=%d",
		videoID, operation, c.config.Model, openAIChatCompletionsURL(c.config.BaseURL), len(payload))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIChatCompletionsURL(c.config.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(c.config.APIKey) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		log.Printf("[AITagging] request failed video_id=%d operation=%s err=%v", videoID, operation, err)
		return "", err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return "", readErr
	}
	log.Printf("[AITagging] response video_id=%d operation=%s status=%d bytes=%d", videoID, operation, resp.StatusCode, len(respBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("AI tagging API returned %d", resp.StatusCode)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parse AI tagging API response: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("AI tagging API returned empty content")
	}
	return parsed.Choices[0].Message.Content, nil
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

func (c *OpenAICompatibleAITaggingClient) buildDecisionRequest(req AITagAgentDecisionRequest) map[string]interface{} {
	frameLimit := c.config.ImagesPerRequest
	if frameLimit <= 0 {
		frameLimit = defaultAITaggingImagesPerRequest
	}
	frames := selectRepresentativeAITaggingFrames(req.Evidence.Frames, frameLimit)
	observations, _ := json.Marshal(req.Observations)
	properties, _ := json.Marshal(req.Evidence.AdditionalProperties)
	content := make([]map[string]interface{}, 0, len(frames)*2+1)
	content = append(content, map[string]interface{}{
		"type": "text",
		"text": fmt.Sprintf(`你正在决定视频标签分析的下一步。每轮只能选择一个动作。

可选动作：
- finalize：现有证据足够，进入最终标签判断。
- request_more_frames：需要更多画面；requested_frame_count 必须是正整数且不超过剩余额度。
- request_transcript：需要本地临时转写补充语义证据。
- find_same_source：需要查找清晰度变化、重编码或空间裁剪后的同源视频。

规则：
1. 当前是第 %d/%d 轮；最后一轮必须 finalize。
2. 临时字幕和同源查找各最多执行一次；已执行后不要再次请求。
3. 工具失败只是观察；应根据剩余证据继续决策。
4. 同源结果只作为标签证据，不能直接当作正式标签。
5. 只输出 JSON：{"action":"finalize|request_more_frames|request_transcript|find_same_source","requested_frame_count":0,"reasoning":"简短理由"}。

视频名：%s
时长：%.2f 秒
当前画面数：%d
额外帧剩余额度：%d
已有字幕：%t
临时字幕已请求：%t
同源查找已请求：%t
字幕摘要：%s
同源证据：%s
工具观察：%s`,
			req.Round, req.MaxRounds, req.Video.Name, req.Video.Duration, len(req.Evidence.Frames), req.RemainingExtraFrames,
			strings.TrimSpace(req.Evidence.SubtitleText) != "", req.TranscriptUsed, req.SameSourceUsed,
			truncateRunes(req.Evidence.SubtitleText, c.config.SubtitleCharLimit), string(properties), string(observations)),
	})
	for _, frame := range frames {
		content = append(content,
			map[string]interface{}{"type": "text", "text": fmt.Sprintf("现有证据帧，约 %.1f 秒。", frame.Position)},
			map[string]interface{}{"type": "image_url", "image_url": map[string]string{"url": frame.DataURL}},
		)
	}
	return map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": "你是视频分析 Agent 的决策器。只输出严格 JSON，不输出 Markdown。"},
			{"role": "user", "content": content},
		},
		"temperature": 0.1,
	}
}

func (c *OpenAICompatibleAITaggingClient) buildSameSourceRequest(req AISameSourceComparisonRequest) map[string]interface{} {
	count := minInt(len(req.Left), len(req.Right))
	if count > 5 {
		count = 5
	}
	content := make([]map[string]interface{}, 0, count*4+1)
	content = append(content, map[string]interface{}{
		"type": "text",
		"text": fmt.Sprintf(`判断两个本地视频是否来自同一段原始内容。允许清晰度下降、重新编码、加边框和空间裁剪；仅有相似主题、同一人物或同一场景不算同源。
只有证据明确时才返回 high。只输出 JSON：{"same_source":true|false,"confidence":"high|medium|low","reasoning":"简短理由"}。
视频 A：%s，时长 %.2f 秒
视频 B：%s，时长 %.2f 秒`, req.Video.Name, req.Video.Duration, req.Candidate.Name, req.Candidate.Duration),
	})
	for index := 0; index < count; index++ {
		content = append(content,
			map[string]interface{}{"type": "text", "text": fmt.Sprintf("对应采样点 %d，视频 A。", index+1)},
			map[string]interface{}{"type": "image_url", "image_url": map[string]string{"url": req.Left[index].DataURL}},
			map[string]interface{}{"type": "text", "text": fmt.Sprintf("对应采样点 %d，视频 B。", index+1)},
			map[string]interface{}{"type": "image_url", "image_url": map[string]string{"url": req.Right[index].DataURL}},
		)
	}
	return map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": "你是严格的视频同源比对器，只输出 JSON。"},
			{"role": "user", "content": content},
		},
		"temperature": 0,
	}
}

func selectRepresentativeAITaggingFrames(frames []AITaggingFrame, limit int) []AITaggingFrame {
	if limit <= 0 || len(frames) <= limit {
		return append([]AITaggingFrame(nil), frames...)
	}
	if limit == 1 {
		return []AITaggingFrame{frames[len(frames)/2]}
	}
	selected := make([]AITaggingFrame, 0, limit)
	for index := 0; index < limit; index++ {
		position := index * (len(frames) - 1) / (limit - 1)
		selected = append(selected, frames[position])
	}
	return selected
}

func parseAITagAgentDecision(content string) (AITagAgentDecision, error) {
	content = normalizeAITaggingJSONContent(content)
	var decision AITagAgentDecision
	if err := json.Unmarshal([]byte(content), &decision); err != nil {
		return decision, fmt.Errorf("parse AI agent decision: %w", err)
	}
	decision.Action = strings.TrimSpace(decision.Action)
	if decision.Action == "" {
		decision.Action = models.AITagAgentActionFinalize
	}
	switch decision.Action {
	case models.AITagAgentActionFinalize,
		models.AITagAgentActionMoreFrames,
		models.AITagAgentActionTranscript,
		models.AITagAgentActionFindSameSource:
	default:
		return decision, fmt.Errorf("unsupported AI agent action %q", decision.Action)
	}
	decision.Reasoning = strings.TrimSpace(decision.Reasoning)
	return decision, nil
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func buildAITaggingPromptText(req AITaggingRequest, subtitleCharLimit int) string {
	evidence := req.Evidence
	agentEvidenceJSON, _ := json.Marshal(evidence.AdditionalProperties)
	agentEvidence := string(agentEvidenceJSON)
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
Agent 补充证据：%s
采样警告：%s`, len(evidence.Frames), batchContext, closedLibrary, req.Video.Name, req.Video.Path, truncateLogSnippet(evidence.SubtitleText, subtitleCharLimit), agentEvidence, strings.Join(evidence.Warnings, "; "))
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
Agent 补充证据：%s
采样警告：%s`, len(evidence.Frames), batchContext, req.Video.Name, req.Video.Path, strings.Join(existingTagNames, ", "), truncateLogSnippet(evidence.SubtitleText, subtitleCharLimit), agentEvidence, strings.Join(evidence.Warnings, "; "))
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
