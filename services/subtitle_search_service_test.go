package services

import (
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSearchSubtitleMatchesFindsVideoBySegmentText(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	srtPath := filepath.Join(root, "movie.srt")

	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	if err := os.WriteFile(srtPath, []byte("1\n00:00:01,000 --> 00:00:03,000\nhello world\n"), 0644); err != nil {
		t.Fatalf("写入字幕文件失败: %v", err)
	}

	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	svc := &SubtitleSearchService{}
	matches, err := svc.SearchSubtitleMatches("world", 10)
	if err != nil {
		t.Fatalf("搜索字幕失败: %v", err)
	}

	if len(matches) != 1 {
		t.Fatalf("期望 1 条命中，实际 %d", len(matches))
	}
	if matches[0].Video.ID != video.ID {
		t.Fatalf("命中视频错误: got=%d want=%d", matches[0].Video.ID, video.ID)
	}
	if matches[0].Segment.Text != "hello world" {
		t.Fatalf("命中字幕文本错误: %q", matches[0].Segment.Text)
	}

	var indexedCount int64
	if err := database.DB.Model(&models.SubtitleSegment{}).Where("video_id = ?", video.ID).Count(&indexedCount).Error; err != nil {
		t.Fatalf("统计字幕索引失败: %v", err)
	}
	if indexedCount != 1 {
		t.Fatalf("期望首次搜索后建立 1 条字幕索引，实际 %d", indexedCount)
	}
}

func TestSearchSubtitleMatchesSkipsVideosWithoutSRT(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "nosub.mp4")

	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}

	video := models.Video{Name: "nosub.mp4", Path: videoPath, Directory: root, Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	svc := &SubtitleSearchService{}
	matches, err := svc.SearchSubtitleMatches("missing", 10)
	if err != nil {
		t.Fatalf("搜索字幕失败: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("期望 0 条命中，实际 %d", len(matches))
	}
}

func TestSearchSubtitleMatchesLimitsByUniqueVideos(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	root := t.TempDir()

	videoAPath := filepath.Join(root, "a.mp4")
	videoASrt := filepath.Join(root, "a.srt")
	videoBPath := filepath.Join(root, "b.mp4")
	videoBSrt := filepath.Join(root, "b.srt")

	if err := os.WriteFile(videoAPath, []byte("fake-video-a"), 0644); err != nil {
		t.Fatalf("写入视频A失败: %v", err)
	}
	if err := os.WriteFile(videoASrt, []byte("1\n00:00:01,000 --> 00:00:02,000\nhello world\n\n2\n00:00:03,000 --> 00:00:04,000\nworld again\n"), 0644); err != nil {
		t.Fatalf("写入字幕A失败: %v", err)
	}
	if err := os.WriteFile(videoBPath, []byte("fake-video-b"), 0644); err != nil {
		t.Fatalf("写入视频B失败: %v", err)
	}
	if err := os.WriteFile(videoBSrt, []byte("1\n00:00:01,000 --> 00:00:02,000\nworld in b\n"), 0644); err != nil {
		t.Fatalf("写入字幕B失败: %v", err)
	}

	videoA := models.Video{Name: "a.mp4", Path: videoAPath, Directory: root, Size: 10}
	videoB := models.Video{Name: "b.mp4", Path: videoBPath, Directory: root, Size: 11}
	if err := database.DB.Create(&videoA).Error; err != nil {
		t.Fatalf("创建视频A失败: %v", err)
	}
	if err := database.DB.Create(&videoB).Error; err != nil {
		t.Fatalf("创建视频B失败: %v", err)
	}

	svc := &SubtitleSearchService{}
	matches, err := svc.SearchSubtitleMatches("world", 2)
	if err != nil {
		t.Fatalf("搜索字幕失败: %v", err)
	}

	if len(matches) != 2 {
		t.Fatalf("期望返回 2 个视频级命中，实际 %d", len(matches))
	}
	if matches[0].Video.ID == matches[1].Video.ID {
		t.Fatalf("期望返回不同视频，实际重复返回同一视频: %d", matches[0].Video.ID)
	}
}

func TestSearchSubtitleMatchesRefreshesStaleIndex(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	srtPath := filepath.Join(root, "movie.srt")

	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	if err := os.WriteFile(srtPath, []byte("1\n00:00:01,000 --> 00:00:03,000\nold keyword\n"), 0644); err != nil {
		t.Fatalf("写入字幕文件失败: %v", err)
	}

	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	svc := &SubtitleSearchService{}
	if matches, err := svc.SearchSubtitleMatches("old", 10); err != nil || len(matches) != 1 {
		t.Fatalf("首次搜索失败 matches=%d err=%v", len(matches), err)
	}

	if err := os.WriteFile(srtPath, []byte("1\n00:00:02,000 --> 00:00:04,000\nnew keyword\n"), 0644); err != nil {
		t.Fatalf("改写字幕文件失败: %v", err)
	}

	matches, err := svc.SearchSubtitleMatches("new", 10)
	if err != nil {
		t.Fatalf("刷新后搜索失败: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("期望刷新后命中新字幕，实际 %d", len(matches))
	}
	if matches[0].Segment.Text != "new keyword" {
		t.Fatalf("期望命中新字幕文本，实际 %q", matches[0].Segment.Text)
	}
}

func setupSubtitleSearchTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "subtitle_search_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&models.Video{}, &models.SubtitleSegment{}, &models.SubtitleIndexState{}, &models.Tag{}, &models.Settings{}, &models.ScanDirectory{}); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	database.DB = db
}
