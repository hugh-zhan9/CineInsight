package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestScanDeletionSourceAndAutomaticRestore(t *testing.T) {
	for _, manual := range []bool{false, true} {
		name := "startup"
		if manual {
			name = "manual_directory"
		}
		t.Run(name, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := t.TempDir()
			svc := &VideoService{}
			scan := func() *ScanSyncResult {
				if manual {
					return svc.SyncDirectoryWithProgress(root, func(DirectoryScanProgress) {})
				}
				return svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
			}
			auto := createScanVisibilityVideo(t, filepath.Join(root, "auto.mp4"), false)
			user := createScanVisibilityVideo(t, filepath.Join(root, "user.mp4"), true)
			unknown := createScanVisibilityVideo(t, filepath.Join(root, "unknown.mp4"), false)
			tag := models.Tag{Name: "keep"}
			if err := database.DB.Model(&auto).Association("Tags").Append(&tag); err != nil {
				t.Fatal(err)
			}
			if err := svc.DeleteVideo(user.ID, false); err != nil {
				t.Fatal(err)
			}
			if err := database.DB.Delete(&unknown).Error; err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(auto.Path); err != nil {
				t.Fatal(err)
			}
			result := scan()
			if result.Deleted != 1 || len(result.Errors) != 0 {
				t.Fatalf("online missing file must be soft-deleted: %+v", result)
			}
			var deleted models.Video
			database.DB.Unscoped().First(&deleted, auto.ID)
			var trash models.VideoTrashEntry
			database.DB.Where("video_id = ?", auto.ID).First(&trash)
			if deleted.DeletedBy != "scanner" || trash.DeletedBy != "scanner" {
				t.Fatalf("missing scanner attribution: %+v %+v", deleted, trash)
			}
			var userTrash models.VideoTrashEntry
			database.DB.Where("video_id = ?", user.ID).First(&userTrash)
			if userTrash.DeletedBy != "user" {
				t.Fatalf("manual deletion attribution: %+v", userTrash)
			}
			mustCreateFile(t, auto.Path)
			mustSetFileModTime(t, auto.Path, time.Now().Add(-10*time.Minute))
			result = scan()
			if result.Restored != 1 || result.Added != 0 || len(result.Errors) != 0 {
				t.Fatalf("scan must restore original: %+v", result)
			}
			var restored models.Video
			if err := database.DB.Preload("Tags").First(&restored, auto.ID).Error; err != nil {
				t.Fatal(err)
			}
			if restored.IsStale || len(restored.Tags) != 1 || restored.Tags[0].ID != tag.ID {
				t.Fatalf("lost metadata: %+v", restored)
			}
			for _, id := range []uint{user.ID, unknown.ID} {
				var count int64
				database.DB.Model(&models.Video{}).Where("id = ?", id).Count(&count)
				if count != 0 {
					t.Fatalf("user/unknown deletion restored: %d", id)
				}
			}
		})
	}
}

