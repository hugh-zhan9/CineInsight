package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupVideoServiceTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "video_service_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}

	if err := db.AutoMigrate(&models.Video{}, &models.Tag{}, &models.Settings{}, &models.ScanDirectory{}); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}

	database.DB = db
	if err := db.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2.0}).Error; err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}
}

func mustCreateFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
}

func TestScanDirectorySkipsHiddenFilesAndDirs(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	visible := filepath.Join(root, "video.mp4")
	hiddenFile := filepath.Join(root, ".hidden.mp4")
	hiddenDirFile := filepath.Join(root, ".cache", "inside.mp4")

	mustCreateFile(t, visible)
	mustCreateFile(t, hiddenFile)
	mustCreateFile(t, hiddenDirFile)

	files, err := svc.ScanDirectory(root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("期望仅扫描到1个可见视频，实际: %d, files=%v", len(files), files)
	}
	if files[0] != visible {
		t.Fatalf("扫描结果不正确: got=%s want=%s", files[0], visible)
	}
}

func TestSearchVideosWithFiltersCombinesKeywordAndTags(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}

	tag := models.Tag{Name: "运动", Color: "#fff"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}

	v1 := models.Video{Name: "cat_run.mp4", Path: "/tmp/cat_run.mp4", Directory: "/tmp", Size: 10}
	v2 := models.Video{Name: "cat_sleep.mp4", Path: "/tmp/cat_sleep.mp4", Directory: "/tmp", Size: 11}
	v3 := models.Video{Name: "dog_run.mp4", Path: "/tmp/dog_run.mp4", Directory: "/tmp", Size: 12}
	if err := database.DB.Create(&v1).Error; err != nil {
		t.Fatalf("创建视频1失败: %v", err)
	}
	if err := database.DB.Create(&v2).Error; err != nil {
		t.Fatalf("创建视频2失败: %v", err)
	}
	if err := database.DB.Create(&v3).Error; err != nil {
		t.Fatalf("创建视频3失败: %v", err)
	}

	if err := database.DB.Model(&v1).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("绑定标签失败: %v", err)
	}
	if err := database.DB.Model(&v3).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("绑定标签失败: %v", err)
	}

	videos, err := svc.SearchVideosWithFilters("cat", []uint{tag.ID}, 0, 0, 0, 100)
	if err != nil {
		t.Fatalf("组合搜索失败: %v", err)
	}
	if len(videos) != 1 {
		t.Fatalf("期望仅返回1条结果，实际 %d", len(videos))
	}
	if videos[0].Name != "cat_run.mp4" {
		t.Fatalf("返回了错误的视频: %s", videos[0].Name)
	}
}

func TestPlayRandomVideoErrorContainsVideoInfo(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "broken.mp4")
	mustCreateFile(t, videoPath)

	video := models.Video{Name: "broken.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	oldOpen := openWithDefaultFn
	openWithDefaultFn = func(path string, isDir bool) error {
		return errors.New("open failed")
	}
	defer func() { openWithDefaultFn = oldOpen }()

	_, err := svc.PlayRandomVideo()
	if err == nil {
		t.Fatalf("期望播放失败")
	}
	msg := err.Error()
	if !strings.Contains(msg, "broken.mp4") || !strings.Contains(msg, videoPath) {
		t.Fatalf("错误信息未包含视频信息: %s", msg)
	}
}

func TestVideoPathHasUniqueConstraint(t *testing.T) {
	setupVideoServiceTestDB(t)

	v1 := models.Video{Name: "a.mp4", Path: "/tmp/dup.mp4", Directory: "/tmp", Size: 1, CreatedAt: time.Now()}
	v2 := models.Video{Name: "b.mp4", Path: "/tmp/dup.mp4", Directory: "/tmp", Size: 2, CreatedAt: time.Now()}
	if err := database.DB.Create(&v1).Error; err != nil {
		t.Fatalf("创建首条记录失败: %v", err)
	}
	if err := database.DB.Create(&v2).Error; err == nil {
		t.Fatalf("期望路径唯一约束生效，但插入成功")
	}
}

func TestGetVideosByDirectoryIncludesSubdirectories(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}

	root := filepath.Join(string(os.PathSeparator), "tmp", "scan-root")
	subDir := filepath.Join(root, "child")
	otherDir := filepath.Join(string(os.PathSeparator), "tmp", "other-root")

	vRoot := models.Video{Name: "root.mp4", Path: filepath.Join(root, "root.mp4"), Directory: root, Size: 1}
	vSub := models.Video{Name: "sub.mp4", Path: filepath.Join(subDir, "sub.mp4"), Directory: subDir, Size: 1}
	vOther := models.Video{Name: "other.mp4", Path: filepath.Join(otherDir, "other.mp4"), Directory: otherDir, Size: 1}

	if err := database.DB.Create(&vRoot).Error; err != nil {
		t.Fatalf("创建根目录视频失败: %v", err)
	}
	if err := database.DB.Create(&vSub).Error; err != nil {
		t.Fatalf("创建子目录视频失败: %v", err)
	}
	if err := database.DB.Create(&vOther).Error; err != nil {
		t.Fatalf("创建其他目录视频失败: %v", err)
	}

	videos, err := svc.GetVideosByDirectory(root)
	if err != nil {
		t.Fatalf("按目录查询失败: %v", err)
	}
	if len(videos) != 2 {
		t.Fatalf("期望返回根目录及子目录共2条，实际 %d 条", len(videos))
	}
}
