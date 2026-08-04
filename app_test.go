package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSubtitleGenerateOptionsUseIndependentTranslationConfig(t *testing.T) {
	settings := &models.Settings{
		BilingualEnabled:            true,
		BilingualLang:               "ja",
		SubtitleTranslationProvider: "llm",
		SubtitleTranslationBaseURL:  "http://127.0.0.1:1234/v1",
		SubtitleTranslationAPIKey:   "subtitle-key",
		SubtitleTranslationModel:    "subtitle-model",
		AITaggingBaseURL:            "https://tagging.example/v1",
		AITaggingAPIKey:             "tagging-key",
		AITaggingModel:              "vision-model",
	}

	options := subtitleGenerateOptionsFromSettings(settings, false)
	if !options.BilingualEnabled || options.BilingualLang != "ja" {
		t.Fatalf("双语字幕设置映射错误: %+v", options)
	}
	if options.TranslationConfig.Provider != "llm" ||
		options.TranslationConfig.BaseURL != "http://127.0.0.1:1234/v1" ||
		options.TranslationConfig.APIKey != "subtitle-key" ||
		options.TranslationConfig.Model != "subtitle-model" {
		t.Fatalf("字幕翻译未使用独立配置: %+v", options.TranslationConfig)
	}
	if options.TranslationConfig.BaseURL == settings.AITaggingBaseURL ||
		options.TranslationConfig.APIKey == settings.AITaggingAPIKey ||
		options.TranslationConfig.Model == settings.AITaggingModel {
		t.Fatalf("字幕翻译错误复用了 AI 标签配置: %+v", options.TranslationConfig)
	}
}

func TestAppLibraryWatcherSettingControlsLifecycleAndDirectoryCRUD(t *testing.T) {
	setupAppTestDB(t)
	settings := models.Settings{VideoExtensions: ".mp4", PlayWeight: 2, LibraryWatchEnabled: false}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}
	firstRoot := t.TempDir()
	app := NewApp()
	first, err := app.AddDirectory(firstRoot, "first")
	if err != nil {
		t.Fatalf("添加关闭状态下的目录失败: %v", err)
	}
	status := app.GetLibraryWatcherStatus()
	if status.Running || len(status.Roots) != 1 || status.Roots[0].State != services.LibraryWatchStateDisabled {
		t.Fatalf("关闭状态 = %#v", status)
	}

	settings.LibraryWatchEnabled = true
	if err := app.UpdateSettings(settings); err != nil {
		t.Fatalf("开启实时同步失败: %v", err)
	}
	status = app.GetLibraryWatcherStatus()
	if !status.Running || len(status.Roots) != 1 || status.Roots[0].DirectoryID != first.ID || status.Roots[0].State != services.LibraryWatchStateWatching {
		t.Fatalf("开启状态 = %#v", status)
	}

	secondRoot := t.TempDir()
	second, err := app.AddDirectory(secondRoot, "second")
	if err != nil {
		t.Fatalf("动态添加目录失败: %v", err)
	}
	status = app.GetLibraryWatcherStatus()
	if len(status.Roots) != 2 {
		t.Fatalf("动态添加后的状态 = %#v", status)
	}
	if err := app.DeleteDirectory(second.ID); err != nil {
		t.Fatalf("动态删除目录失败: %v", err)
	}
	if status = app.GetLibraryWatcherStatus(); len(status.Roots) != 1 {
		t.Fatalf("动态删除后的状态 = %#v", status)
	}

	settings.LibraryWatchEnabled = false
	if err := app.UpdateSettings(settings); err != nil {
		t.Fatalf("关闭实时同步失败: %v", err)
	}
	status = app.GetLibraryWatcherStatus()
	if status.Running || len(status.Roots) != 1 || status.Roots[0].State != services.LibraryWatchStateDisabled {
		t.Fatalf("关闭后的状态 = %#v", status)
	}
}

