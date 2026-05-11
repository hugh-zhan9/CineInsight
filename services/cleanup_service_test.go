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

func TestAnalyzeCleanupCandidatesSkipsMissingFilesAndUsesFreshMetadata(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()

	ffprobeDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(ffprobeDir, 0755); err != nil {
		t.Fatalf("创建 ffprobe 目录失败: %v", err)
	}
	ffprobePath := filepath.Join(ffprobeDir, "ffprobe")
	ffprobeScript := `#!/bin/sh
cat <<'JSON'
{"streams":[{"width":1280,"height":720,"duration":"10.0"}],"format":{"duration":"10.0"}}
JSON
`
	if err := os.WriteFile(ffprobePath, []byte(ffprobeScript), 0755); err != nil {
		t.Fatalf("写入 ffprobe stub 失败: %v", err)
	}
	t.Setenv("PATH", ffprobeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	existingPath := filepath.Join(root, "existing.mp4")
	mustWriteSizedFile(t, existingPath, []byte("dummy"))

	existing := models.Video{Name: "existing.mp4", Path: existingPath, Directory: root, Size: 100, Duration: 2, Width: 320, Height: 240}
	missing := models.Video{Name: "missing.mp4", Path: filepath.Join(root, "missing.mp4"), Directory: root, Size: 100, Duration: 2, Width: 320, Height: 240}
	if err := database.DB.Create(&existing).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Create(&missing).Error; err != nil {
		t.Fatalf("创建缺失视频记录失败: %v", err)
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

	if len(result.LowDuration) != 0 {
		t.Fatalf("缺失文件或已刷新元数据后不应落入短视频，实际 %+v", result.LowDuration)
	}
	if len(result.LowResolution) != 0 {
		t.Fatalf("缺失文件或已刷新元数据后不应落入低清视频，实际 %+v", result.LowResolution)
	}
}

func TestCleanupBackgroundAnalysisKeepsStatusAfterCompletion(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)

	shortPath := filepath.Join(root, "short.mp4")
	mustWriteSizedFile(t, shortPath, []byte("short"))
	video := models.Video{Name: "short.mp4", Path: shortPath, Directory: root, Size: 5, Duration: 30, Width: 1280, Height: 720}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	svc := &CleanupService{}
	status, err := svc.StartAnalysis(CleanupCriteria{
		MinDuration: 5 * time.Second,
		MinWidth:    480,
		MinHeight:   320,
	})
	if err != nil {
		t.Fatalf("启动后台清理分析失败: %v", err)
	}
	if !status.Running || status.Completed {
		t.Fatalf("启动后状态错误: %+v", status)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status = svc.Status()
		if status.Completed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	status = svc.Status()
	if status.Running || !status.Completed || status.Error != "" {
		t.Fatalf("完成后状态错误: %+v", status)
	}
	if status.Analysis == nil || len(status.Analysis.LowDuration) != 1 {
		t.Fatalf("完成后应保留分析结果，实际 %+v", status.Analysis)
	}
}

func TestAnalyzeCleanupCandidatesFindsPhysicalDuplicates(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
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

func TestAnalyzeCleanupCandidatesSkipsNonVideoRecordsWithoutMetadata(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	firstPath := filepath.Join(root, "types.ts")
	secondPath := filepath.Join(root, "index.d.ts")
	sourceContent := []byte("export type A = string\n")
	mustWriteSizedFile(t, firstPath, sourceContent)
	mustWriteSizedFile(t, secondPath, sourceContent)

	first := models.Video{Name: "types.ts", Path: firstPath, Directory: root, Size: 23}
	second := models.Video{Name: "index.d.ts", Path: secondPath, Directory: root, Size: 23}
	if err := database.DB.Create(&first).Error; err != nil {
		t.Fatalf("创建 types.ts 记录失败: %v", err)
	}
	if err := database.DB.Create(&second).Error; err != nil {
		t.Fatalf("创建 index.d.ts 记录失败: %v", err)
	}

	svc := &CleanupService{}
	result, err := svc.AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析清理候选失败: %v", err)
	}
	if len(result.DuplicateGroups) != 0 {
		t.Fatalf("无法解析视频元数据的源码文件不应进入重复候选，实际 %+v", result.DuplicateGroups)
	}
}

func TestAnalyzeCleanupCandidatesFindsLowQualityVideos(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
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

func mockFFProbe(t *testing.T, root string) {
	t.Helper()
	ffprobeDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(ffprobeDir, 0755); err != nil {
		t.Fatalf("创建 ffprobe 目录失败: %v", err)
	}
	ffprobePath := filepath.Join(ffprobeDir, "ffprobe")
	script := `#!/bin/bash
target="${@: -1}"
name="$(basename "$target")"
case "$name" in
  short.mp4)
    duration="2.0"
    width=1280
    height=720
    ;;
  small.mp4)
    duration="30.0"
    width=320
    height=240
    ;;
  *)
    duration="12.0"
    width=1920
    height=1080
    ;;
esac
cat <<JSON
{"streams":[{"width":${width},"height":${height},"duration":"${duration}"}],"format":{"duration":"${duration}"}}
JSON
`
	if err := os.WriteFile(ffprobePath, []byte(script), 0755); err != nil {
		t.Fatalf("写入 ffprobe stub 失败: %v", err)
	}
	t.Setenv("PATH", ffprobeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
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
