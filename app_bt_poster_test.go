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
