package services

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gorm.io/gorm"

	"video-master/database"
	"video-master/models"
)

func TestJellyfinOriginalRangeReadsCurrentScanRootsOnce(t *testing.T) {
	s, token, video, _ := jellyfinLibraryFixture(t)
	rootReads := 0
	const probe = "test:count_jellyfin_scan_root_reads"
	if err := database.DB.Callback().Query().Before("gorm:query").Register(probe, func(tx *gorm.DB) {
		if tx.Statement.Table == "scan_directories" {
			rootReads++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.DB.Callback().Query().Remove(probe) })
	path := "/Videos/" + jellyfinID(jellyVideo, video.ID) + "/stream"
	read := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "127.0.0.1:2"
		r.Header.Set("X-Emby-Token", token)
		r.Header.Set("Range", "bytes=2-5")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := read(); w.Code != http.StatusPartialContent || w.Body.String() != "2345" {
		t.Fatalf("wrong original bytes: %d %q", w.Code, w.Body)
	}
	if rootReads != 1 {
		t.Errorf("expected one current root lookup for original Range; got %d", rootReads)
	}
	rootReads = 0
	if err := database.DB.Where("path = ?", video.Directory).Delete(&models.ScanDirectory{}).Error; err != nil {
		t.Fatal(err)
	}
	if w := read(); w.Code != http.StatusNotFound || rootReads != 1 {
		t.Fatalf("root removal must take effect on next Range with one lookup: %d %s, root reads %d", w.Code, w.Body, rootReads)
	}
}
