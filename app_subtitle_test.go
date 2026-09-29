package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// MEDIA-04（复审 B 遗留）：对话框里的「强制生成」复用待确认的临时字幕重试收尾；写回仍失败时，
// App 返回带 error_code=subtitle_replace_failed、pending_retained=true 的结果而不是错误，
// 原字幕逐字节不变，临时文件与任务中心的待确认行都保留。
func TestMEDIA04ForceGenerateSubtitleMapsReplaceFailureToErrorCode(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2.0}).Error; err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "movie.mp4")
	srtPath := filepath.Join(dir, "movie.srt")
	pendingPath := filepath.Join(dir, ".movie.cineinsight-pending.srt")
	original := []byte("1\n00:00:00,000 --> 00:00:01,000\nhand-made\n")
	for path, content := range map[string][]byte{
		videoPath:   []byte("video"),
		srtPath:     original,
		pendingPath: []byte("1\n00:00:00,000 --> 00:00:01,000\nrecognized\n"),
	} {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: dir, Size: 5}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	job := models.SubtitleJob{
		VideoID: video.ID, Engine: string(services.SubtitleEngineWhisperX), SourceLang: "en",
		Status: string(services.SubtitleQueueTaskStatusNeedsConfirmation), PendingArtifactPath: pendingPath,
		OptionsJSON: `{"error_code":"subtitle_replace_failed","detected_lang":"en"}`,
	}
	if err := database.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	// 数据目录为空：覆盖已有字幕前无法备份，写回必然失败。
	app := &App{
		videoService:    &services.VideoService{},
		settingsService: &services.SettingsService{},
		subtitleService: services.NewSubtitleService(""),
	}

	result, err := app.ForceGenerateSubtitle(services.SubtitleGenerateRequest{VideoID: video.ID, Engine: services.SubtitleEngineWhisperX, SourceLang: "en"})
	if err != nil {
		t.Fatalf("写回失败应以结果返回，不是错误: %v", err)
	}
	if result.ErrorCode != services.SubtitleErrorReplaceFailed || !result.PendingRetained || !result.ForceEligible {
		t.Fatalf("写回失败应带错误码与 pending_retained: %+v", result)
	}
	if got, _ := os.ReadFile(srtPath); !bytes.Equal(got, original) {
		t.Fatalf("原字幕必须逐字节不变: %q", got)
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("临时字幕应保留以便再次重试: %v", err)
	}
	var jobs []models.SubtitleJob
	if err := database.DB.Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].Status != string(services.SubtitleQueueTaskStatusNeedsConfirmation) ||
		jobs[0].PendingArtifactPath != pendingPath {
		t.Fatalf("强制生成应沿用同一条待确认行并回到 needs_confirmation: %+v", jobs)
	}
}
