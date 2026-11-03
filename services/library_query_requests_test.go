package services

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

const fixtureQueryOutput = `{"conditions":[{"kind":"watch","operator":"eq","value":"unwatched","source_text":"未看"}],"unsupported":[]}`

func TestLibraryQueryCancellationBeforeAdmissionAndCompletedIdentity(t *testing.T) {
	var calls atomic.Int64
	textAI, _ := textAIFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); textAIReply(w, fixtureQueryOutput) })
	service := NewLibraryQueryService(&VideoService{}, textAI)
	status, err := textAI.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := ParseLibraryQueryRequest{RequestID: uuid.NewString(), Text: "未看", Mode: "external", ProviderRevision: status.ProviderRevision}
	if state, err := service.Cancel(context.Background(), request.RequestID); err != nil || state.Status != "cancelled" {
		t.Fatalf("%+v %v", state, err)
	}
	if _, err := service.Parse(context.Background(), request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("sent after early cancel")
	}
	request.RequestID = uuid.NewString()
	draft, err := service.Parse(context.Background(), request)
	if err != nil || draft.Status != "ready" {
		t.Fatalf("%+v %v", draft, err)
	}
	if state, err := service.Cancel(context.Background(), request.RequestID); err != nil || state.Status != "completed" {
		t.Fatalf("completed cancellation %+v %v", state, err)
	}
	if _, err := service.Parse(context.Background(), request); !errors.Is(err, ErrLibraryQueryRequestUsed) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("replayed request")
	}
}
func TestLibraryQueryCancellationStopsHTTPWithoutCancellingNextRequest(t *testing.T) {
	var calls atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	textAI, _ := textAIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		textAIReply(w, fixtureQueryOutput)
	})
	t.Cleanup(func() { close(release) })
	service := NewLibraryQueryService(&VideoService{}, textAI)
	status, err := textAI.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := ParseLibraryQueryRequest{RequestID: uuid.NewString(), Text: "未看", Mode: "external", ProviderRevision: status.ProviderRevision}
	done := make(chan error, 1)
	go func() { _, err := service.Parse(context.Background(), first); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	if _, err := service.Cancel(context.Background(), first.RequestID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not cancel")
	}
	second := first
	second.RequestID = uuid.NewString()
	if _, err := service.Cancel(context.Background(), first.RequestID); err != nil {
		t.Fatal(err)
	}
	if draft, err := service.Parse(context.Background(), second); err != nil || draft.Status != "ready" {
		t.Fatalf("old cancel affected next %+v %v", draft, err)
	}
	if len(service.drafts) != 1 || calls.Load() != 2 {
		t.Fatalf("late draft or duplicate request: %d/%d", len(service.drafts), calls.Load())
	}
}

func TestExternalQueryDraftCannotOutlivePurposeConfiguration(t *testing.T) {
	var calls atomic.Int64
	textAI, _ := textAIFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); textAIReply(w, fixtureQueryOutput) })
	service := NewLibraryQueryService(&VideoService{}, textAI)
	status, err := textAI.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	draft, err := service.Parse(context.Background(), ParseLibraryQueryRequest{RequestID: uuid.NewString(), Text: "未看", Mode: "external", ProviderRevision: status.ProviderRevision})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("query_parse_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search(context.Background(), resolvedQuery(draft)); !errors.Is(err, ErrTextAIDisabled) {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("query_parse_enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search(context.Background(), resolvedQuery(draft)); !errors.Is(err, ErrTextAIConfigChanged) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("search re-sent user text")
	}
}
