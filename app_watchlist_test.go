package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// D-P2-03 的扫描触发：扫描入库了新视频才在后台给全部未关联的想看条目跑一轮自动关联；没有新视频不跑。
func TestScanWithNewVideosTriggersWatchlistLibraryMatch(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	year := 2016
	existing := models.Video{Name: "arrival.mp4", DisplayTitle: "降临", Year: &year, Path: filepath.Join(root, "arrival.mp4"), Directory: root, Size: 1}
	writeOldFile(t, existing.Path)
	if err := database.DB.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	entry := models.WatchlistEntry{Title: "降临", Kind: models.WatchlistKindMovie, Year: 2016, EnrichmentStatus: models.WatchlistEnrichmentManual}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	linked := func() int {
		states, err := app.ListWatchlistLibraryState([]uint{entry.ID})
		if err != nil {
			t.Fatal(err)
		}
		return len(states[entry.ID].Linked)
	}

	// 没有新视频：不触发。
	if result, err := app.SyncScanDirectories(services.ScanTriggerManual); err != nil || result.Added != 0 {
		t.Fatalf("应没有新视频: %+v err=%v", result, err)
	}
	app.watchlistService.WaitLibraryMatch()
	if n := linked(); n != 0 {
		t.Fatalf("没有新视频不应跑自动关联: %d", n)
	}

	// 新视频入库：后台跑一轮，关联已在库的那一部。
	writeOldFile(t, filepath.Join(root, "other.mp4"))
	if result, err := app.SyncScanDirectories(services.ScanTriggerManual); err != nil || result.Added != 1 {
		t.Fatalf("应新增 1 个视频: %+v err=%v", result, err)
	}
	app.watchlistService.WaitLibraryMatch()
	if n := linked(); n != 1 {
		t.Fatalf("扫描入库新视频后应自动关联: %d", n)
	}
}

// writeOldFile 写一个修改时间在一小时前的文件：扫描会跳过刚改过的文件。
func writeOldFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadImportTriggersWatchlistLibraryMatchOnlyWhenImported(t *testing.T) {
	setupAppTestDB(t)
	year := 2021
	video := models.Video{Name: "dune.mp4", DisplayTitle: "沙丘", Year: &year, Path: "/library/dune.mp4", Directory: "/library"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	entry := models.WatchlistEntry{Title: "沙丘", Kind: models.WatchlistKindMovie, Year: 2021, EnrichmentStatus: models.WatchlistEnrichmentManual}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	failed := app.importDownloadAndMatchWatchlist(func(string, string) (uint, error) { return 0, errors.New("没有入库") })
	if _, err := failed("/library", "/library/dune.mp4"); err == nil {
		t.Fatal("应原样返回入库错误")
	}
	app.watchlistService.WaitLibraryMatch()
	if states, _ := app.ListWatchlistLibraryState([]uint{entry.ID}); len(states[entry.ID].Linked) != 0 {
		t.Fatal("没入库不应触发")
	}
	imported := app.importDownloadAndMatchWatchlist(func(string, string) (uint, error) { return video.ID, nil })
	if id, err := imported("/library", "/library/dune.mp4"); err != nil || id != video.ID {
		t.Fatalf("应原样返回入库结果: %d %v", id, err)
	}
	app.watchlistService.WaitLibraryMatch()
	if states, _ := app.ListWatchlistLibraryState([]uint{entry.ID}); len(states[entry.ID].Linked) != 1 {
		t.Fatal("下载入库后应自动关联")
	}
}
