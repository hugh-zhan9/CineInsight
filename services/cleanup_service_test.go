package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAnalyzeCleanupCandidatesFindsPhysicalDuplicates(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	a := filepath.Join(root, "a.mp4")
	b := filepath.Join(root, "b.mp4")
	mustWriteSizedFile(t, a, []byte("same-content"))
	mustWriteSizedFile(t, b, []byte("same-content"))

	v1 := models.Video{Name: "a.mp4", Path: a, Directory: root, Size: 12, Width: 1920, Height: 1080}
	v2 := models.Video{Name: "b.mp4", Path: b, Directory: root, Size: 12, Width: 1280, Height: 720}
	if err := database.DB.Create(&v1).Error; err != nil {
		t.Fatalf("创建视频1失败: %v", err)
	}
	if err := database.DB.Create(&v2).Error; err != nil {
		t.Fatalf("创建视频2失败: %v", err)
	}

	svc := &CleanupService{}
	result, err := svc.AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析清理候选失败: %v", err)
	}

	if len(result.DuplicateGroups) != 1 {
		t.Fatalf("期望 1 个重复组，实际 %d", len(result.DuplicateGroups))
	}
	group := result.DuplicateGroups[0]
	if group.Original.ID != v1.ID {
		t.Fatalf("期望更高分辨率视频为原件: got=%d want=%d", group.Original.ID, v1.ID)
	}
	if len(group.Candidates) != 1 || group.Candidates[0].ID != v2.ID {
		t.Fatalf("重复候选错误: %+v", group.Candidates)
	}
}

func TestAnalyzeCleanupCandidatesFindsLowQualityVideos(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	shortPath := filepath.Join(root, "short.mp4")
	smallPath := filepath.Join(root, "small.mp4")
	mustWriteSizedFile(t, shortPath, []byte("short"))
	mustWriteSizedFile(t, smallPath, []byte("small"))

	short := models.Video{Name: "short.mp4", Path: shortPath, Directory: root, Size: 5, Duration: 2, Width: 1280, Height: 720}
	small := models.Video{Name: "small.mp4", Path: smallPath, Directory: root, Size: 5, Duration: 30, Width: 320, Height: 240}
	if err := database.DB.Create(&short).Error; err != nil {
		t.Fatalf("创建短视频失败: %v", err)
	}
	if err := database.DB.Create(&small).Error; err != nil {
		t.Fatalf("创建低清视频失败: %v", err)
	}

	svc := &CleanupService{}
	result, err := svc.AnalyzeCleanupCandidates(CleanupCriteria{
		MinDuration: 5 * time.Second,
		MinWidth:    480,
		MinHeight:   320,
	})
	if err != nil {
		t.Fatalf("分析清理候选失败: %v", err)
	}

	if len(result.LowDuration) != 1 || result.LowDuration[0].ID != short.ID {
		t.Fatalf("短时长候选错误: %+v", result.LowDuration)
	}
	if len(result.LowResolution) != 1 || result.LowResolution[0].ID != small.ID {
		t.Fatalf("低分辨率候选错误: %+v", result.LowResolution)
	}
}

func mustWriteSizedFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}
}

func setupCleanupServiceTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cleanup_service_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	database.DB = db
}
