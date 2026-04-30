package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
	"video-master/services"

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

	if err := db.AutoMigrate(models.AllModels()...); err != nil {
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

func TestAITaggingReviewAPIsApproveCandidate(t *testing.T) {
	setupAppTestDB(t)
	tag := models.Tag{Name: "动作", Color: "#fff"}
	video := models.Video{Name: "fight.mp4", Path: "/tmp/fight.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	candidate := models.AITagCandidate{
		VideoID:        video.ID,
		SuggestedName:  "动作",
		NormalizedName: "动作",
		MatchedTagID:   &tag.ID,
		Confidence:     models.AITagConfidenceHigh,
		Status:         models.AITagCandidateStatusPending,
	}
	if err := database.DB.Create(&candidate).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}

	app := NewApp()
	candidates, err := app.ListAITagCandidates(0, "", "pending")
	if err != nil {
		t.Fatalf("列出候选失败: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != candidate.ID {
		t.Fatalf("候选列表错误: %#v", candidates)
	}
	if _, err := app.ApproveAITagCandidate(candidate.ID); err != nil {
		t.Fatalf("审批候选失败: %v", err)
	}
	var linkCount int64
	if err := database.DB.Table("video_tags").Where("video_id = ? AND tag_id = ?", video.ID, tag.ID).Count(&linkCount).Error; err != nil {
		t.Fatalf("统计正式关联失败: %v", err)
	}
	if linkCount != 1 {
		t.Fatalf("审批后应写入正式关联，实际 %d", linkCount)
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

func TestSubtitleAPIContractsCompile(t *testing.T) {
	app := NewApp()

	var getStatuses func() ([]services.SubtitleEngineStatus, error) = app.GetSubtitleEngineStatuses
	_ = getStatuses

	var prepare func(services.SubtitleEngine) error = app.PrepareSubtitleEngine
	_ = prepare

	req := services.SubtitleGenerateRequest{
		VideoID:    1,
		Engine:     services.SubtitleEngineWhisperX,
		SourceLang: "auto",
	}

	var generate func(services.SubtitleGenerateRequest) (*services.SubtitleGenerateResult, error) = app.GenerateSubtitle
	_ = generate

	var forceGenerate func(services.SubtitleGenerateRequest) (*services.SubtitleGenerateResult, error) = app.ForceGenerateSubtitle
	_ = forceGenerate

	result := &services.SubtitleGenerateResult{}
	result.Status = services.SubtitleResultStatusValidationFailed
	result.ValidationCode = services.SubtitleValidationCodeHallucinationDetected
	result.ForceEligible = true
	result.Engine = services.SubtitleEngineQwen
	result.SourceLang = req.SourceLang
	if result.Status != services.SubtitleResultStatusValidationFailed {
		t.Fatalf("结果状态错误: got=%s", result.Status)
	}
}

func TestBatchVideoAPIContractsCompile(t *testing.T) {
	app := NewApp()

	var batchDelete func([]uint, bool) *services.BatchVideoOperationResult = app.BatchDeleteVideos
	_ = batchDelete

	var batchAddTag func([]uint, uint) *services.BatchVideoOperationResult = app.BatchAddTagToVideos
	_ = batchAddTag

	var batchRemoveTag func([]uint, uint) *services.BatchVideoOperationResult = app.BatchRemoveTagFromVideos
	_ = batchRemoveTag

	var batchRefreshMetadata func([]uint) *services.BatchVideoOperationResult = app.BatchRefreshVideoMetadata
	_ = batchRefreshMetadata
}

func TestRedactSensitiveLogMessage(t *testing.T) {
	message := `{"deepl_api_key":"abc123:fx","nested":{"apiKey":"secret"},"Authorization":"Bearer token-value"}`
	redacted := redactSensitiveLogMessage(message)
	if strings.Contains(redacted, "abc123") || strings.Contains(redacted, "secret") || strings.Contains(redacted, "token-value") {
		t.Fatalf("敏感信息未被脱敏: %s", redacted)
	}
	if !strings.Contains(redacted, "[REDACTED]") {
		t.Fatalf("期望包含脱敏占位符: %s", redacted)
	}
}
