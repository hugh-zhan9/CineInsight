package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func createScanVisibilityVideo(t *testing.T, path string, stale bool) models.Video {
	t.Helper()
	mustCreateFile(t, path)
	mustSetFileModTime(t, path, time.Now().Add(-10*time.Minute))
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: 1,
		Duration: 600, Width: 1280, Height: 720, Resolution: "1280x720", IsStale: stale}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

func TestFullScanRestoresExistingStaleVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createScanVisibilityVideo(t, filepath.Join(root, "movie.mp4"), true)
	tag := models.Tag{Name: "保留标签"}
	if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if len(result.Errors) != 0 || result.Restored != 1 || result.Added != 0 || result.Deleted != 0 {
		t.Fatalf("已存在文件应恢复原记录: %+v", result)
	}
	var restored models.Video
	if err := database.DB.Preload("Tags").First(&restored, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restored.IsStale || len(restored.Tags) != 1 || restored.Tags[0].ID != tag.ID {
		t.Fatalf("恢复必须保留原 ID 与标签: %+v", restored)
	}
	if again := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: root}}); again.Restored != 0 {
		t.Fatalf("重复扫描不应重复计恢复: %+v", again)
	}
}

func TestFullScanDoesNotDeleteOrRelocateRecentlyChangedExistingVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createScanVisibilityVideo(t, filepath.Join(root, "old", "movie.mp4"), false)
	mustSetFileModTime(t, video.Path, time.Now())
	// 相同名称和大小的新文件不能把仍在原位置、仅因活跃窗口被跳过的记录抢走。
	newPath := filepath.Join(root, "new", "movie.mp4")
	mustCreateFile(t, newPath)
	mustSetFileModTime(t, newPath, time.Now().Add(-10*time.Minute))
	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.Deleted != 0 || result.Relocated != 0 || result.Added != 1 || len(result.Errors) != 0 {
		t.Fatalf("过滤掉的已存在文件不得进入缺失/迁移候选: %+v", result)
	}
	var saved models.Video
	if err := database.DB.First(&saved, video.ID).Error; err != nil {
		t.Fatalf("记录不应被软删: %v", err)
	}
	if saved.Path != video.Path || saved.IsStale {
		t.Fatalf("原记录路径/可见性被改动: %+v", saved)
	}
	var trashCount int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("video_id = ?", video.ID).Count(&trashCount).Error; err != nil || trashCount != 0 {
		t.Fatalf("不能产生回收站条目: count=%d err=%v", trashCount, err)
	}
}

func TestFullScanKeepsRecentlyChangedExistingVideoVisible(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createScanVisibilityVideo(t, filepath.Join(root, "movie.mp4"), false)
	mustSetFileModTime(t, video.Path, time.Now())
	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.Deleted != 0 || result.Relocated != 0 || len(result.Errors) != 0 {
		t.Fatalf("已有近期文件不能被当作丢失: %+v", result)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("已有记录不应隐藏: %v", err)
	}
}

func TestScanReportsUnreadableSubdirectoryAndPreservesRecords(t *testing.T) {
	for _, narrow := range []bool{false, true} {
		name := "full"
		if narrow {
			name = "narrow"
		}
		t.Run(name, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := t.TempDir()
			folder := filepath.Join(root, "restricted")
			video := createScanVisibilityVideo(t, filepath.Join(folder, "movie.mp4"), false)
			if err := os.Chmod(folder, 0000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(folder, 0755) })
			if _, err := os.ReadDir(folder); !errors.Is(err, os.ErrPermission) {
				t.Skip("当前环境无法模拟目录读取权限失败")
			}
			svc := &VideoService{}
			if _, err := svc.ScanDirectoryWithInfo(root); !errors.Is(err, os.ErrPermission) {
				t.Errorf("目录读取失败必须报错，不应伪装成空扫描: %v", err)
			}
			dirs := []models.ScanDirectory{{Path: root}}
			var result *ScanSyncResult
			if narrow {
				result = svc.SyncAffectedDirectories(dirs, []string{root})
			} else {
				result = svc.SyncScanDirectories(dirs)
			}
			if len(result.Errors) == 0 || result.Deleted != 0 || result.Stale != 0 {
				t.Fatalf("失败扫描不能删除或隐藏记录: %+v", result)
			}
			var saved models.Video
			if err := database.DB.First(&saved, video.ID).Error; err != nil || saved.IsStale {
				t.Fatalf("读取错误不等于文件丢失: stale=%v err=%v", saved.IsStale, err)
			}
		})
	}
}