func TestScanOfflineRootHidesWithoutDeletingAndRemountRestores(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := filepath.Join(t.TempDir(), "disk")
	video := createScanVisibilityVideo(t, filepath.Join(root, "movie.mp4"), false)
	if err := os.Rename(root, root+"-ejected"); err != nil {
		t.Fatal(err)
	}
	svc := &VideoService{}
	dirs := []models.ScanDirectory{{Path: root}}
	result := svc.SyncScanDirectories(dirs)
	var saved models.Video
	if err := database.DB.First(&saved, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 0 || result.Stale != 1 || !saved.IsStale || saved.DeletedBy != "" {
		t.Fatalf("eject must only hide: %+v %+v", result, saved)
	}
	var count int64
	database.DB.Model(&models.VideoTrashEntry{}).Count(&count)
	if count != 0 {
		t.Fatal("offline disk entered trash")
	}
	if err := os.Rename(root+"-ejected", root); err != nil {
		t.Fatal(err)
	}
	result = svc.SyncScanDirectories(dirs)
	if result.Restored != 1 || result.Added != 0 || result.Deleted != 0 {
		t.Fatalf("remount must restore: %+v", result)
	}
}

func TestScanDeletionRechecksRootAndBlacklistAfterTraversal(t *testing.T) {
	for _, change := range []string{"eject", "blacklist", "file_returns"} {
		t.Run(change, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := filepath.Join(t.TempDir(), "disk")
			video := createScanVisibilityVideo(t, filepath.Join(root, "missing.mp4"), false)
			if err := os.Remove(video.Path); err != nil {
				t.Fatal(err)
			}
			changed := false
			result := (&VideoService{}).SyncDirectoryWithProgress(root, func(p DirectoryScanProgress) {
				if p.Phase != "processing" || changed {
					return
				}
				changed = true
				switch change {
				case "eject":
					if err := os.Rename(root, root+"-offline"); err != nil {
						t.Fatal(err)
					}
				case "blacklist":
					if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", root).Error; err != nil {
						t.Fatal(err)
					}
				case "file_returns":
					mustCreateFile(t, video.Path)
				}
			})
			if !changed || result.Deleted != 0 {
				t.Fatalf("changed state must block deletion: %+v", result)
			}
			if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLibraryBlacklistHidesAndUnhidesWithoutDeleting(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	excluded := filepath.Join(root, "hide_%")
	v := createScanVisibilityVideo(t, filepath.Join(excluded, "a.mp4"), false)
	sibling := createScanVisibilityVideo(t, filepath.Join(root, "hide_X", "b.mp4"), false)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", excluded).Error; err != nil {
		t.Fatal(err)
	}
	visible := func(id uint) int64 {
		q, err := applyLibraryFilter(database.DB.Model(&models.Video{}), LibraryFilter{}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := q.Where("videos.id = ?", id).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count
	}
	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.Deleted != 0 || visible(v.ID) != 0 || visible(sibling.ID) != 1 {
		t.Fatalf("blacklist visibility incorrect: %+v", result)
	}
	if err := database.DB.First(&models.Video{}, v.ID).Error; err != nil {
		t.Fatal(err)
	}
	database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", "")
	if visible(v.ID) != 1 {
		t.Fatal("removing exclusion should immediately show the original record")
	}
}

func TestManualScanDoesNotMarkOtherRootsStale(t *testing.T) {
	setupVideoServiceTestDB(t)
	other := createScanVisibilityVideo(t, filepath.Join(t.TempDir(), "other.mp4"), false)
	(&VideoService{}).SyncDirectoryWithProgress(t.TempDir(), nil)
	var saved models.Video
	if err := database.DB.First(&saved, other.ID).Error; err != nil || saved.IsStale {
		t.Fatalf("unrelated root changed: %+v %v", saved, err)
	}
}

func TestTrashRestoreClearsStale(t *testing.T) {
	setupVideoServiceTestDB(t)
	v := createScanVisibilityVideo(t, filepath.Join(t.TempDir(), "a.mp4"), true)
	svc := &VideoService{}
	if err := svc.DeleteVideo(v.ID, false); err != nil {
		t.Fatal(err)
	}
	var entry models.VideoTrashEntry
	database.DB.Where("video_id = ?", v.ID).First(&entry)
	restored, err := svc.RestoreTrashEntry(entry.ID)
	if err != nil || restored.IsStale {
		t.Fatalf("restored record must be visible: %+v %v", restored, err)
	}
}

func TestScanEjectAfterDiscoveryHidesEvenWithoutMissingCandidates(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := filepath.Join(t.TempDir(), "disk")
	v := createScanVisibilityVideo(t, filepath.Join(root, "a.mp4"), false)
	changed := false
	result := (&VideoService{}).SyncDirectoryWithProgress(root, func(p DirectoryScanProgress) {
		if p.Phase == "processing" && !changed {
			changed = true
			if err := os.Rename(root, root+"-offline"); err != nil {
				t.Fatal(err)
			}
		}
	})
	var saved models.Video
	if err := database.DB.First(&saved, v.ID).Error; err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 0 || !saved.IsStale {
		t.Fatalf("late eject must hide all records: %+v %+v", result, saved)
	}
}

func TestImageBlacklistHidesWithoutDeleting(t *testing.T) {
	setupImageServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "a.jpg")
	mustCreateFile(t, path)
	svc := &ImageService{}
	imageTestMustAddDirectory(t, svc, root)
	imageTestMustSync(t, svc)
	database.DB.Model(&models.Settings{}).Where("1 = 1").Update("image_scan_exclude_paths", root)
	result := imageTestMustSync(t, svc)
	if result.Removed != 0 {
		t.Fatalf("exclusion deleted rows: %+v", result)
	}
	var count int64
	database.DB.Model(&models.Image{}).Count(&count)
	if count != 1 {
		t.Fatal("record must remain active")
	}
	q := applyImageFilter(database.DB.Model(&models.Image{}), ImageFilter{})
	if err := q.Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("excluded image visible: %d %v", count, err)
	}
	database.DB.Model(&models.Settings{}).Where("1 = 1").Update("image_scan_exclude_paths", "")
	if err := applyImageFilter(database.DB.Model(&models.Image{}), ImageFilter{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("remove exclusion should unhide: %d %v", count, err)
	}
}
