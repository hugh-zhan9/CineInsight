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

	if err := db.AutoMigrate(&models.Video{}, &models.SubtitleSegment{}, &models.SubtitleIndexState{}, &models.Tag{}, &models.Settings{}, &models.ScanDirectory{}); err != nil {
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

func mustSetFileModTime(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("设置文件时间失败: %v", err)
	}
}

func previewStatsSnapshot(t *testing.T, videoID uint) models.Video {
	t.Helper()
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		t.Fatalf("读取视频统计失败: %v", err)
	}
	return video
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
	mustSetFileModTime(t, visible, time.Now().Add(-10*time.Minute))

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

func TestScanDirectorySkipsTrashTempSuffixAndRecentlyActiveFiles(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	stableVideo := filepath.Join(root, "stable.mp4")
	trashVideo := filepath.Join(root, "trash", "trashed.mp4")
	tempSuffixVideo := filepath.Join(root, "downloading.temp.mp4")
	recentVideo := filepath.Join(root, "recent.mp4")

	mustCreateFile(t, stableVideo)
	mustCreateFile(t, trashVideo)
	mustCreateFile(t, tempSuffixVideo)
	mustCreateFile(t, recentVideo)

	oldTime := time.Now().Add(-10 * time.Minute)
	mustSetFileModTime(t, stableVideo, oldTime)
	mustSetFileModTime(t, trashVideo, oldTime)
	mustSetFileModTime(t, tempSuffixVideo, oldTime)

	files, err := svc.ScanDirectory(root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("期望仅扫描到1个稳定视频，实际: %d, files=%v", len(files), files)
	}
	if files[0] != stableVideo {
		t.Fatalf("扫描结果不正确: got=%s want=%s", files[0], stableVideo)
	}
}

func TestDeleteVideoMovesFileToTrashWhenDeleteFileEnabled(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	videoPath := filepath.Join(root, "library", "movie.mp4")
	mustCreateFile(t, videoPath)

	video := models.Video{
		Name:      "movie.mp4",
		Path:      videoPath,
		Directory: filepath.Dir(videoPath),
		Size:      1,
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatalf("删除视频失败: %v", err)
	}

	if _, err := os.Stat(videoPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("期望原文件已移走, err=%v", err)
	}

	trashPath := filepath.Join(filepath.Dir(videoPath), DefaultTrashDirName, filepath.Base(videoPath))
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("期望文件已移动到回收站: %v", err)
	}

	var deleted models.Video
	if err := database.DB.Unscoped().First(&deleted, video.ID).Error; err != nil {
		t.Fatalf("期望数据库仍可查到软删除记录: %v", err)
	}
	if !deleted.DeletedAt.IsValid() {
		t.Fatalf("期望视频记录已被软删除")
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

	videos, err := svc.SearchVideosWithFilters("cat", []uint{tag.ID}, 0, 0, 0, 100, 0, 0, 0, 100)
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

func TestBatchAddTagToVideosReportsPartialFailures(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}

	tag := models.Tag{Name: "batch", Color: "#fff"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	videoA := models.Video{Name: "a.mp4", Path: "/tmp/batch-a.mp4", Directory: "/tmp", Size: 1}
	videoB := models.Video{Name: "b.mp4", Path: "/tmp/batch-b.mp4", Directory: "/tmp", Size: 1}
	if err := database.DB.Create(&videoA).Error; err != nil {
		t.Fatalf("创建视频A失败: %v", err)
	}
	if err := database.DB.Create(&videoB).Error; err != nil {
		t.Fatalf("创建视频B失败: %v", err)
	}

	result := svc.BatchAddTagToVideos([]uint{videoA.ID, 999999, videoB.ID}, tag.ID)
	if result.Requested != 3 || result.Succeeded != 2 || result.Failed != 1 {
		t.Fatalf("批量结果错误: %#v", result)
	}

	var loaded models.Video
	if err := database.DB.Preload("Tags").First(&loaded, videoA.ID).Error; err != nil {
		t.Fatalf("读取视频标签失败: %v", err)
	}
	if len(loaded.Tags) != 1 || loaded.Tags[0].ID != tag.ID {
		t.Fatalf("期望视频A已打标签，实际 %#v", loaded.Tags)
	}
}

func TestGetVideosPaginatedPrioritizesLowerScoreBeforeLargerSize(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}

	videos := []models.Video{
		{Name: "zero-small.mp4", Path: "/tmp/zero-small.mp4", Directory: "/tmp", Size: 10, PlayCount: 0, RandomPlayCount: 0},
		{Name: "two-large.mp4", Path: "/tmp/two-large.mp4", Directory: "/tmp", Size: 1000, PlayCount: 1, RandomPlayCount: 0},
		{Name: "zero-large.mp4", Path: "/tmp/zero-large.mp4", Directory: "/tmp", Size: 100, PlayCount: 0, RandomPlayCount: 0},
	}
	for _, video := range videos {
		video := video
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("创建测试视频失败: %v", err)
		}
	}

	result, err := svc.GetVideosPaginated(0, 0, 0, 10)
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("期望返回3条结果，实际 %d", len(result))
	}
	if result[0].Name != "zero-large.mp4" || result[1].Name != "zero-small.mp4" || result[2].Name != "two-large.mp4" {
		t.Fatalf("排序不符合 score ASC, size DESC 预期: %#v", []string{result[0].Name, result[1].Name, result[2].Name})
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

	result, err := svc.PlayRandomVideo()
	if err != nil {
		t.Fatalf("随机播放不应返回系统错误: %v", err)
	}
	if result == nil || result.DispatchSucceeded {
		t.Fatalf("期望 dispatch 失败结果")
	}
	msg := result.UserMessage
	if !strings.Contains(msg, "broken.mp4") || !strings.Contains(msg, videoPath) {
		t.Fatalf("错误信息未包含视频信息: %s", msg)
	}
	if result.ReconcileResult != nil {
		t.Fatalf("dispatch_failed 不应返回 reconcile result")
	}
}

