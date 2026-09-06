package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AITaggingConnectionTestInput 是设置页「测试连接」传来的表单值。留空的字段回退到
// 已保存 / 环境变量配置，用户不必先保存再测。
type AITaggingConnectionTestInput struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

// AITaggingConnectionTestResult 不回传 API Key；BaseURL 与 Model 回显是为了让用户
// 看清这次到底测的是哪一套配置（表单值与已保存值可能不同）。
type AITaggingConnectionTestResult struct {
	OK        bool   `json:"ok"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	LatencyMS int64  `json:"latency_ms"`
	Reply     string `json:"reply"`
	Message   string `json:"message"`
}

const aiTaggingConnectionTestTimeout = 30 * time.Second

var aiTaggingHTTPStatusPattern = regexp.MustCompile(`returned (\d{3})`)

// ProbeAITaggingConnection 用一条极短的纯文本请求验证接口地址、API Key 与模型名三者能否
// 走通。它只证明"接口与模型可达"，不证明模型支持图像输入——那要等真正的打标请求才知道。
// 有了它，打标失败率高时才分得清是接口问题还是抽帧 / 扫描那一侧的问题。
func ProbeAITaggingConnection(ctx context.Context, input AITaggingConnectionTestInput) AITaggingConnectionTestResult {
	// 已保存配置缺地址或模型时 Load 会报错；这里只拿它当底子，表单值覆盖在上面。
	config, _ := SettingsAITaggingConfigProvider{}.Load()
	if value := strings.TrimSpace(input.BaseURL); value != "" {
		config.BaseURL = value
	}
	if value := strings.TrimSpace(input.APIKey); value != "" {
		config.APIKey = value
	}
	if value := strings.TrimSpace(input.Model); value != "" {
		config.Model = value
	}
	result := AITaggingConnectionTestResult{BaseURL: config.BaseURL, Model: config.Model}
	if config.BaseURL == "" || config.Model == "" {
		result.Message = "接口地址与模型不能为空"
		return result
	}
	if parsed, err := url.ParseRequestURI(config.BaseURL); err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		result.Message = "接口地址不是合法的 http(s) URL"
		return result
	}

	client := &OpenAICompatibleAITaggingClient{config: config, client: &http.Client{Timeout: aiTaggingConnectionTestTimeout}}
	ctx, cancel := context.WithTimeout(ctx, aiTaggingConnectionTestTimeout)
	defer cancel()
	started := time.Now()
	reply, err := client.doChatCompletion(ctx, 0, "connection-test", map[string]interface{}{
		"model":       config.Model,
		"messages":    []map[string]interface{}{{"role": "user", "content": "请只回复 OK"}},
		"max_tokens":  8,
		"temperature": 0,
	})
	result.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Message = describeAITaggingConnectionError(err)
		return result
	}
	result.OK = true
	result.Reply = truncateRunes(strings.TrimSpace(reply), 80)
	result.Message = fmt.Sprintf("连接正常，模型已响应（%d ms）", result.LatencyMS)
	return result
}

// describeAITaggingConnectionError 把客户端那几种错误翻成能直接动手排查的一句话。
func describeAITaggingConnectionError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("连接超时：%d 秒内没有响应，检查地址、端口或网络", int(aiTaggingConnectionTestTimeout/time.Second))
	}
	if match := aiTaggingHTTPStatusPattern.FindStringSubmatch(err.Error()); len(match) == 2 {
		status, _ := strconv.Atoi(match[1])
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return fmt.Sprintf("API Key 无效或没有权限（HTTP %d）", status)
		case status == http.StatusNotFound:
			return fmt.Sprintf("接口地址或模型不存在（HTTP %d）", status)
		case status == http.StatusTooManyRequests:
			return fmt.Sprintf("请求被限流（HTTP %d），稍后再试", status)
		case status >= 500:
			return fmt.Sprintf("服务端错误（HTTP %d）", status)
		default:
			return fmt.Sprintf("接口拒绝了请求（HTTP %d）", status)
		}
	}
	message := err.Error()
	if strings.Contains(message, "parse AI tagging API response") {
		return "返回内容不是 OpenAI 兼容的 chat/completions 格式"
	}
	if strings.Contains(message, "returned empty content") {
		return "接口已响应但没有返回内容，检查模型名是否正确"
	}
	return "无法连接：" + message
}
