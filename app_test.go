package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	result, err := app.SyncScanDirectories()
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
	var after int64
	database.DB.Model(&models.Image{}).Count(&after)
	if after != 0 {
		t.Fatalf("删除图片目录后该目录下的图片应当从图库消失，实际还有 %d 张", after)
	}
}
