package services

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func TestSyncAffectedDirectoriesImportsStableRecentFileWithoutScanningWholeRoot(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	affected := filepath.Join(root, "incoming")
	path := filepath.Join(affected, "new.mp4")
	mustCreateFile(t, path)

	service := &VideoService{}
	var scanned []string
	service.scanDirectoryWithOptions = func(directory string, skipRecentlyActive bool) ([]ScannedFile, error) {
		scanned = append(scanned, directory)
		if skipRecentlyActive {
			t.Fatal("watcher reconciliation must accept files after its own stability probe")
		}
		return service.scanDirectoryWithInfo(directory, skipRecentlyActive)
	}

	result := service.SyncAffectedDirectories([]models.ScanDirectory{{ID: 1, Path: root}}, []string{affected})
	if len(result.Errors) != 0 || result.Added != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(scanned) != 1 || scanned[0] != affected {
		t.Fatalf("narrow reconciliation scanned %#v, want only %q", scanned, affected)
	}
	var video models.Video
	if err := database.DB.Where("path = ?", path).First(&video).Error; err != nil {
		t.Fatalf("recent stable file was not imported: %v", err)
	}
}

func TestSyncAffectedDirectoriesPreservesRelationsAcrossUniqueRename(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	directory := filepath.Join(root, "movies")
	oldPath := filepath.Join(directory, "old-name.mp4")
	newPath := filepath.Join(directory, "new-name.mp4")
	mustCreateFile(t, oldPath)

	tag := models.Tag{Name: "preserved", Color: "#fff"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}
	video := models.Video{Name: filepath.Base(oldPath), Path: oldPath, Directory: directory, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("attach tag: %v", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("rename source: %v", err)
	}

	result := (&VideoService{}).SyncAffectedDirectories([]models.ScanDirectory{{ID: 1, Path: root}}, []string{directory})
	if len(result.Errors) != 0 || result.Relocated != 1 || result.Added != 0 || result.Stale != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	var loaded models.Video
	if err := database.DB.Preload("Tags").First(&loaded, video.ID).Error; err != nil {
		t.Fatalf("load relocated video: %v", err)
	}
	if loaded.Path != newPath || len(loaded.Tags) != 1 || loaded.Tags[0].ID != tag.ID {
		t.Fatalf("rename did not preserve identity: %#v", loaded)
	}
}

func TestReconciliationLoadsOnlyVideosUnderTheConfiguredRoots(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	outsideRoot := t.TempDir()
	inside := models.Video{Name: "inside.mp4", Path: filepath.Join(root, "inside.mp4"), Directory: root, Size: 1}
	if err := database.DB.Create(&inside).Error; err != nil {
		t.Fatalf("create in-root video: %v", err)
	}
	for i := 0; i < 5; i++ {
		outside := models.Video{
			Name:      fmt.Sprintf("outside-%d.mp4", i),
			Path:      filepath.Join(outsideRoot, fmt.Sprintf("outside-%d.mp4", i)),
			Directory: outsideRoot,
			Size:      int64(100 + i),
		}
		if err := database.DB.Create(&outside).Error; err != nil {
			t.Fatalf("create out-of-root video: %v", err)
		}
	}

	var rowsRead int64
	const callbackName = "test:count_video_rows"
	if err := database.DB.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "videos" {
			rowsRead += tx.Statement.RowsAffected
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() { _ = database.DB.Callback().Query().Remove(callbackName) })

	videos, err := (&VideoService{}).getActiveVideosUnderRoots([]string{root})
	if err != nil {
		t.Fatalf("load videos under roots: %v", err)
	}
	if len(videos) != 1 || videos[0].ID != inside.ID {
		t.Fatalf("unexpected videos: %#v", videos)
	}
	// The database must do the narrowing; loading the whole library per batch is what
	// makes watcher reconciliation expensive on large collections.
	if rowsRead != 1 {
		t.Fatalf("reconciliation read %d video rows, want 1", rowsRead)
	}
}

func TestSyncAffectedDirectoriesDoesNotClaimStaleRecordsOutsideAffectedDirectories(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	affected := filepath.Join(root, "affected")
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(affected, 0755); err != nil {
		t.Fatalf("create affected directory: %v", err)
	}
	if err := os.MkdirAll(elsewhere, 0755); err != nil {
		t.Fatalf("create unrelated directory: %v", err)
	}

	// A stale record from an untouched directory, carrying curated metadata.
	tag := models.Tag{Name: "curated", Color: "#fff"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}
	stalePath := filepath.Join(elsewhere, "long-gone.mp4")
	stale := models.Video{Name: "long-gone.mp4", Path: stalePath, Directory: elsewhere, Size: 12345, IsStale: true}
	if err := database.DB.Create(&stale).Error; err != nil {
		t.Fatalf("create stale video: %v", err)
	}
	if err := database.DB.Model(&stale).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("attach tag: %v", err)
	}

	// An unrelated new file that merely happens to have the same byte count.
	newPath := filepath.Join(affected, "brand-new.mp4")
	if err := os.WriteFile(newPath, make([]byte, 12345), 0644); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	result := (&VideoService{}).SyncAffectedDirectories([]models.ScanDirectory{{ID: 1, Path: root}}, []string{affected})
	if result.Relocated != 0 {
		t.Fatalf("a same-size coincidence must not be treated as a relocation: %#v", result)
	}
	if result.Added != 1 {
		t.Fatalf("the unrelated file should be imported as new: %#v", result)
	}
	var reloaded models.Video
	if err := database.DB.Preload("Tags").First(&reloaded, stale.ID).Error; err != nil {
		t.Fatalf("load stale video: %v", err)
	}
	if reloaded.Path != stalePath || len(reloaded.Tags) != 1 {
		t.Fatalf("stale record lost its identity: %#v", reloaded)
	}
}

