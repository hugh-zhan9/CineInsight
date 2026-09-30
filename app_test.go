package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
	"video-master/services"
)

func setupAppTestDB(t *testing.T) {
	t.Helper()

	db := dbtest.Open(t)

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

func TestAppLibraryWatcherAppliesSavedExclusionsWithoutRestart(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	excluded := filepath.Join(root, "hugh_")
	if err := os.MkdirAll(filepath.Join(excluded, "child"), 0755); err != nil {
		t.Fatal(err)
	}
	settings := models.Settings{VideoExtensions: ".mp4", PlayWeight: 2, LibraryWatchEnabled: true, ScanExcludePaths: excluded}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	defer app.libraryWatcher.Close()
	if err := app.configureLibraryWatcher(true); err != nil {
		t.Fatal(err)
	}
	assertWatchCount := func(want int) {
		t.Helper()
		status := app.GetLibraryWatcherStatus()
		if !status.Running || len(status.Roots) != 1 || status.Roots[0].State != services.LibraryWatchStateWatching || status.Roots[0].WatchCount != want {
			t.Fatalf("want %d directory watches, got %+v", want, status)
		}
	}
	assertWatchCount(1)
	settings.ScanExcludePaths = ""
	if err := app.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		assertWatchCount(1) // FSEvents registers the entire root tree once.
	} else {
		assertWatchCount(3)
	}
	settings.ScanExcludePaths = excluded
	if err := app.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	assertWatchCount(1)
	settings.ScanExcludePaths = root
	if err := app.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	if status := app.GetLibraryWatcherStatus(); status.Roots[0].ReasonCode != "excluded" || status.Roots[0].WatchCount != 0 {
		t.Fatalf("excluding the root must release its watcher: %+v", status)
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

// 删掉扫描目录：记录标失效、从默认列表消失、不进回收站、磁盘文件不动；
// 把同一路径加回来：记录自动恢复，标签评分观看进度原样保留（D-S01..D-S03）。
func TestDeleteDirectoryMarksStaleAndAddBackRestores(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}
	root := t.TempDir()
	moviePath := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(moviePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("造视频文件失败: %v", err)
	}
	dir := models.ScanDirectory{Path: root, Alias: "lib"}
	if err := database.DB.Create(&dir).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}
	rating := 8.5
	video := models.Video{
		Name: "movie.mp4", Path: moviePath, Directory: root,
		IsFavorite: true, PersonalRating: &rating, WatchPositionSeconds: 42,
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	app := NewApp()
	if err := app.DeleteDirectory(dir.ID); err != nil {
		t.Fatalf("删除扫描目录失败: %v", err)
	}

	var afterDelete models.Video
	if err := database.DB.First(&afterDelete, video.ID).Error; err != nil {
		t.Fatalf("删目录后记录应当还在: %v", err)
	}
	if !afterDelete.IsStale {
		t.Fatal("删目录后该目录下的视频应当标为失效")
	}
	// 磁盘文件不动、回收站不进条目
	if _, err := os.Stat(moviePath); err != nil {
		t.Fatalf("磁盘文件不该被动: %v", err)
	}
	var trashCount int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Count(&trashCount).Error; err != nil {
		t.Fatalf("统计回收站失败: %v", err)
	}
	if trashCount != 0 {
		t.Fatalf("删目录不该产生回收站条目，实际 %d 条", trashCount)
	}
	// 默认列表里看不到它了
	listed, err := app.videoService.SearchLibraryVideos(services.LibraryFilter{}, 0, 0, 0, 20)
	if err != nil {
		t.Fatalf("查询片库失败: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("失效记录不该出现在默认列表里，实际 %+v", listed)
	}

	// 把同一个路径加回来：后台恢复扫描应当把它接回来
	if _, err := app.AddDirectory(root, "lib again"); err != nil {
		t.Fatalf("重新添加扫描目录失败: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var restored models.Video
	for time.Now().Before(deadline) {
		if err := database.DB.First(&restored, video.ID).Error; err != nil {
			t.Fatalf("读取视频失败: %v", err)
		}
		if !restored.IsStale {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if restored.IsStale {
		t.Fatal("把路径加回来之后记录应当自动恢复")
	}
	// 元数据一个都不能丢
	if !restored.IsFavorite || restored.PersonalRating == nil || *restored.PersonalRating != 8.5 || restored.WatchPositionSeconds != 42 {
		t.Fatalf("恢复后元数据应当原样保留: %+v", restored)
	}
	listed, err = app.videoService.SearchLibraryVideos(services.LibraryFilter{}, 0, 0, 0, 20)
	if err != nil {
		t.Fatalf("查询片库失败: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != video.ID {
		t.Fatalf("恢复后应当回到默认列表，实际 %+v", listed)
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
	page, err := app.ListAITagCandidatePage(0, "", "pending", 0, 0)
	if err != nil {
		t.Fatalf("列出候选失败: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != candidate.ID {
		t.Fatalf("候选列表错误: %#v", page.Items)
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

	var batchAddTag func([]uint, uint) *services.BatchVideoOperationResult = app.BatchAddTagToVideos
	_ = batchAddTag

	var batchRemoveTag func([]uint, uint) *services.BatchVideoOperationResult = app.BatchRemoveTagFromVideos
	_ = batchRemoveTag
}

func TestTrashRestoreAPIContracts(t *testing.T) {
	app := NewApp()
	type trashRestoreAPI interface {
		ListTrashEntries(services.TrashFilter) (*services.TrashPage, error)
		RestoreTrashEntries(string, []uint) (*services.BatchResult, error)
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

// 扫描后的自动任务：默认全关，开了才跑；库没变化时一件都不做。
func TestPostScanAutomationRespectsSettingsAndSkipsUnchangedLibrary(t *testing.T) {
	setupAppTestDB(t)
	settings := models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}
	app := NewApp()

	// 库没有任何变化：即使开着开关也不该启动后台任务
	settings.AutoTechnicalBackfill = true
	settings.AutoPerceptualHash = true
	settings.AutoCleanupAnalysis = true
	if err := app.UpdateSettings(settings); err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}
	app.runPostScanAutomation(&services.ScanSyncResult{Scanned: 12})
	if app.technicalBackfill.Status().Running || app.perceptualHash.Status().Running {
		t.Fatal("库没变化时不该启动任何后台任务")
	}

	// 全部关掉：库有变化也不该自动跑
	settings.AutoTechnicalBackfill = false
	settings.AutoPerceptualHash = false
	settings.AutoCleanupAnalysis = false
	if err := app.UpdateSettings(settings); err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}
	app.runPostScanAutomation(&services.ScanSyncResult{Added: 3})
	time.Sleep(50 * time.Millisecond)
	if app.technicalBackfill.Status().Running || app.perceptualHash.Status().Running {
		t.Fatal("开关关着时不该自动启动后台任务")
	}
}

// 扫描后自动 pHash 经空闲门（AC-20）：用户活跃时任务停在「等待空闲」，
// 模拟空闲之后才开始；用户显式点「补全」永远不排队。
func TestPostScanAutomationGatesAutomaticPerceptualHashOnly(t *testing.T) {
	setupAppTestDB(t)
	settings := models.Settings{
		VideoExtensions: ".mp4", PlayWeight: 2,
		IdleSchedulingEnabled: true, IdleThresholdMinutes: 5,
	}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}
	app := NewApp()
	settings.AutoPerceptualHash = true
	if err := app.UpdateSettings(settings); err != nil {
		t.Fatalf("打开自动感知哈希失败: %v", err)
	}
	var probeMu sync.Mutex
	idle := 10 * time.Second
	app.idleGate.SetProbe(func(context.Context) (services.IdleSample, error) {
		probeMu.Lock()
		defer probeMu.Unlock()
		return services.IdleSample{Idle: idle, OnACPower: true}, nil
	})
	app.idleGate.SetProbeInterval(5 * time.Millisecond)

	app.runPostScanAutomation(&services.ScanSyncResult{Added: 1})

	// 用户活跃：任务应停在门口，pHash 一直没启动。
	waitForIdleGateWaiting(t, app, "phash")
	if app.perceptualHash.Status().Running {
		t.Fatal("用户活跃时不该启动自动感知哈希")
	}

	// 显式启动不经门：立刻就跑起来（库里没有视频，随即正常收尾）。
	if _, err := app.StartPerceptualHashBackfill(); err != nil {
		t.Fatalf("显式启动感知哈希失败: %v", err)
	}
	status := app.perceptualHash.Status()
	if !status.Running && !status.Completed {
		t.Fatalf("显式启动应立即执行，实际 %+v", status)
	}
	if status.Gate.WaitingIdle {
		t.Fatalf("显式启动不该被空闲门挡住: %+v", status.Gate)
	}
	waitForPerceptualHashIdle(t, app)

	// 模拟机器空闲：门放行，自动任务开始（无视频时会立刻跑完）。
	probeMu.Lock()
	idle = 30 * time.Minute
	probeMu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(app.idleGate.GetIdleSchedulerStatus().Waiting) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("空闲之后自动任务应被放行，实际仍在等待: %+v",
		app.idleGate.GetIdleSchedulerStatus().Waiting)
}

func waitForIdleGateWaiting(t *testing.T, app *App, taskKey string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, waiting := range app.idleGate.GetIdleSchedulerStatus().Waiting {
			if waiting.TaskKey == taskKey {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %s 进入空闲门等待清单超时", taskKey)
}

func waitForPerceptualHashIdle(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !app.perceptualHash.Status().Running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("显式感知哈希任务未在预期时间内结束")
}

// 登记表接线：任务跑起来时 background-tasks 里出现对应 key，结束后清空。
func TestBackgroundTaskRegistryReflectsExplicitPerceptualHashRun(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	seen := make(chan []string, 32)
	app.backgroundTasks.SetOnChange(func(running []string) {
		select {
		case seen <- running:
		default:
		}
	})
	if _, err := app.StartPerceptualHashBackfill(); err != nil {
		t.Fatalf("启动感知哈希失败: %v", err)
	}
	waitForPerceptualHashIdle(t, app)
	if tasks := app.GetBackgroundTasks(); len(tasks) != 0 {
		t.Fatalf("任务结束后登记表应清空: %v", tasks)
	}
	sawRunning := false
	for {
		select {
		case running := <-seen:
			for _, key := range running {
				if key == "phash" {
					sawRunning = true
				}
			}
			continue
		default:
		}
		break
	}
	if !sawRunning {
		t.Fatal("任务运行期间 background-tasks 应包含 phash")
	}
}

// 扫描扫到新片后的 AI 打标唤醒是扫描的自动后果，与扫描后自动任务同一口径：
// 用户活跃时唤醒停在空闲门后（AC-20），机器空闲后才放行。
func TestScanSyncGatesAutomaticAITaggingWake(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	// 扫描会跳过 5 分钟内改动过的文件（可能正在写入），把 mtime 拨回去。
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(videoPath, past, past); err != nil {
		t.Fatalf("回拨文件时间失败: %v", err)
	}
	settings := models.Settings{
		VideoExtensions: ".mp4", PlayWeight: 2,
		IdleSchedulingEnabled: true, IdleThresholdMinutes: 5,
	}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: root, Alias: "扫描根"}).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}

	app := NewApp()
	var probeMu sync.Mutex
	idle := 10 * time.Second
	app.idleGate.SetProbe(func(context.Context) (services.IdleSample, error) {
		probeMu.Lock()
		defer probeMu.Unlock()
		return services.IdleSample{Idle: idle, OnACPower: true}, nil
	})
	app.idleGate.SetProbeInterval(5 * time.Millisecond)

	result, err := app.SyncScanDirectories(services.ScanTriggerManual)
	if err != nil {
		t.Fatalf("扫描同步失败: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("应新增 1 个视频，实际 %+v", result)
	}

	waitForIdleGateWaiting(t, app, "ai_tagging")

	// 空闲之后唤醒被放行（worker 没起来，Trigger 什么也不做，等待清单清空即可）。
	probeMu.Lock()
	idle = 30 * time.Minute
	probeMu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(app.idleGate.GetIdleSchedulerStatus().Waiting) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("空闲之后唤醒应被放行，实际仍在等待: %+v",
		app.idleGate.GetIdleSchedulerStatus().Waiting)
}

// 前后台上报（D-013）：前端调 SetWindowForeground，后端的通知中心随之改标记。
// 没上报过时默认后台，所以初始就是 false。
func TestSetWindowForegroundUpdatesNotificationCenter(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()

	if app.desktopNotify.Foreground() {
		t.Fatal("未上报前后台时应默认视为后台")
	}
	app.SetWindowForeground(true)
	if !app.desktopNotify.Foreground() {
		t.Fatal("上报前台后标记应为前台")
	}
	app.SetWindowForeground(false)
	if app.desktopNotify.Foreground() {
		t.Fatal("上报后台后标记应回到后台")
	}
}

// 通知开关直接从库里读，保存即时生效（设计 6.2）；读不到设置就当没开。
func TestDesktopNotificationsEnabledReadsCurrentSettings(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()

	if app.desktopNotificationsEnabled() {
		t.Fatal("读不到设置行时不该认为开关是开的")
	}
	settings := models.Settings{DesktopNotificationsEnabled: true}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatalf("创建设置行失败: %v", err)
	}
	if !app.desktopNotificationsEnabled() {
		t.Fatal("开关开着时应读到开启")
	}

	settings.DesktopNotificationsEnabled = false
	if err := app.settingsService.UpdateSettings(settings); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	if app.desktopNotificationsEnabled() {
		t.Fatal("关掉开关后应立即读到关闭")
	}
}

// 走 App 层删除图片目录：该目录下的图片应当从图库消失（记录仍在库里）。
func TestDeleteImageDirectoryHidesItsImages(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}
	root := t.TempDir()
	photo := filepath.Join(root, "p.jpg")
	if err := os.WriteFile(photo, []byte("jpeg"), 0o644); err != nil {
		t.Fatalf("造图片失败: %v", err)
	}
	app := NewApp()
	dir, err := app.AddImageDirectory(root, "photos")
	if err != nil {
		t.Fatalf("添加图片目录失败: %v", err)
	}
	if _, err := app.SyncImageDirectories(); err != nil {
		t.Fatalf("图片对账失败: %v", err)
	}
	var before int64
	database.DB.Model(&models.Image{}).Count(&before)
	if before != 1 {
		t.Fatalf("应当先收录 1 张，实际 %d", before)
	}

	if err := app.DeleteImageDirectory(dir.ID); err != nil {
		t.Fatalf("删除图片目录失败: %v", err)
	}

	// 图库列表里看不到它了。这里查的是照片页本身，而不是 images 表的行数：
	// 删除扫描目录只标 is_stale 不软删，行本来就该留着，能不能恢复全靠它。
	page, err := app.SearchImagePage(services.ImagePageRequest{Limit: 20})
	if err != nil {
		t.Fatalf("查询照片页失败: %v", err)
	}
	if len(page.Images) != 0 {
		t.Fatalf("删除图片目录后该目录下的图片应当从图库消失，实际还有 %d 张", len(page.Images))
	}
	groups, err := app.ListImageFolderGroups(services.ImageFilter{})
	if err != nil {
		t.Fatalf("查询文件夹图集失败: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("删除图片目录后不该再有文件夹图集: %+v", groups)
	}

	// 记录仍在库里，且带着 is_stale 这枚恢复用的标记。
	var afterDelete models.Image
	if err := database.DB.First(&afterDelete, "directory = ?", root).Error; err != nil {
		t.Fatalf("删目录后图片记录应当还在: %v", err)
	}
	if !afterDelete.IsStale {
		t.Fatal("删目录后该目录下的图片应当标为失效，否则目录加回来不会自动恢复")
	}
	if _, err := os.Stat(photo); err != nil {
		t.Fatalf("磁盘文件不该被动: %v", err)
	}
}

// ===== P-029 接线（产品完善度批次） =====

// APP-05：已看状态双向同步的两处注入都接在 App 上。视频 → 榜单的观察者按调用时取当前的榜单
// 服务，所以重建之后照样生效；榜单 → 视频的回写口接在重建出来的新实例上；IINA 同步的已看
// 翻转经 VideoService 转发给同一个观察者。
func TestNewAppWiresWatchStateObserversAcrossMovieChartRebuildAPP05(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setupAppTestDB(t)
	seedMovieChartBindingSettings(t, movieChartBindingInvalidProxy)
	root := t.TempDir()
	first := models.Video{Name: "first.mp4", Path: filepath.Join(root, "first.mp4"), Directory: root, Size: 1, Duration: 28}
	second := models.Video{Name: "second.mp4", Path: filepath.Join(root, "second.mp4"), Directory: root, Size: 1, Duration: 28}
	for _, video := range []*models.Video{&first, &second} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
	}
	createChartEntryForTest(t, "1000001", "")
	createChartEntryForTest(t, "1000002", "")

	app := NewApp()
	app.rebuildMovieChartService()
	t.Cleanup(func() {
		if svc := app.movieChartService(); svc != nil {
			svc.StopRefreshAndWait()
		}
	})
	if err := app.LinkMovieToVideo("1000001", first.ID); err != nil {
		t.Fatalf("关联第一部失败: %v", err)
	}
	if err := app.LinkMovieToVideo("1000002", second.ID); err != nil {
		t.Fatalf("关联第二部失败: %v", err)
	}
	markOf := func(doubanID string) string {
		t.Helper()
		var mark models.MovieChartMark
		if err := database.DB.Where("douban_id = ?", doubanID).First(&mark).Error; err != nil {
			return ""
		}
		return mark.Mark
	}
	watchedOf := func(videoID uint) bool {
		t.Helper()
		var video models.Video
		if err := database.DB.First(&video, videoID).Error; err != nil {
			t.Fatalf("读取视频失败: %v", err)
		}
		return video.IsWatched
	}

	// 视频 → 榜单。
	if _, err := app.SetVideoWatched(first.ID, true); err != nil {
		t.Fatalf("标已看失败: %v", err)
	}
	if got := markOf("1000001"); got != models.MovieChartMarkWatched {
		t.Fatalf("视频标已看后榜单应标已看，实际 %q", got)
	}

	// 重建榜单服务（恢复失败续跑同样会重建）之后，两个方向都跟到新实例上。
	app.rebuildMovieChartService()
	if _, err := app.SetVideoWatched(first.ID, false); err != nil {
		t.Fatalf("取消已看失败: %v", err)
	}
	if got := markOf("1000001"); got != "" {
		t.Fatalf("重建后取消已看应撤销榜单标记，实际 %q", got)
	}
	// 榜单 → 视频。
	if _, err := app.MarkMovieChartEntry("1000001", models.MovieChartMarkWatched); err != nil {
		t.Fatalf("榜单标已看失败: %v", err)
	}
	if !watchedOf(first.ID) {
		t.Fatal("榜单标已看后关联视频应标已看（重建后的实例没接回写口）")
	}

	// IINA 判看完 → VideoService 转发 → 榜单。
	watchLater := filepath.Join(home, "Library", "Application Support", "com.colliderli.iina", "watch_later")
	if err := os.MkdirAll(watchLater, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := md5.Sum([]byte(second.Path))
	name := strings.ToUpper(hex.EncodeToString(digest[:]))
	if err := os.WriteFile(filepath.Join(watchLater, name), []byte("start=27.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.iinaProgress.Sync(); err != nil {
		t.Fatalf("IINA 同步失败: %v", err)
	}
	if !watchedOf(second.ID) {
		t.Fatal("IINA 断点接近片尾应判看完")
	}
	if got := markOf("1000002"); got != models.MovieChartMarkWatched {
		t.Fatalf("IINA 判看完后榜单应标已看，实际 %q", got)
	}
}

// APP-04：AI 打标 worker 的启动批次经空闲门。用户活跃时它停在门口（出现在等待清单里），
// 不接空闲门的话启动批次会直接执行、清单里什么都没有。
func TestNewAppGatesAITaggingWorkerStartupBatchAPP04(t *testing.T) {
	setupAppTestDB(t)
	settings := models.Settings{VideoExtensions: ".mp4", PlayWeight: 2, IdleSchedulingEnabled: true, IdleThresholdMinutes: 5}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}
	app := NewApp()
	app.idleGate.SetProbe(func(context.Context) (services.IdleSample, error) {
		return services.IdleSample{Idle: 10 * time.Second, OnACPower: true}, nil
	})
	app.idleGate.SetProbeInterval(5 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		app.aiTaggingService.StopAndWait()
		// 门口的等待随 worker 的上下文一起撤走；等它真的离开，免得拖到下一个用例的数据库上。
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && len(app.idleGate.GetIdleSchedulerStatus().Waiting) > 0 {
			time.Sleep(5 * time.Millisecond)
		}
	})
	app.aiTaggingService.Start(ctx)
	waitForIdleGateWaiting(t, app, string(services.BackgroundTaskAITagging))
}

// IMG-12：图片清理分析登记为 image_cleanup，任务中心与退出判定才看得到它。
func TestNewAppRegistersImageCleanupAnalysisIMG12(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	var mu sync.Mutex
	seen := false
	app.backgroundTasks.SetOnChange(func(running []string) {
		mu.Lock()
		defer mu.Unlock()
		for _, key := range running {
			if key == string(services.BackgroundTaskImageCleanup) {
				seen = true
			}
		}
	})
	if _, err := app.StartImageCleanupAnalysis(); err != nil {
		t.Fatalf("启动图片清理分析失败: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && app.imageCleanupService.GetImageCleanupStatus().Running {
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen {
		t.Fatal("图片清理分析运行期间登记表应包含 image_cleanup")
	}
}

// META-02：撤销「标签转人物」删掉新建人物时，头像文件一并删掉。直接调服务方法，
// 绕开 App 绑定里的兜底注入：证明注入在构造时就已完成。
func TestNewAppInjectsAvatarRemoverForConversionUndoMETA02(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupAppTestDB(t)
	app := NewApp()
	tag, err := app.CreateTagWithCategory("王五", "", "人物")
	if err != nil {
		t.Fatalf("创建人物标签失败: %v", err)
	}
	converted, err := app.ConvertTagToPerson(services.TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, CreateNew: true})
	if err != nil {
		t.Fatalf("标签转人物失败: %v", err)
	}
	source := filepath.Join(t.TempDir(), "avatar.png")
	writeP029TestPNG(t, source)
	person, err := app.SetPersonAvatar(converted.Person.ID, source)
	if err != nil || person.AvatarPath == "" {
		t.Fatalf("设置头像失败: %+v err=%v", person, err)
	}
	asset, err := app.personService.ResolvePersonAvatar(person.ID)
	if err != nil {
		t.Fatalf("解析托管头像失败: %v", err)
	}
	if _, err := os.Stat(asset.Path); err != nil {
		t.Fatalf("托管头像应存在: %v", err)
	}
	undo, err := app.tagService.UndoTagPersonConversion(converted.ConversionID)
	if err != nil || !undo.PersonDeleted {
		t.Fatalf("撤销应删掉新建的人物: %+v err=%v", undo, err)
	}
	if _, err := os.Stat(asset.Path); !os.IsNotExist(err) {
		t.Fatalf("撤销删掉人物后头像文件应一并删除: %v", err)
	}
}

func writeP029TestPNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}

// PLAY-12 / APP-03：startup 的钩子。播放失败后后台重定位成功发 video-relocated；登记表、
// 空闲门与后台任务自己的状态事件都会触发一次（合并后的）task-center-changed。
func TestWireStartupHooksEmitsRelocationAndTaskCenterChangesPLAY12APP03(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2, IdleSchedulingEnabled: true, IdleThresholdMinutes: 5}).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root, Alias: "root"}).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}
	moved := filepath.Join(root, "moved", "movie.mp4")
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(moved, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(moved, past, past); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "movie.mp4", Path: filepath.Join(root, "old", "movie.mp4"), Directory: filepath.Join(root, "old"), Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	runtimeEvents := stubRuntimeEvents(t)
	previousDelay := taskCenterChangeDelay
	taskCenterChangeDelay = 10 * time.Millisecond
	t.Cleanup(func() { taskCenterChangeDelay = previousDelay })

	var mu sync.Mutex
	emitted := []recordedEvent{}
	emit := func(event string, data any) {
		mu.Lock()
		defer mu.Unlock()
		emitted = append(emitted, recordedEvent{name: event, data: []interface{}{data}})
	}
	emittedNamed := func(name string) []recordedEvent {
		mu.Lock()
		defer mu.Unlock()
		matched := []recordedEvent{}
		for _, event := range emitted {
			if event.name == name {
				matched = append(matched, event)
			}
		}
		return matched
	}
	taskCenterChanges := func() int {
		count := 0
		for _, event := range runtimeEvents() {
			if event.name == taskCenterChangedEvent {
				count++
			}
		}
		return count
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if ok() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("等待超时：%s", what)
	}

	app := NewApp()
	// 空闲探测换成假的，必须放在一切可能触发真实探测的操作之前：否则一次在途的真实探测会在
	// 后面的 SetProbe 之后才返回、把机器真实的空闲时长写进缓存——机器闲置超过阈值时空闲门直接
	// 放行，「进入等待清单」的断言就依赖了真实环境（APP-03）。
	fakeProbe := func(context.Context) (services.IdleSample, error) {
		return services.IdleSample{Idle: 10 * time.Second, OnACPower: true}, nil
	}
	app.idleGate.SetProbe(fakeProbe)
	ctx, cancelCtx := context.WithCancel(context.Background())
	app.ctx = ctx
	app.wireStartupHooks(emit)
	t.Cleanup(func() {
		// 合并中的 task-center-changed 定时器可能在用例结束之后才触发：先取消上下文让它们空转，
		// 再等已经在发的那一次落到桩上。这条清理登记在 stubRuntimeEvents 之后，因此先于
		// 「恢复真实的 EventsEmit」执行——真实的 EventsEmit 拿到非 Wails 上下文会直接退出进程。
		cancelCtx()
		services.StopPlaybackRelocation()
		services.SetVideoRelocatedNotifier(nil)
		services.SetPlaybackLaunchedHook(nil)
		time.Sleep(100 * time.Millisecond)
	})

	// PLAY-12：在线根下文件不见了，立即返回；后台找到同名同大小的文件后发事件。
	result, err := app.PlayVideo(video.ID)
	if err != nil || result == nil || result.DispatchSucceeded || result.Reason != "missing_file" {
		t.Fatalf("缺失文件应立即返回 missing_file: %+v err=%v", result, err)
	}
	waitFor("video-relocated 事件", func() bool { return len(emittedNamed("video-relocated")) > 0 })
	relocated, ok := emittedNamed("video-relocated")[0].data[0].(services.VideoRelocatedEvent)
	if !ok || relocated.VideoID != video.ID || relocated.NewPath != moved {
		t.Fatalf("video-relocated 载荷不对: %+v", emittedNamed("video-relocated")[0].data)
	}

	// APP-03：登记表变化。
	before := taskCenterChanges()
	app.backgroundTasks.Begin(services.BackgroundTaskBackup)
	app.backgroundTasks.End(services.BackgroundTaskBackup)
	waitFor("background-tasks 事件", func() bool { return len(emittedNamed("background-tasks")) > 0 })
	waitFor("登记表变化后的 task-center-changed", func() bool { return taskCenterChanges() > before })

	// 空闲门状态变化：用户活跃时自动任务进等待清单。登记表回调换成不通知的，只剩空闲门这一路。
	app.backgroundTasks.SetOnChange(func([]string) {})
	app.idleGate.SetProbe(fakeProbe)
	app.idleGate.SetProbeInterval(5 * time.Millisecond)
	before = taskCenterChanges()
	gateCtx, cancelGate := context.WithCancel(context.Background())
	gateDone := make(chan struct{})
	go func() {
		defer close(gateDone)
		_ = app.idleGate.Run(gateCtx, string(services.BackgroundTaskTechnical), func(context.Context) error { return nil })
	}()
	waitForIdleGateWaiting(t, app, string(services.BackgroundTaskTechnical))
	waitFor("idle-scheduler-state 事件", func() bool { return len(emittedNamed("idle-scheduler-state")) > 0 })
	waitFor("空闲门变化后的 task-center-changed", func() bool { return taskCenterChanges() > before })
	cancelGate()
	<-gateDone

	// 后台任务自己的状态事件：登记表回调已不通知，这一次只能来自状态事件本身。
	before = taskCenterChanges()
	if _, err := app.StartImageCleanupAnalysis(); err != nil {
		t.Fatalf("启动图片清理分析失败: %v", err)
	}
	waitFor("image-cleanup-progress 事件", func() bool { return len(emittedNamed("image-cleanup-progress")) > 0 })
	waitFor("状态事件后的 task-center-changed", func() bool { return taskCenterChanges() > before })
}

// LIB-09：迁移目录会连带改写扫描目录，成功后监听按新路径重配。夹具让新位置落在排除规则里：
// 重配过的话这一根报 excluded，没重配的话仍是迁移前的 watching。
func TestMoveDirectoryReconfiguresLibraryWatcherLIB09(t *testing.T) {
	setupAppTestDB(t)
	base := t.TempDir()
	source := filepath.Join(base, "src", "lib")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	destinationParent := filepath.Join(base, "dst")
	if err := os.MkdirAll(destinationParent, 0o755); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(destinationParent, "lib")
	settings := models.Settings{VideoExtensions: ".mp4", PlayWeight: 2, LibraryWatchEnabled: true, ScanExcludePaths: moved}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	dir := models.ScanDirectory{Path: source, Alias: "lib"}
	if err := database.DB.Create(&dir).Error; err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	defer app.libraryWatcher.Close()
	if err := app.configureLibraryWatcher(true); err != nil {
		t.Fatal(err)
	}
	if status := app.GetLibraryWatcherStatus(); len(status.Roots) != 1 || status.Roots[0].State != services.LibraryWatchStateWatching {
		t.Fatalf("迁移前应在监听: %+v", status)
	}

	if _, err := app.MoveDirectory(source, destinationParent); err != nil {
		t.Fatalf("迁移目录失败: %v", err)
	}
	var rewritten models.ScanDirectory
	if err := database.DB.First(&rewritten, dir.ID).Error; err != nil || rewritten.Path != moved {
		t.Fatalf("扫描目录应改写到新位置: %+v err=%v", rewritten, err)
	}
	status := app.GetLibraryWatcherStatus()
	if len(status.Roots) != 1 || status.Roots[0].ReasonCode != "excluded" || status.Roots[0].WatchCount != 0 {
		t.Fatalf("迁移后监听应按新路径重配（新位置已被排除）: %+v", status)
	}
}

// PLAY-01 / PLAY-14：手机端开关关着时，经生命周期锁的启动与直接调用 startShortFeedServer
// 都不监听；没在监听时状态里的访问范围文案与运行中的服务同一口径（按是否已设 PIN）。
func TestStartupShortFeedServerHonoursSwitchPLAY01PLAY14(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}).Error; err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	if err := app.withShortFeedLifecycle(func() error { return app.restartShortFeedServerLocked(context.Background()) }); err != nil {
		t.Fatalf("开关关着时启动应安静跳过: %v", err)
	}
	_ = app.withShortFeedLifecycle(func() error {
		app.startShortFeedServer(context.Background())
		return nil
	})
	if app.shortFeedServer != nil {
		t.Fatal("开关关着时不得创建或监听手机端服务")
	}
	status := app.GetShortFeedServerStatus()
	if status.Running || status.StartupError != "" || !strings.Contains(status.AllowedAccess, "no PIN set") {
		t.Fatalf("未设 PIN 的未监听状态不对: %+v", status)
	}
	if err := app.SetShortFeedPIN("246810"); err != nil {
		t.Fatalf("设置 PIN 失败: %v", err)
	}
	if status := app.GetShortFeedServerStatus(); status.Running || !strings.Contains(status.AllowedAccess, "PIN login required") {
		t.Fatalf("已设 PIN 的未监听状态应写需要登录: %+v", status)
	}
}

