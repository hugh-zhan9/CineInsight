package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
)

const TextAIQueryPurpose = "query_parse"
const TextAIChapterPurpose = "chapter_summary"

var ErrTextAIDisabled = errors.New("text_ai_disabled: 此文本用途尚未启用")
var ErrTextAIConfigChanged = errors.New("text_ai_config_changed: 服务配置已变化，请重新核对发送范围")
var ErrTextAIUnavailable = errors.New("text_ai_unavailable: 请先配置可用的文本模型地址和模型")
var ErrTextAIFailed = errors.New("text_ai_failed: 文本模型调用失败，请检查服务配置")

type TextAIStatus struct {
	QueryEnabled     bool   `json:"query_enabled"`
	SummaryEnabled   bool   `json:"summary_enabled"`
	Available        bool   `json:"available"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	ProviderRevision string `json:"provider_revision"`
	SummaryRevision  string `json:"summary_revision"`
	Reason           string `json:"reason"`
}
type textAIConfig struct {
	db                           *gorm.DB
	baseURL, apiKey, model       string
	queryEnabled, summaryEnabled bool
}

// TextAIService owns configuration generations, not tagging jobs or subtitle facts.
// Configuration cancellation supplements each admitted App operation's lifetime.
type textAIPurposeGrant struct {
	revision   string
	generation context.Context
	cancel     context.CancelFunc
}
type TextAIService struct {
	mu     sync.Mutex
	client *http.Client
	loaded bool
	config textAIConfig
	grants map[string]textAIPurposeGrant
}

func NewTextAIService() *TextAIService {
	return &TextAIService{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func loadTextAIConfig(ctx context.Context) (textAIConfig, error) {
	var settings models.Settings
	var dbIdentity *gorm.DB
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error { dbIdentity = database.DB; return db.First(&settings).Error })
	if err != nil {
		return textAIConfig{}, err
	}
	config, _ := aiTaggingConfigFromSettings(&settings)
	return textAIConfig{db: dbIdentity, baseURL: config.BaseURL, apiKey: config.APIKey, model: config.Model, queryEnabled: settings.QueryParseEnabled, summaryEnabled: settings.ChapterSummaryEnabled}, nil
}
func textAIEndpoint(config textAIConfig) (string, error) {
	endpoint := openAIChatCompletionsURL(config.baseURL)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.TrimSpace(config.model) == "" {
		return "", ErrTextAIUnavailable
	}
	return endpoint, nil
}

// refreshLocked reads one persisted settings snapshot; no network I/O holds mu.
func (s *TextAIService) rotatePurposeLocked(purpose string) {
	if old, ok := s.grants[purpose]; ok {
		old.cancel()
	}
	if s.grants == nil {
		s.grants = map[string]textAIPurposeGrant{}
	}
	generation, cancel := context.WithCancel(context.Background())
	s.grants[purpose] = textAIPurposeGrant{revision: uuid.NewString(), generation: generation, cancel: cancel}
}
func (s *TextAIService) refreshLocked(ctx context.Context) error {
	config, err := loadTextAIConfig(ctx)
	if err != nil {
		for _, grant := range s.grants {
			grant.cancel()
		}
		s.grants = nil
		s.loaded = false
		return err
	}
	shared := !s.loaded || s.config.db != config.db || s.config.baseURL != config.baseURL || s.config.apiKey != config.apiKey || s.config.model != config.model
	if _, ok := s.grants[TextAIQueryPurpose]; shared || !ok || s.config.queryEnabled != config.queryEnabled {
		s.rotatePurposeLocked(TextAIQueryPurpose)
	}
	if _, ok := s.grants[TextAIChapterPurpose]; shared || !ok || s.config.summaryEnabled != config.summaryEnabled {
		s.rotatePurposeLocked(TextAIChapterPurpose)
	}
	s.config = config
	s.loaded = true
	return nil
}

// Invalidate retires both uses for shutdown, database or shared provider changes.
func (s *TextAIService) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, grant := range s.grants {
		grant.cancel()
	}
	s.grants = nil
	s.loaded = false
}

// InvalidatePurpose preserves the independent grant of the other text use.
func (s *TextAIService) InvalidatePurpose(purpose string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if grant, ok := s.grants[purpose]; ok {
		grant.cancel()
		delete(s.grants, purpose)
	}
}
func (s *TextAIService) Status(ctx context.Context) (*TextAIStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshLocked(ctx); err != nil {
		return nil, err
	}
	result := &TextAIStatus{QueryEnabled: s.config.queryEnabled, SummaryEnabled: s.config.summaryEnabled, Model: s.config.model, ProviderRevision: s.grants[TextAIQueryPurpose].revision, SummaryRevision: s.grants[TextAIChapterPurpose].revision}
	endpoint, err := textAIEndpoint(s.config)
	if err != nil {
		result.Reason = "请先在 AI 设置中填写接口地址与模型"
		return result, nil
	}
	parsed, _ := url.Parse(endpoint)
	result.Provider = parsed.Scheme + "://" + parsed.Host
	result.Available = true
	return result, nil
}
func (s *TextAIService) validateLocked(purpose, revision string) error {
	enabled := false
	switch purpose {
	case TextAIQueryPurpose:
		enabled = s.config.queryEnabled
	case TextAIChapterPurpose:
		enabled = s.config.summaryEnabled
	default:
		return ErrLibraryQueryInvalid
	}
	if !enabled {
		return ErrTextAIDisabled
	}
	if revision == "" || revision != s.grants[purpose].revision {
		return ErrTextAIConfigChanged
	}
	_, err := textAIEndpoint(s.config)
	return err
}
func (s *TextAIService) Validate(ctx context.Context, purpose, revision string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshLocked(ctx); err != nil {
		return err
	}
	return s.validateLocked(purpose, revision)
}

// Complete sends only the caller's explicitly reviewed text and a fixed-purpose
// instruction. It has no tools, follows no redirects and never retries.
func (s *TextAIService) Complete(owner context.Context, purpose, revision, instruction, text string) (string, error) {
	timeout := 20 * time.Second
	switch purpose {
	case TextAIQueryPurpose:
		if utf8.RuneCountInString(text) > 500 {
			return "", ErrLibraryQueryInvalid
		}
	case TextAIChapterPurpose:
		timeout = 60 * time.Second
		if len(text) > 256*1024 {
			return "", ErrLibraryQueryInvalid
		}
	default:
		return "", ErrLibraryQueryInvalid
	}
	ctx, cancel := context.WithTimeout(owner, timeout)
	defer cancel()
	s.mu.Lock()
	if err := s.refreshLocked(ctx); err != nil {
		s.mu.Unlock()
		return "", err
	}
	if err := s.validateLocked(purpose, revision); err != nil {
		s.mu.Unlock()
		return "", err
	}
	config, generation := s.config, s.grants[purpose].generation
	stop := context.AfterFunc(generation, cancel)
	s.mu.Unlock()
	defer stop()
	if generation.Err() != nil {
		return "", ErrTextAIConfigChanged
	}
	endpoint, _ := textAIEndpoint(config)
	body := map[string]any{"model": config.model, "messages": []map[string]string{{"role": "system", "content": instruction}, {"role": "user", "content": text}}, "response_format": map[string]string{"type": "json_object"}}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", ErrTextAIOutput
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", ErrTextAIUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	if config.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+config.apiKey)
	}
	response, err := s.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrTextAIFailed
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", ErrTextAIFailed
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrTextAIFailed
	}
	if len(raw) > 1024*1024 {
		return "", ErrTextAIOutput
	}
	var envelope struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content      string            `json:"content"`
				ToolCalls    []json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage   `json:"function_call"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Choices) != 1 {
		return "", ErrTextAIOutput
	}
	choice := envelope.Choices[0]
	if choice.FinishReason != "" && choice.FinishReason != "stop" || strings.TrimSpace(choice.Message.Content) == "" || len(choice.Message.ToolCalls) != 0 || len(choice.Message.FunctionCall) != 0 && string(choice.Message.FunctionCall) != "null" {
		return "", ErrTextAIOutput
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.Validate(ctx, purpose, revision); err != nil {
		return "", err
	}
	return choice.Message.Content, nil
}

// CommitPurpose keeps a purpose grant current through a bounded local commit.
// The callback must not call TextAI methods or make model requests.
func (s *TextAIService) CommitPurpose(ctx context.Context, purpose, revision string, commit func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.refreshLocked(ctx); err != nil {
		return err
	}
	if err := s.validateLocked(purpose, revision); err != nil {
		return err
	}
	return commit()
}