func TestSyncAffectedDirectoriesMarksMissingStaleAndClearsWhenRestored(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, path)
	video := models.Video{Name: "movie.mp4", Path: path, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove video: %v", err)
	}
	service := &VideoService{}

	missing := service.SyncAffectedDirectories([]models.ScanDirectory{{ID: 1, Path: root}}, []string{root})
	if missing.Stale != 1 || missing.Deleted != 0 {
		t.Fatalf("missing result: %#v", missing)
	}
	var stale models.Video
	if err := database.DB.First(&stale, video.ID).Error; err != nil {
		t.Fatalf("watcher must keep an active recoverable record: %v", err)
	}
	if !stale.IsStale {
		t.Fatal("missing video was not marked stale")
	}

	mustCreateFile(t, path)
	restored := service.SyncAffectedDirectories([]models.ScanDirectory{{ID: 1, Path: root}}, []string{root})
	if len(restored.Errors) != 0 || restored.Added != 0 {
		t.Fatalf("restored result: %#v", restored)
	}
	if err := database.DB.First(&stale, video.ID).Error; err != nil {
		t.Fatalf("reload restored video: %v", err)
	}
	if stale.IsStale {
		t.Fatal("restored path remained stale")
	}
}

func TestSyncAffectedDirectoriesRefreshesChangedSubtitleIndex(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	srtPath := filepath.Join(root, "movie.srt")
	mustCreateFile(t, videoPath)
	if err := os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nold subtitle\n"), 0644); err != nil {
		t.Fatalf("write old SRT: %v", err)
	}
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	if err := indexSubtitleFileForVideoID(video.ID, srtPath); err != nil {
		t.Fatalf("index old SRT: %v", err)
	}
	if err := os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nnew subtitle text\n"), 0644); err != nil {
		t.Fatalf("write new SRT: %v", err)
	}

	result := (&VideoService{}).SyncAffectedDirectories([]models.ScanDirectory{{ID: 1, Path: root}}, []string{root})
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	var indexed models.SubtitleSegment
	if err := database.DB.Where("video_id = ?", video.ID).First(&indexed).Error; err != nil {
		t.Fatalf("load refreshed segment: %v", err)
	}
	if indexed.Text != "new subtitle text" {
		t.Fatalf("subtitle index text = %q", indexed.Text)
	}
}