func TestNarrowScanKeepsExcludedExistingVideoVisible(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createScanVisibilityVideo(t, filepath.Join(root, "excluded", "movie.mp4"), false)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", video.Directory).Error; err != nil {
		t.Fatal(err)
	}
	result := (&VideoService{}).SyncAffectedDirectories([]models.ScanDirectory{{Path: root}}, []string{root})
	if result.Stale != 0 || result.Deleted != 0 || len(result.Errors) != 0 {
		t.Fatalf("黑名单只影响收录，不能把现有视频误判为丢失: %+v", result)
	}
}

func TestFullScanStillRemovesTrulyMissingVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createScanVisibilityVideo(t, filepath.Join(root, "movie.mp4"), false)
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.Deleted != 1 || len(result.Errors) != 0 {
		t.Fatalf("明确丢失仍应遵守原有删除规则: %+v", result)
	}
}

func TestScanSkipsUnreadableExcludedAndHiddenDirectories(t *testing.T) {
	for _, name := range []string{"excluded", ".Spotlight-V100", DefaultTrashDirName} {
		t.Run(name, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := t.TempDir()
			video := createScanVisibilityVideo(t, filepath.Join(root, "visible.mp4"), false)
			folder := filepath.Join(root, name)
			if err := os.Mkdir(folder, 0755); err != nil {
				t.Fatal(err)
			}
			if name == "excluded" {
				if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", folder).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(folder, 0000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(folder, 0755) })
			if _, err := os.ReadDir(folder); !errors.Is(err, os.ErrPermission) {
				t.Skip("当前环境无法模拟读取权限失败")
			}
			svc := &VideoService{}
			files, err := svc.ScanDirectoryWithInfo(root)
			if err != nil || len(files) != 1 || files[0].Path != video.Path {
				t.Fatalf("扫描范围外的不可读目录不能挡住正常视频: files=%v err=%v", files, err)
			}
		})
	}
}

func TestNarrowScanDoesNotProbeUnrelatedUnavailableStaleFiles(t *testing.T) {
	for _, addUnrelated := range []bool{false, true} {
		name := "no_new_files"
		if addUnrelated {
			name = "unrelated_new_file"
		}
		t.Run(name, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := t.TempDir()
			otherRoot := t.TempDir()
			folder := filepath.Join(otherRoot, "restricted")
			stale := createScanVisibilityVideo(t, filepath.Join(folder, "old.mp4"), true)
			if addUnrelated {
				mustCreateFile(t, filepath.Join(root, "new.mp4"))
			}
			if err := os.Chmod(folder, 0000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(folder, 0755) })
			if _, err := os.Stat(stale.Path); !errors.Is(err, os.ErrPermission) {
				t.Skip("当前环境无法模拟权限失败")
			}
			dirs := []models.ScanDirectory{{Path: root}, {Path: otherRoot}}
			result := (&VideoService{}).SyncAffectedDirectories(dirs, []string{root})
			wantAdded := 0
			if addUnrelated {
				wantAdded = 1
			}
			if len(result.Errors) != 0 || result.Added != wantAdded || result.Relocated != 0 || result.Stale != 0 {
				t.Fatalf("无关根不可用不应令当前目录同步失败: %+v", result)
			}
		})
	}
}

func TestNarrowScanDoesNotRelocateStaleFileThatStillExists(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	otherRoot := t.TempDir()
	stale := createScanVisibilityVideo(t, filepath.Join(otherRoot, "movie.mp4"), true)
	mustCreateFile(t, filepath.Join(root, "movie.mp4"))
	result := (&VideoService{}).SyncAffectedDirectories([]models.ScanDirectory{{Path: root}, {Path: otherRoot}}, []string{root})
	if len(result.Errors) != 0 || result.Relocated != 0 || result.Added != 1 {
		t.Fatalf("原文件仍在时不能挪用其记录: %+v", result)
	}
	var saved models.Video
	if err := database.DB.First(&saved, stale.ID).Error; err != nil || saved.Path != stale.Path {
		t.Fatalf("另一目录原记录被改动: %+v %v", saved, err)
	}
}
