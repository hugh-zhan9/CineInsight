package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHotAssetByteCacheServesFromMemoryAndEvictsLRU(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "thumb.jpg")
	if err := os.WriteFile(filePath, []byte("jpeg-bytes-v1"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}

	cache := newHotAssetByteCache(32)
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/preview/thumbnail/1", nil)
	if err := cache.serveFile(rec1, req1, filePath, "thumb.jpg", info.ModTime(), "image/jpeg", "private, max-age=300"); err != nil {
		t.Fatalf("first serve failed: %v", err)
	}
	if rec1.Body.String() != "jpeg-bytes-v1" {
		t.Fatalf("unexpected first body: %q", rec1.Body.String())
	}

	// Remove backing file to prove second request hits memory directly.
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/preview/thumbnail/1", nil)
	if err := cache.serveFile(rec2, req2, filePath, "thumb.jpg", info.ModTime(), "image/jpeg", "private, max-age=300"); err != nil {
		t.Fatalf("cached serve failed: %v", err)
	}
	if rec2.Body.String() != "jpeg-bytes-v1" {
		t.Fatalf("unexpected cached body: %q", rec2.Body.String())
	}

	// ModTime change misses cache cleanly.
	if _, ok := cache.Get(filePath, info.ModTime().Add(time.Second)); ok {
		t.Fatalf("stale modTime should miss cache")
	}

	// Evict via larger entry exceeding 32-byte budget.
	cache.Put("/other.jpg", time.Now(), []byte("12345678901234567890123456"))
	if _, ok := cache.Get(filePath, info.ModTime()); ok {
		t.Fatalf("old entry should have been evicted by LRU budget")
	}
}