func TestGetPreviewSessionInlineMode(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	mustCreateFile(t, videoPath)

	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	session, err := svc.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("获取预览 session 失败: %v", err)
	}
	if session.Mode != "inline" {
		t.Fatalf("期望 inline 模式，实际 %s", session.Mode)
	}
	if session.InlineSource == nil {
		t.Fatalf("期望返回 inline source")
	}
	if session.InlineSource.LocatorStrategy != "asset_route" {
		t.Fatalf("locator strategy 错误: %s", session.InlineSource.LocatorStrategy)
	}
	if session.InlineSource.LocatorValue != previewMediaPath(video.ID) {
		t.Fatalf("locator value 错误: got=%s want=%s", session.InlineSource.LocatorValue, previewMediaPath(video.ID))
	}
	if session.InlineSource.MIME != "video/mp4" {
		t.Fatalf("mime 错误: %s", session.InlineSource.MIME)
	}
	if session.ExternalAction != nil {
		t.Fatalf("inline 模式不应返回 external action")
	}
	if session.ReasonCode != "" || session.ReasonMessage != "" {
		t.Fatalf("inline 模式不应返回 reason: %+v", session)
	}
}

func TestGetPreviewSessionExternalPreviewMode(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mkv")
	mustCreateFile(t, videoPath)

	video := models.Video{Name: "clip.mkv", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	session, err := svc.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("获取预览 session 失败: %v", err)
	}
	if session.Mode != "external-preview" {
		t.Fatalf("期望 external-preview 模式，实际 %s", session.Mode)
	}
	if session.InlineSource != nil {
		t.Fatalf("external-preview 模式不应返回 inline source")
	}
	if session.ExternalAction == nil {
		t.Fatalf("期望返回 external action")
	}
	if session.ExternalAction.ActionID != "preview_externally" {
		t.Fatalf("action id 错误: %s", session.ExternalAction.ActionID)
	}
	if !strings.Contains(session.ExternalAction.Hint, "不计正式播放统计") {
		t.Fatalf("hint 未说明统计隔离: %s", session.ExternalAction.Hint)
	}
	if session.ReasonCode == "" || session.ReasonMessage == "" {
		t.Fatalf("external-preview 模式应返回 reason")
	}
}

func TestGetPreviewSessionUnsupportedWhenFileMissing(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "missing.mp4")

	video := models.Video{Name: "missing.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	session, err := svc.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("获取预览 session 失败: %v", err)
	}
	if session.Mode != "unsupported" {
		t.Fatalf("期望 unsupported 模式，实际 %s", session.Mode)
	}
	if session.InlineSource != nil || session.ExternalAction != nil {
		t.Fatalf("unsupported 模式不应返回 source/action")
	}
	if session.ReasonCode != "file_missing" {
		t.Fatalf("reason code 错误: %s", session.ReasonCode)
	}
}

func TestPreviewExternallyDoesNotMutateFormalPlayStats(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "preview.mp4")
	mustCreateFile(t, videoPath)

	video := models.Video{Name: "preview.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	openedPath := ""
	oldOpen := openWithDefaultFn
	openWithDefaultFn = func(path string, isDir bool) error {
		openedPath = path
		return nil
	}
	defer func() { openWithDefaultFn = oldOpen }()

	before := previewStatsSnapshot(t, video.ID)

	if err := svc.PreviewExternally(video.ID); err != nil {
		t.Fatalf("外部预览失败: %v", err)
	}
	if openedPath != videoPath {
		t.Fatalf("打开路径错误: got=%s want=%s", openedPath, videoPath)
	}

	after := previewStatsSnapshot(t, video.ID)
	if after.PlayCount != before.PlayCount || after.RandomPlayCount != before.RandomPlayCount {
		t.Fatalf("预览不应修改播放计数: before=%+v after=%+v", before, after)
	}
	if after.LastPlayedAt != nil {
		t.Fatalf("预览不应更新 last_played_at")
	}
}

