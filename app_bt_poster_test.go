package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBTPosterAssetRejectsUnboundRequests(t *testing.T) {
	h := newAssetHandler(&App{})
	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/preview/bt-poster/ci-bt-" + strings.Repeat("a", 48), 405},
		{"GET", "/preview/bt-poster/http://example.com/x", 404},
		{"GET", "/preview/bt-poster/ci-bt-" + strings.Repeat("a", 48) + "?url=http://example.com", 404},
		{"GET", "/preview/bt-poster/ci-bt-" + strings.Repeat("a", 48), 404},
	} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(tt.method, tt.path, nil))
		if r.Code != tt.status {
			t.Fatalf("%s: %d", tt.path, r.Code)
		}
	}
}

func TestBTDetailImageAssetRejectsUnboundRequests(t *testing.T) {
	app := &App{}
	for _, item := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/preview/bt-detail-image/ci-bt-" + strings.Repeat("a", 48) + "/1/0", 405},
		{"GET", "/preview/bt-detail-image/http://example.com/1/0", 404},
		{"GET", "/preview/bt-detail-image/ci-bt-" + strings.Repeat("a", 48) + "/1/0?url=private", 404},
		{"GET", "/preview/bt-detail-image/ci-bt-" + strings.Repeat("a", 48) + "/0/0", 404},
		{"GET", "/preview/bt-detail-image/ci-bt-" + strings.Repeat("a", 48) + "/1/16", 404},
		{"GET", "/preview/bt-detail-image/ci-bt-" + strings.Repeat("a", 48) + "/1/0", 404},
	} {
		response := httptest.NewRecorder()
		app.serveBTDetailImage(response, httptest.NewRequest(item.method, item.path, nil))
		if response.Code != item.status {
			t.Error("unexpected asset response", response.Code)
		}
	}
}