func TestAITaggingReviewAPIsApproveCandidate(t *testing.T) {
	setupAppTestDB(t)
	tag := models.Tag{Name: "动作", Color: "#fff", Namespace: "用户分类", IsSystem: true, IsActive: true}
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

func TestThumbnailHandlerServesGeneratedJPEG(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Size: 10, Duration: 30}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("创建 ffmpeg stub 目录失败: %v", err)
	}
	ffmpegPath := filepath.Join(binDir, "ffmpeg")
	ffmpegScript := "#!/bin/bash\ndestination=\"${@: -1}\"\nprintf 'jpeg-thumbnail' > \"$destination\"\n"
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0755); err != nil {
		t.Fatalf("写入 ffmpeg stub 失败: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	app := NewApp()
	app.thumbnailService = services.NewThumbnailService(app.videoService, root)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/preview/thumbnail/%d", video.ID), nil)
	rec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("content-type 错误: got=%s want=image/jpeg", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=300" {
		t.Fatalf("cache-control 错误: %q", got)
	}
	if rec.Body.String() != "jpeg-thumbnail" {
		t.Fatalf("缩略图响应体错误: %q", rec.Body.String())
	}
}

func TestSeekSpriteHandlerServesGeneratedJPEG(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Size: 10, Duration: 60}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("创建 ffmpeg stub 目录失败: %v", err)
	}
	ffmpegPath := filepath.Join(binDir, "ffmpeg")
	ffmpegScript := "#!/bin/bash\ndestination=\"${@: -1}\"\nprintf 'jpeg-seek-sprite' > \"$destination\"\n"
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0755); err != nil {
		t.Fatalf("写入 ffmpeg stub 失败: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	app := NewApp()
	app.thumbnailService = services.NewThumbnailService(app.videoService, root)
	handler := newAssetHandler(app)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/preview/seek-sprite/%d", video.ID), nil))
	if first.Code != http.StatusNotFound {
		t.Fatalf("首次请求应返回 404 并转入后台生成，实际 %d body=%s", first.Code, first.Body.String())
	}

	var rec *httptest.ResponseRecorder
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/preview/seek-sprite/%d", video.ID), nil))
		if rec.Code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("后台生成完成后应返回 200，实际 %d body=%s", rec.Code, rec.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("content-type 错误: got=%s want=image/jpeg", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=300" {
		t.Fatalf("cache-control 错误: %q", got)
	}
	if rec.Body.String() != "jpeg-seek-sprite" {
		t.Fatalf("seek sprite 响应体错误: %q", rec.Body.String())
	}
}

func TestPersonAvatarHandlerServesOnlyManagedEntityAsset(t *testing.T) {
	setupAppTestDB(t)
	dataDir := t.TempDir()
	app := NewApp()
	app.personService = services.NewPersonService(dataDir)
	person, err := app.personService.CreatePerson("Handler Person", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	source := filepath.Join(t.TempDir(), "avatar.png")
	content := append([]byte("\x89PNG\r\n\x1a\n"), []byte("handler-image")...)
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatalf("写入头像源文件失败: %v", err)
	}
	if _, err := app.personService.SetPersonAvatar(person.ID, source); err != nil {
		t.Fatalf("设置人物头像失败: %v", err)
	}

	path := fmt.Sprintf("/preview/person-avatar/%d", person.ID)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != string(content) {
		t.Fatalf("人物头像响应错误: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("人物头像 Content-Type=%q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("人物头像 Cache-Control=%q", got)
	}

	post := httptest.NewRequest(http.MethodPost, path, nil)
	postRec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(postRec, post)
	if postRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("人物头像非 GET/HEAD 应返回 405，实际=%d", postRec.Code)
	}

	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, content, 0600); err != nil {
		t.Fatalf("写入外部文件失败: %v", err)
	}
	if err := database.DB.Model(&models.Person{}).Where("id = ?", person.ID).Update("avatar_path", outside).Error; err != nil {
		t.Fatalf("构造越界数据库路径失败: %v", err)
	}
	traversalReq := httptest.NewRequest(http.MethodGet, path, nil)
	traversalRec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(traversalRec, traversalReq)
	if traversalRec.Code != http.StatusNotFound {
		t.Fatalf("绝对/越界数据库路径必须拒绝，实际=%d body=%q", traversalRec.Code, traversalRec.Body.String())
	}
}

func TestCollectionCoverHandlerServesManagedAssetAndRejectsOtherMethods(t *testing.T) {
	setupAppTestDB(t)
	dataDir := t.TempDir()
	app := NewApp()
	app.collectionService = services.NewCollectionService(dataDir)
	collection, err := app.collectionService.CreateCollection("Handler Collection", "")
	if err != nil {
		t.Fatalf("创建作品集失败: %v", err)
	}
	source := filepath.Join(t.TempDir(), "cover.png")
	content := append([]byte("\x89PNG\r\n\x1a\n"), []byte("collection-cover")...)
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatalf("写入作品集封面失败: %v", err)
	}
	if _, err := app.collectionService.SetCollectionCover(collection.ID, source); err != nil {
		t.Fatalf("设置作品集封面失败: %v", err)
	}

	path := fmt.Sprintf("/preview/collection-cover/%d", collection.ID)
	rec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, path, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("作品集封面 HEAD 响应错误: status=%d content-type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("作品集封面 Cache-Control=%q", got)
	}
	postRec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(postRec, httptest.NewRequest(http.MethodPost, path, nil))
	if postRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("作品集封面非 GET/HEAD 应返回 405，实际=%d", postRec.Code)
	}
}

