package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupAppTestDB(t *testing.T) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "app_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}

	if err := db.AutoMigrate(&models.Video{}, &models.Tag{}, &models.Settings{}, &models.ScanDirectory{}); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}

	database.DB = db
}

func TestGetSubtitleSegmentsReturnsStructuredSegments(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	srtPath := filepath.Join(root, "movie.srt")

	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	content := "1\n00:00:01,000 --> 00:00:03,500\nfirst line\nsecond line\n"
	if err := os.WriteFile(srtPath, []byte(content), 0644); err != nil {
		t.Fatalf("写入字幕文件失败: %v", err)
	}

	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	app := NewApp()
	segments, err := app.GetSubtitleSegments(video.ID)
	if err != nil {
		t.Fatalf("获取字幕片段失败: %v", err)
	}

	if len(segments) != 1 {
		t.Fatalf("期望 1 条字幕片段，实际 %d", len(segments))
	}
	if segments[0].Index != 1 {
		t.Fatalf("index 错误: got=%d want=1", segments[0].Index)
	}
	if segments[0].StartTimeMs != 1000 || segments[0].EndTimeMs != 3500 {
		t.Fatalf("时间范围错误: got=%d-%d want=1000-3500", segments[0].StartTimeMs, segments[0].EndTimeMs)
	}
	if segments[0].Text != "first line\nsecond line" {
		t.Fatalf("字幕文本错误: %q", segments[0].Text)
	}
	if len(segments[0].Lines) != 2 {
		t.Fatalf("期望保留 2 行，实际 %d", len(segments[0].Lines))
	}
}

func TestGetSubtitleSegmentsReturnsErrorWhenSubtitleMissing(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")

	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}

	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	app := NewApp()
	if _, err := app.GetSubtitleSegments(video.ID); err == nil {
		t.Fatalf("期望缺失字幕文件时返回错误")
	}
}

func TestGetSubtitleSegmentsReturnsEmptyWhenSubtitleMalformed(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	srtPath := filepath.Join(root, "movie.srt")

	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	if err := os.WriteFile(srtPath, []byte("1\n00:00:01 --> 00:00:03,000\nbroken\n"), 0644); err != nil {
		t.Fatalf("写入字幕文件失败: %v", err)
	}

	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	app := NewApp()
	segments, err := app.GetSubtitleSegments(video.ID)
	if err != nil {
		t.Fatalf("容错解析下不应因损坏字幕整体失败: %v", err)
	}
	if len(segments) != 0 {
		t.Fatalf("期望损坏字幕被跳过后返回 0 条，实际 %d", len(segments))
	}
}

func TestPreviewMediaHandlerServesInlineMedia(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	content := []byte("fake-preview-bytes")

	if err := os.WriteFile(videoPath, content, 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}

	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Size: int64(len(content))}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	app := NewApp()
	handler := newAssetHandler(app)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/preview/media/%d", video.ID), nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("content-type 错误: got=%s want=video/mp4", got)
	}
	if rec.Body.String() != string(content) {
		t.Fatalf("响应体错误: got=%q want=%q", rec.Body.String(), string(content))
	}
}
