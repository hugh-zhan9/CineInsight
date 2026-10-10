package services

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"video-master/models"
)

func TestViewingDiaryMobileOptionalSessionHTTP(t *testing.T) {
	video, _ := notesFixture(t)
	server := NewShortFeedHTTPServer(NewShortFeedService(&VideoService{}), nil, ShortFeedHTTPServerConfig{})
	post := func(body, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/short-api/items/video/%d/play", video.ID), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		server.handleItemMutation(rec, req)
		return rec
	}
	for _, body := range []string{`{"source":"short_feed","view_session_id":"visit"}`, `{"source":"short_feed","view_session_id":"visit"}`, `{"source":"short_feed"}`} {
		rec := post(body, "http://example.com")
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 1)
	mustNoteCount(t, &models.PlayEvent{}, 2)
	for _, body := range []string{`{"source":"wrong","view_session_id":"visit"}`, `{"source":"short_feed","view_session_id":" "}`, `{"source":"short_feed","view_session_id":42}`, `{"source":"short_feed","view_session_id":"` + strings.Repeat("x", 65) + `"}`} {
		if rec := post(body, "http://example.com"); rec.Code != 400 {
			t.Fatalf("invalid body status %d", rec.Code)
		}
	}
	if rec := post(`{"source":"short_feed","view_session_id":"other"}`, "http://evil.test"); rec.Code != 403 {
		t.Fatalf("origin status %d", rec.Code)
	}
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 1)
	mustNoteCount(t, &models.PlayEvent{}, 2)
}
