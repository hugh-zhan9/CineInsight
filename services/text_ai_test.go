package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func textAIFixture(t *testing.T, handler http.HandlerFunc) (*TextAIService, *httptest.Server) {
	t.Helper()
	setupVideoServiceTestDB(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	settings := models.Settings{AITaggingBaseURL: server.URL + "/v1", AITaggingModel: "fixture-model", AITaggingAPIKey: "fixture-key", QueryParseEnabled: true}
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Updates(map[string]any{"ai_tagging_base_url": settings.AITaggingBaseURL, "ai_tagging_model": settings.AITaggingModel, "ai_tagging_api_key": settings.AITaggingAPIKey, "query_parse_enabled": true}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewTextAIService()
	t.Cleanup(service.Invalidate)
	return service, server
}
func textAIReply(w http.ResponseWriter, content string) {
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": content}}}})
}
func TestTextAIExplicitPurposeSendsOnlyReviewedTextAndRejectsDisabled(t *testing.T) {
	var calls atomic.Int64
	text := "未看 收藏 120分钟以内"
	service, _ := textAIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("wrong endpoint/auth")
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		if json.Unmarshal(raw, &body) != nil || len(body) != 3 {
			t.Errorf("unexpected payload: %s", raw)
		}
		var messages []map[string]string
		json.Unmarshal(body["messages"], &messages)
		if len(messages) != 2 || messages[1]["content"] != text || messages[1]["role"] != "user" {
			t.Errorf("unexpected messages %+v", messages)
		}
		textAIReply(w, `{"conditions":[],"unsupported":["未看 收藏 120分钟以内"]}`)
	})
	status, err := service.Status(context.Background())
	if err != nil || !status.Available || status.SummaryEnabled || calls.Load() != 0 {
		t.Fatalf("%+v %v", status, err)
	}
	if _, err := service.Complete(context.Background(), TextAIChapterPurpose, status.ProviderRevision, "fixture", text); !errors.Is(err, ErrTextAIDisabled) {
		t.Fatal(err)
	}
	if _, err := service.Complete(context.Background(), TextAIQueryPurpose, "old", "fixture", text); !errors.Is(err, ErrTextAIConfigChanged) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("called without purpose grant")
	}
	if _, err := service.Complete(context.Background(), TextAIQueryPurpose, status.ProviderRevision, "fixture", text); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
func TestTextAIConfigurationChangeCancelsFlightAndPreventsABA(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	service, _ := textAIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := service.Complete(context.Background(), TextAIQueryPurpose, status.ProviderRevision, "fixture", "未看")
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request not started")
	}
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("query_parse_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	service.Invalidate()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flight not cancelled")
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("server request not cancelled")
	}
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("query_parse_enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	next, err := service.Status(context.Background())
	if err != nil || next.ProviderRevision == status.ProviderRevision {
		t.Fatalf("ABA grant %+v %v", next, err)
	}
	if err := service.Validate(context.Background(), TextAIQueryPurpose, status.ProviderRevision); !errors.Is(err, ErrTextAIConfigChanged) {
		t.Fatal(err)
	}
}
func TestTextAINoRedirectsNoRetriesAndBoundedResponse(t *testing.T) {
	for _, mode := range []string{"redirect", "failed", "too_large", "tools"} {
		t.Run(mode, func(t *testing.T) {
			var calls, targetCalls atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
			defer target.Close()
			service, _ := textAIFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				case "failed":
					http.Error(w, "private response", 500)
				case "too_large":
					io.WriteString(w, strings.Repeat("x", 1024*1024+1))
				case "tools":
					io.WriteString(w, `{"choices":[{"message":{"content":"{}","tool_calls":[{}]}}]}`)
				}
			})
			status, err := service.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Complete(context.Background(), TextAIQueryPurpose, status.ProviderRevision, "fixture", "未看"); err == nil {
				t.Fatal("accepted failed/unbounded/tool response")
			}
			if calls.Load() != 1 || targetCalls.Load() != 0 {
				t.Fatalf("retried or redirected: %d/%d", calls.Load(), targetCalls.Load())
			}
		})
	}
}