func TestPlayVideoUpdatesFormalPlayStats(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "formal.mp4")
	mustCreateFile(t, videoPath)

	video := models.Video{Name: "formal.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	oldOpen := openWithDefaultFn
	openWithDefaultFn = func(path string, isDir bool) error { return nil }
	defer func() { openWithDefaultFn = oldOpen }()

	result, err := svc.PlayVideo(video.ID)
	if err != nil {
		t.Fatalf("正式播放失败: %v", err)
	}
	if result == nil || !result.DispatchSucceeded {
		t.Fatalf("期望 dispatch success result")
	}

	after := previewStatsSnapshot(t, video.ID)
	if after.PlayCount != 1 {
		t.Fatalf("正式播放应增加 play_count，实际 %d", after.PlayCount)
	}
	if after.LastPlayedAt == nil {
		t.Fatalf("正式播放应更新 last_played_at")
	}
	if after.IsStale {
		t.Fatalf("正式播放成功后不应保持 stale")
	}
}

func TestPlayVideoMissingFileReturnsReconcileResultAndMarksStale(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "missing.mp4")

	video := models.Video{Name: "missing.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	result, err := svc.PlayVideo(video.ID)
	if err != nil {
		t.Fatalf("期望领域失败走返回值而非 error: %v", err)
	}
	if result == nil || result.DispatchSucceeded {
		t.Fatalf("期望 dispatch 失败")
	}
	if result.ReconcileResult == nil {
		t.Fatalf("期望返回 reconcile result")
	}
	if !result.ReconcileResult.DidMarkStale {
		t.Fatalf("期望标记 stale")
	}
	if !strings.Contains(result.UserMessage, "missing.mp4") || !strings.Contains(result.UserMessage, videoPath) {
		t.Fatalf("错误信息未包含文件级上下文: %s", result.UserMessage)
	}

	after := previewStatsSnapshot(t, video.ID)
	if after.PlayCount != 0 || after.LastPlayedAt != nil {
		t.Fatalf("失败播放不应污染正式统计: %+v", after)
	}
	if !after.IsStale {
		t.Fatalf("失败后记录应标记为 stale")
	}
}

func TestPlayRandomVideoSuccessWritesStatsOnlyOnDispatchSuccess(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "random.mp4")
	mustCreateFile(t, videoPath)

	video := models.Video{Name: "random.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	oldOpen := openWithDefaultFn
	openWithDefaultFn = func(path string, isDir bool) error { return nil }
	defer func() { openWithDefaultFn = oldOpen }()

	result, err := svc.PlayRandomVideo()
	if err != nil {
		t.Fatalf("随机播放失败: %v", err)
	}
	if result == nil || !result.DispatchSucceeded || result.Video == nil {
		t.Fatalf("期望返回 dispatch success result")
	}

	after := previewStatsSnapshot(t, video.ID)
	if after.RandomPlayCount != 1 {
		t.Fatalf("随机播放成功后应增加 random_play_count，实际 %d", after.RandomPlayCount)
	}
	if after.LastPlayedAt == nil {
		t.Fatalf("随机播放成功后应更新 last_played_at")
	}
}

func TestPlayRandomVideoNoVideosReturnsDomainFailureResult(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}

	result, err := svc.PlayRandomVideo()
	if err != nil {
		t.Fatalf("无视频时不应返回系统错误: %v", err)
	}
	if result == nil {
		t.Fatalf("期望返回结构化结果")
	}
	if result.DispatchSucceeded {
		t.Fatalf("无视频时不应视为 dispatch success")
	}
	if result.ReasonCode != "no_videos" {
		t.Fatalf("reason code 错误: %s", result.ReasonCode)
	}
	if !strings.Contains(result.UserMessage, "没有可播放的视频") {
		t.Fatalf("user message 不明确: %s", result.UserMessage)
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

func TestParseFFProbeOutputFallsBackToFormatDuration(t *testing.T) {
	output := []byte(`{
		"streams": [{"width": 1920, "height": 1080}],
		"format": {"duration": "12.34"}
	}`)

	duration, resolution, width, height, err := parseFFProbeOutput(output)
	if err != nil {
		t.Fatalf("解析 ffprobe 输出失败: %v", err)
	}
	if duration != 12.34 {
		t.Fatalf("duration 错误: got=%v want=12.34", duration)
	}
	if resolution != "1920x1080" || width != 1920 || height != 1080 {
		t.Fatalf("分辨率解析错误: resolution=%s width=%d height=%d", resolution, width, height)
	}
}

func TestParseFFProbeOutputRejectsNonJSON(t *testing.T) {
	if _, _, _, _, err := parseFFProbeOutput([]byte("ratecontrol warning")); err == nil {
		t.Fatalf("期望非 JSON 输出返回错误")
	}
}