// MEDIA-10：启动前把上一次退出时还在排队或运行的字幕任务标成 interrupted，前端据此提示。
func TestMarkInterruptedSubtitleJobsBeforeFrontendMEDIA10(t *testing.T) {
	setupAppTestDB(t)
	video := models.Video{Name: "sub.mp4", Path: filepath.Join(t.TempDir(), "sub.mp4"), Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	jobs := []models.SubtitleJob{
		{VideoID: video.ID, Engine: "whisperx", Status: "running"},
		{VideoID: video.ID, Engine: "whisperx", Status: "queued"},
		{VideoID: video.ID, Engine: "whisperx", Status: "succeeded"},
	}
	for index := range jobs {
		if err := database.DB.Create(&jobs[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	app := NewApp()
	app.markInterruptedSubtitleJobs()
	want := []string{"interrupted", "interrupted", "succeeded"}
	for index, job := range jobs {
		var got models.SubtitleJob
		if err := database.DB.First(&got, job.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.Status != want[index] {
			t.Fatalf("任务 %d 状态应为 %s，实际 %s", job.ID, want[index], got.Status)
		}
	}
	summary, err := app.GetInterruptedSubtitleJobs()
	if err != nil || summary.Count != 2 {
		t.Fatalf("应提示 2 个中断任务: %+v err=%v", summary, err)
	}
}

// 退出流程的冒烟：新构造的 App 走完 shutdown 不会卡住（迁移取消、重定位停止、随机提交、
// 手机端生命周期锁都在其中），之后恢复与切换入口进入终态。
func TestShutdownCompletesAndEntersTerminalStatePLAY07(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.shutdown(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("shutdown 未在预期时间内结束")
	}
	app.restoreMu.Lock()
	terminal := app.restoreTerminal
	app.restoreMu.Unlock()
	if !terminal || app.shortFeedServer != nil {
		t.Fatalf("shutdown 之后应进入终态且手机端服务已停: terminal=%v server=%v", terminal, app.shortFeedServer != nil)
	}
}

// 接线契约：只在真实的 Wails 启动与退出里才跑得到的接线点（包级钩子、退出顺序、main 的注册），
// 在测试里按源码核对。行为本身分别由上面几条运行时用例与各服务自己的用例覆盖。
func TestStartupShutdownWiringContractMEDIA10APP09PLAY03(t *testing.T) {
	startup := p029FuncSource(t, "app.go", "startup")
	hooks := p029FuncSource(t, "app.go", "wireStartupHooks")
	shutdown := p029FuncSource(t, "app.go", "shutdown")

	for _, want := range []string{
		"a.wireStartupHooks(emit)",
		"a.rebuildMovieChartService()",
		"a.subtitleSearchService.SyncSubtitleIndexNow()",
		"a.withShortFeedLifecycle(func() error { return a.restartShortFeedServerLocked(ctx) })",
	} {
		if !strings.Contains(startup, want) {
			t.Errorf("startup 缺少接线 %q", want)
		}
	}
	for _, banned := range []string{"a.resetMovieChartService()", "a.startShortFeedServer(", "SyncFeedback"} {
		if strings.Contains(startup, banned) {
			t.Errorf("startup 不应再直接调用 %q", banned)
		}
	}
	// PLAY-03：IINA 会话登记。
	if !strings.Contains(hooks, "services.SetPlaybackLaunchedHook(a.iinaProgress.OnPlaybackLaunched)") {
		t.Error("wireStartupHooks 缺少 SetPlaybackLaunchedHook(iinaProgress.OnPlaybackLaunched)")
	}
	// 超分对账之后在同一个 goroutine 里清扫孤儿工作目录。
	if !p029GoFuncHasOrdered(t, "app.go", "startup", "a.enhancement.RecoverOnStartup(ctx)", "a.enhancement.SweepOrphanWorkdirs(ctx)") {
		t.Error("SweepOrphanWorkdirs 应在 RecoverOnStartup 之后、同一个后台 goroutine 里调用")
	}
	// APP-09：定时备份登记在 backupWG 上。
	if !p029GoFuncHasOrdered(t, "app.go", "startup", "defer a.backupWG.Done()", "a.backupService.StartPeriodic(backupCtx)") {
		t.Error("StartPeriodic 应在登记了 backupWG 的后台 goroutine 里运行")
	}

	cancelAt := strings.Index(shutdown, "a.cancelDatabaseSwitchForShutdown()")
	lockAt := strings.Index(shutdown, "a.restoreMu.Lock()")
	if cancelAt < 0 || lockAt < 0 || cancelAt > lockAt {
		t.Error("shutdown 必须在 restoreMu.Lock() 之前调用 cancelDatabaseSwitchForShutdown()")
	}
	for _, want := range []string{
		"services.StopPlaybackRelocation()",
		"services.FlushPendingRandomCommit()",
		"a.withShortFeedLifecycle(a.stopShortFeedForSetting)",
	} {
		if !strings.Contains(shutdown, want) {
			t.Errorf("shutdown 缺少 %q", want)
		}
	}
	if strings.Contains(shutdown, "a.shortFeedServer.Stop(") {
		t.Error("shutdown 不得绕过生命周期锁直接停手机端服务")
	}

	appSource, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(appSource), "SyncFeedback()") {
		t.Error("app.go 里已成空操作的 SyncFeedback 调用应删除")
	}

	// MEDIA-10 / D-PC21：main 在 wails.Run 之前标中断字幕任务，并注册 OnBeforeClose。
	mainSource := p029FuncSource(t, "main.go", "main")
	initAt := strings.Index(mainSource, "database.Init()")
	markAt := strings.Index(mainSource, "app.markInterruptedSubtitleJobs()")
	runAt := strings.Index(mainSource, "wails.Run(")
	if initAt < 0 || markAt < initAt || runAt < markAt {
		t.Error("main 必须在 database.Init() 之后、wails.Run 之前调用 markInterruptedSubtitleJobs")
	}
	for _, file := range []string{"main.go", "main_bindings.go"} {
		source := strings.Join(strings.Fields(p029FuncSource(t, file, "main")), "")
		if !strings.Contains(source, "OnBeforeClose:app.beforeClose") {
			t.Errorf("%s 缺少 OnBeforeClose: app.beforeClose", file)
		}
	}
}

// p029FuncSource 返回文件里名为 name 的函数（或方法）的源码。
func p029FuncSource(t *testing.T, path, name string) string {
	t.Helper()
	fset, file, source := p029ParseFile(t, path)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return string(source[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset])
		}
	}
	t.Fatalf("%s 里找不到函数 %s", path, name)
	return ""
}

// p029GoFuncHasOrdered 报告函数 name 里是否有一条 go 语句，它的函数字面量先后包含 first 与 second。
func p029GoFuncHasOrdered(t *testing.T, path, name, first, second string) bool {
	t.Helper()
	fset, file, source := p029ParseFile(t, path)
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			stmt, ok := node.(*ast.GoStmt)
			if !ok {
				return true
			}
			lit, ok := stmt.Call.Fun.(*ast.FuncLit)
			if !ok {
				return true
			}
			body := string(source[fset.Position(lit.Pos()).Offset:fset.Position(lit.End()).Offset])
			firstAt, secondAt := strings.Index(body, first), strings.Index(body, second)
			if firstAt >= 0 && secondAt > firstAt {
				found = true
			}
			return true
		})
	}
	return found
}