func TestMediaDetailAppMethodsReturnAggregatedLocalDetail(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{PlayWeight: 2}).Error; err != nil {
		t.Fatalf("创建片库设置失败: %v", err)
	}
	video := models.Video{Name: "app-detail.mkv", Path: filepath.Join(t.TempDir(), "app-detail.mkv"), DisplayTitle: "App Detail"}
	if err := os.WriteFile(video.Path, []byte("video"), 0600); err != nil {
		t.Fatalf("创建详情视频失败: %v", err)
	}
	video.Directory = filepath.Dir(video.Path)
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建详情视频记录失败: %v", err)
	}
	app := NewApp()
	detail, err := app.GetVideoDetails(video.ID)
	if err != nil {
		t.Fatalf("App 获取视频详情失败: %v", err)
	}
	if detail.EffectiveTitle != "App Detail" || detail.TechnicalStatus.State != services.TechnicalStateUnprobed {
		t.Fatalf("App 视频详情错误: %#v", detail)
	}
	page, err := app.SearchLibraryVideoPage(services.LibraryVideoPageRequest{Limit: 20})
	if err != nil || len(page.Videos) != 1 || page.Videos[0].ID != video.ID {
		t.Fatalf("App 新片库分页接口错误: page=%#v err=%v", page, err)
	}
}

func TestTechnicalBackfillAppRemainsIdleUntilExplicitStart(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{ShortFeedMaxDurationMinutes: services.DefaultShortFeedMaxDurationMinutes}).Error; err != nil {
		t.Fatalf("创建补全测试设置失败: %v", err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "backfill.mkv")
	if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
		t.Fatalf("创建补全视频失败: %v", err)
	}
	video := models.Video{Name: "backfill.mkv", Path: path, Directory: root}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建补全视频记录失败: %v", err)
	}
	app := NewApp()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("创建 ffprobe stub 目录失败: %v", err)
	}
	marker := filepath.Join(root, "ffprobe-calls")
	script := "#!/bin/sh\nprintf x >> \"$CINEINSIGHT_PROBE_MARKER\"\nprintf '{\"streams\":[],\"format\":{\"format_name\":\"matroska\"}}'\n"
	if err := os.WriteFile(filepath.Join(binDir, "ffprobe"), []byte(script), 0755); err != nil {
		t.Fatalf("写入 ffprobe stub 失败: %v", err)
	}
	t.Setenv("CINEINSIGHT_PROBE_MARKER", marker)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	probe := services.NewMediaProbeService()
	app.mediaProbeService = probe
	app.technicalBackfill = services.NewTechnicalBackfillService(probe)
	if status := app.GetTechnicalBackfillStatus(); status.Running {
		t.Fatalf("显式启动前不得补全: status=%#v", status)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("显式启动前 ffprobe 不应执行: err=%v", err)
	}
	if _, err := app.StartTechnicalBackfill(); err != nil {
		t.Fatalf("App 启动技术补全失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for app.GetTechnicalBackfillStatus().Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	status := app.GetTechnicalBackfillStatus()
	markerContent, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("读取 ffprobe 调用标记失败: %v", err)
	}
	if !status.Completed || status.Succeeded != 1 || len(markerContent) != 1 {
		t.Fatalf("显式技术补全结果错误: status=%#v calls=%d", status, len(markerContent))
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

func TestTrashRestoreAPIContracts(t *testing.T) {
	app := NewApp()
	type trashRestoreAPI interface {
		ListTrashEntries() ([]models.VideoTrashEntry, error)
		RestoreTrashEntry(uint) (*models.Video, error)
	}
	if _, ok := any(app).(trashRestoreAPI); !ok {
		t.Fatalf("App 应暴露回收站列表与恢复 API")
	}
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