func p029ParseFile(t *testing.T, path string) (*token.FileSet, *ast.File, []byte) {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	return fset, file, source
}

// APP-03：语义索引、图片 AI 打标、图片语义索引的状态事件同样要触发任务中心刷新（与其余后台任务的事件一致）。
func TestTaskCenterRefreshWiredToIndexAndImageTaggingEventsAPP03(t *testing.T) {
	for _, tc := range []struct{ file, event string }{
		{"app_ai.go", "semantic-index-state"},
		{"app_image.go", "image-ai-tagging-progress"},
		{"app_image.go", "image-semantic-index-state"},
	} {
		source, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		emit := `runtime.EventsEmit(a.ctx, "` + tc.event + `", status)`
		index := strings.Index(string(source), emit)
		if index < 0 {
			t.Fatalf("%s 中找不到 %s 事件的发送", tc.file, tc.event)
		}
		tail := string(source[index+len(emit):])
		if end := strings.Index(tail, "})"); end < 0 || !strings.Contains(tail[:end], "a.notifyTaskCenterChanged()") {
			t.Errorf("%s 的 %s 发送之后应调用 notifyTaskCenterChanged", tc.file, tc.event)
		}
	}
}

// LIB-08：扫描摘要区分启动扫描与手动扫描（D-PC09）。前端启动时传 startup、扫描条传 manual；
// 为空或不认识的值（旧调用点）按 manual 处理。
func TestSyncScanDirectoriesEmitsSummaryWithTriggerLIB08(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}).Error; err != nil {
		t.Fatal(err)
	}
	events := stubRuntimeEvents(t)
	app := NewApp()
	app.ctx = context.Background()
	for _, tc := range []struct{ in, want string }{
		{services.ScanTriggerStartup, services.ScanTriggerStartup},
		{services.ScanTriggerManual, services.ScanTriggerManual},
		{"", services.ScanTriggerManual},
		{"whatever", services.ScanTriggerManual},
	} {
		before := len(events())
		if _, err := app.SyncScanDirectories(tc.in); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		var got []string
		for _, event := range events()[before:] {
			if event.name != "library-scan-summary" {
				continue
			}
			summary, ok := event.data[0].(services.LibraryScanSummaryEvent)
			if !ok {
				t.Fatalf("摘要载荷类型不对: %T", event.data[0])
			}
			got = append(got, summary.Trigger)
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("trigger=%q 应发一次 %q 摘要，实际 %v", tc.in, tc.want, got)
		}
	}
}
