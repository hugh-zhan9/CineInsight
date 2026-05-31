package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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

	if err := db.AutoMigrate(models.AllModels()...); err != nil {
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

func TestRenameVideoMovesSubtitleAndRefreshesIndex(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	oldVideoPath := filepath.Join(root, "old-name.mp4")
	oldSRTPath := filepath.Join(root, "old-name.srt")
	newSRTPath := filepath.Join(root, "new-name.srt")

	mustCreateFile(t, oldVideoPath)
	if err := os.WriteFile(oldSRTPath, []byte("1\n00:00:01,000 --> 00:00:03,000\nhello renamed subtitle\n\n"), 0644); err != nil {
		t.Fatalf("写入字幕文件失败: %v", err)
	}

	video := models.Video{Name: "old-name.mp4", Path: oldVideoPath, Directory: root, Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := indexSubtitleFileForVideoID(video.ID, oldSRTPath); err != nil {
		t.Fatalf("索引字幕失败: %v", err)
	}

	if err := svc.RenameVideo(video.ID, "new-name"); err != nil {
		t.Fatalf("重命名视频失败: %v", err)
	}

	if _, err := os.Stat(oldSRTPath); !os.IsNotExist(err) {
		t.Fatalf("旧字幕文件应被移走，stat err=%v", err)
	}
	if _, err := os.Stat(newSRTPath); err != nil {
		t.Fatalf("新字幕文件不存在: %v", err)
	}

	var state models.SubtitleIndexState
	if err := database.DB.Where("video_id = ?", video.ID).First(&state).Error; err != nil {
		t.Fatalf("读取字幕索引状态失败: %v", err)
	}
	if filepath.Clean(state.SubtitlePath) != filepath.Clean(newSRTPath) {
		t.Fatalf("字幕索引路径未更新 got=%q want=%q", state.SubtitlePath, newSRTPath)
	}

	matches, err := (&SubtitleSearchService{}).SearchSubtitleMatches("renamed subtitle", 10)
	if err != nil {
		t.Fatalf("搜索重命名后的字幕失败: %v", err)
	}
	if len(matches) != 1 || matches[0].Video.ID != video.ID {
		t.Fatalf("期望命中重命名后的视频，实际 %#v", matches)
	}
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

func TestScanDirectorySkipsTypeScriptSourceWhenTsExtensionEnabled(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("video_extensions", ".ts,.mp4").Error; err != nil {
		t.Fatalf("更新扩展名设置失败: %v", err)
	}
	svc := &VideoService{}
	root := t.TempDir()
	oldTime := time.Now().Add(-10 * time.Minute)
	sourcePath := filepath.Join(root, "node_modules", "pkg", "types.ts")
	declarationPath := filepath.Join(root, "node_modules", "pkg", "index.d.ts")
	mediaPath := filepath.Join(root, "capture.ts")
	mustCreateFile(t, sourcePath)
	mustCreateFile(t, declarationPath)
	mustCreateFile(t, mediaPath)
	mustSetFileModTime(t, sourcePath, oldTime)
	mustSetFileModTime(t, declarationPath, oldTime)
	mustSetFileModTime(t, mediaPath, oldTime)

	files, err := svc.ScanDirectory(root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(files) != 1 || files[0] != mediaPath {
		t.Fatalf("应跳过 TypeScript 源码，只保留视频 TS，实际 files=%v", files)
	}
}

func TestScanDirectoryWithInfoReturnsErrorForMissingRoot(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}

	_, err := svc.ScanDirectoryWithInfo(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatalf("缺失的扫描根目录不应被当作空目录处理")
	}
}

func TestSyncScanDirectoriesAddsAndRelocatesPreservingTags(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	oldTime := time.Now().Add(-10 * time.Minute)

	newVideoPath := filepath.Join(root, "incoming", "new.mp4")
	movedOldPath := filepath.Join(root, "old", "movie.mp4")
	movedNewPath := filepath.Join(root, "new", "movie.mp4")
	mustCreateFile(t, newVideoPath)
	mustCreateFile(t, movedNewPath)
	mustSetFileModTime(t, newVideoPath, oldTime)
	mustSetFileModTime(t, movedNewPath, oldTime)

	tag := models.Tag{Name: "keep", Color: "#fff"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	movedVideo := models.Video{
		Name:      "movie.mp4",
		Path:      movedOldPath,
		Directory: filepath.Dir(movedOldPath),
		Size:      1,
	}
	if err := database.DB.Create(&movedVideo).Error; err != nil {
		t.Fatalf("创建待迁移视频失败: %v", err)
	}
	if err := database.DB.Model(&movedVideo).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("绑定标签失败: %v", err)
	}

	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root, Alias: "root"}})
	if result.Relocated != 1 || result.Added != 1 || result.Deleted != 0 {
		t.Fatalf("同步结果错误: %#v", result)
	}

	var loadedMoved models.Video
	if err := database.DB.Preload("Tags").First(&loadedMoved, movedVideo.ID).Error; err != nil {
		t.Fatalf("读取迁移后视频失败: %v", err)
	}
	if loadedMoved.Path != movedNewPath || loadedMoved.Directory != filepath.Dir(movedNewPath) {
		t.Fatalf("迁移路径错误: got path=%s dir=%s", loadedMoved.Path, loadedMoved.Directory)
	}
	if len(loadedMoved.Tags) != 1 || loadedMoved.Tags[0].ID != tag.ID {
		t.Fatalf("迁移后应保留标签，实际 %#v", loadedMoved.Tags)
	}

	var added models.Video
	if err := database.DB.Where("path = ?", newVideoPath).First(&added).Error; err != nil {
		t.Fatalf("新文件未入库: %v", err)
	}
}

func TestSyncScanDirectoriesDoesNotReimportSoftDeletedPath(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	oldTime := time.Now().Add(-10 * time.Minute)
	videoPath := filepath.Join(root, "4006929f-356a-4e1f-bcc9-024590e9127c.mp4")

	mustCreateFile(t, videoPath)
	mustSetFileModTime(t, videoPath, oldTime)

	video := models.Video{
		Name:      filepath.Base(videoPath),
		Path:      videoPath,
		Directory: root,
		Size:      1,
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := svc.DeleteVideo(video.ID, false); err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}

	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root, Alias: "root"}})
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("软删除同路径文件不应重新导入，实际结果: %#v", result)
	}

	var activeCount int64
	if err := database.DB.Model(&models.Video{}).Where("path = ?", videoPath).Count(&activeCount).Error; err != nil {
		t.Fatalf("统计 active 视频失败: %v", err)
	}
	if activeCount != 0 {
		t.Fatalf("软删除同路径文件不应重新出现 active 记录，实际 %d", activeCount)
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

func TestBatchDeleteVideosReportsPartialFailures(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	videoAPath := filepath.Join(root, "a.mp4")
	videoBPath := filepath.Join(root, "b.mp4")
	mustCreateFile(t, videoAPath)
	mustCreateFile(t, videoBPath)

	videoA := models.Video{Name: "a.mp4", Path: videoAPath, Directory: root, Size: 1}
	videoB := models.Video{Name: "b.mp4", Path: videoBPath, Directory: root, Size: 1}
	if err := database.DB.Create(&videoA).Error; err != nil {
		t.Fatalf("创建视频A失败: %v", err)
	}
	if err := database.DB.Create(&videoB).Error; err != nil {
		t.Fatalf("创建视频B失败: %v", err)
	}

	result := svc.BatchDeleteVideos([]uint{videoA.ID, 999999, videoB.ID}, false)
	if result.Requested != 3 || result.Succeeded != 2 || result.Failed != 1 {
		t.Fatalf("批量删除结果错误: %#v", result)
	}
	if len(result.Errors) != 1 || result.Errors[0].VideoID != 999999 {
		t.Fatalf("期望记录失败视频ID，实际 %#v", result.Errors)
	}

	var remaining int64
	if err := database.DB.Model(&models.Video{}).Where("id IN ?", []uint{videoA.ID, videoB.ID}).Count(&remaining).Error; err != nil {
		t.Fatalf("统计剩余视频失败: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("期望成功项已被软删除，剩余 %d", remaining)
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

func TestSearchVideosWithFiltersKeepsLiteralFileSearchSemantics(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}

	named4K := models.Video{Name: "4k-old-clip.mp4", Path: "/media/4k-old-clip.mp4", Directory: "/media", Size: 100, Width: 640, Height: 360}
	actual4K := models.Video{Name: "plain.mp4", Path: "/media/plain.mp4", Directory: "/media", Size: 200, Width: 3840, Height: 2160}
	if err := database.DB.Create(&named4K).Error; err != nil {
		t.Fatalf("创建文件名包含4k的视频失败: %v", err)
	}
	if err := database.DB.Create(&actual4K).Error; err != nil {
		t.Fatalf("创建真实4k视频失败: %v", err)
	}

	videos, err := svc.SearchVideosWithFilters("4k", nil, 0, 0, 0, 0, 0, 0, 0, 10)
	if err != nil {
		t.Fatalf("文件搜索失败: %v", err)
	}
	if len(videos) != 1 || videos[0].ID != named4K.ID {
		t.Fatalf("普通文件搜索应按文件名/路径字面匹配 got=%v want=%d", videoIDs(videos), named4K.ID)
	}
}

func TestSearchVideosSmartUnderstandsNaturalLanguageHints(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}

	personTag := models.Tag{Name: "人物", Color: "#fff"}
	stageTag := models.Tag{Name: "舞台", Color: "#000"}
	if err := database.DB.Create(&personTag).Error; err != nil {
		t.Fatalf("创建人物标签失败: %v", err)
	}
	if err := database.DB.Create(&stageTag).Error; err != nil {
		t.Fatalf("创建舞台标签失败: %v", err)
	}

	personVideo := models.Video{
		Name:      "family-portrait.mp4",
		Path:      "/media/family-portrait.mp4",
		Directory: "/media",
		Size:      100,
		Width:     720,
		Height:    1280,
	}
	stageVideo := models.Video{
		Name:      "concert-stage.mp4",
		Path:      "/media/concert-stage.mp4",
		Directory: "/media",
		Size:      200,
		Width:     1920,
		Height:    1080,
	}
	silentVideo := models.Video{
		Name:      "plain.mp4",
		Path:      "/media/plain.mp4",
		Directory: "/media",
		Size:      300,
		Width:     3840,
		Height:    2160,
	}
	for _, video := range []*models.Video{&personVideo, &stageVideo, &silentVideo} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
	}
	if err := database.DB.Model(&personVideo).Association("Tags").Append(&personTag); err != nil {
		t.Fatalf("绑定人物标签失败: %v", err)
	}
	if err := database.DB.Model(&stageVideo).Association("Tags").Append(&stageTag); err != nil {
		t.Fatalf("绑定舞台标签失败: %v", err)
	}
	if err := database.DB.Create(&models.VideoFace{
		VideoID:   personVideo.ID,
		Signature: "face-person",
		Status:    models.VideoFaceStatusDetected,
		Source:    "test",
	}).Error; err != nil {
		t.Fatalf("创建人脸记录失败: %v", err)
	}
	if err := database.DB.Create(&models.VideoFace{
		VideoID:   stageVideo.ID,
		Signature: "face-stage",
		Status:    models.VideoFaceStatusDetected,
		Source:    "test",
	}).Error; err != nil {
		t.Fatalf("创建人脸记录失败: %v", err)
	}
	if err := database.DB.Create(&models.SubtitleSegment{
		VideoID:      stageVideo.ID,
		SegmentIndex: 1,
		StartTimeMs:  1000,
		EndTimeMs:    3000,
		Text:         "the crowd says secret phrase",
		SubtitlePath: "/media/concert-stage.srt",
	}).Error; err != nil {
		t.Fatalf("创建字幕索引失败: %v", err)
	}

	cases := []struct {
		name    string
		keyword string
		wantIDs []uint
	}{
		{name: "detect face query", keyword: "找有人脸的视频", wantIDs: []uint{personVideo.ID, stageVideo.ID}},
		{name: "detect tag query", keyword: "查找带舞台标签的视频", wantIDs: []uint{stageVideo.ID}},
		{name: "detect resolution query", keyword: "找4k视频", wantIDs: []uint{silentVideo.ID}},
		{name: "detect vertical query", keyword: "竖屏人物视频", wantIDs: []uint{personVideo.ID}},
		{name: "detect subtitle query", keyword: "字幕里提到 secret phrase", wantIDs: []uint{stageVideo.ID}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			videos, err := svc.SearchVideosSmart(tc.keyword, nil, 0, 0, 0, 0, 0, 0, 0, 10)
			if err != nil {
				t.Fatalf("自然语言搜索失败: %v", err)
			}
			gotIDs := videoIDs(videos)
			sort.Slice(gotIDs, func(i, j int) bool { return gotIDs[i] < gotIDs[j] })
			sort.Slice(tc.wantIDs, func(i, j int) bool { return tc.wantIDs[i] < tc.wantIDs[j] })
			if len(gotIDs) != len(tc.wantIDs) {
				t.Fatalf("结果数量错误 got=%v want=%v", gotIDs, tc.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tc.wantIDs[i] {
					t.Fatalf("结果错误 got=%v want=%v", gotIDs, tc.wantIDs)
				}
			}
		})
	}
}

func TestSearchVideosSmartUsesLocalMLEmbeddingSearch(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(map[string]interface{}{
		"ai_backend_mode": string(AIBackendModeLocal),
		"local_ml_model":  defaultLocalMLModel,
	}).Error; err != nil {
		t.Fatalf("更新本地 ML 设置失败: %v", err)
	}

	stageVideo := models.Video{Name: "clip-a.mp4", Path: "/media/clip-a.mp4", Directory: "/media", Size: 10}
	kitchenVideo := models.Video{Name: "clip-b.mp4", Path: "/media/clip-b.mp4", Directory: "/media", Size: 20}
	if err := database.DB.Create(&stageVideo).Error; err != nil {
		t.Fatalf("创建舞台视频失败: %v", err)
	}
	if err := database.DB.Create(&kitchenVideo).Error; err != nil {
		t.Fatalf("创建厨房视频失败: %v", err)
	}
	createVideoEmbedding(t, stageVideo.ID, defaultLocalMLModel, []float32{1, 0})
	createVideoEmbedding(t, kitchenVideo.ID, defaultLocalMLModel, []float32{0, 1})

	runtime := &fakeLocalMLRuntime{
		running:   true,
		model:     defaultLocalMLModel,
		embedding: []float32{1, 0},
	}
	svc := &VideoService{
		embeddingService: &VideoEmbeddingService{
			configProvider: SettingsAITaggingConfigProvider{},
			localMLRuntime: runtime,
		},
	}

	videos, err := svc.SearchVideosSmart("舞台上的灯光和观众", nil, 0, 0, 0, 0, 0, 0, 0, 10)
	if err != nil {
		t.Fatalf("本地 ML 智能搜索失败: %v", err)
	}
	if len(videos) != 1 || videos[0].ID != stageVideo.ID {
		t.Fatalf("本地 ML 搜索应按语义向量召回舞台视频 got=%v want=%d", videoIDs(videos), stageVideo.ID)
	}
	if videos[0].SearchScore <= 0.9 {
		t.Fatalf("本地 ML 搜索应返回相似度分数，实际 %.3f", videos[0].SearchScore)
	}
	if runtime.embedCalls != 1 {
		t.Fatalf("本地 ML 搜索应只调用一次文本 embedding，实际 %d", runtime.embedCalls)
	}
}

func TestSearchVideosSmartDoesNotUseLocalMLInAPIMode(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(map[string]interface{}{
		"ai_backend_mode": string(AIBackendModeAPI),
		"local_ml_model":  defaultLocalMLModel,
	}).Error; err != nil {
		t.Fatalf("更新 API 模式设置失败: %v", err)
	}

	video := models.Video{Name: "clip-a.mp4", Path: "/media/clip-a.mp4", Directory: "/media", Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	createVideoEmbedding(t, video.ID, defaultLocalMLModel, []float32{1, 0})

	runtime := &fakeLocalMLRuntime{
		running:   true,
		model:     defaultLocalMLModel,
		embedding: []float32{1, 0},
	}
	svc := &VideoService{
		embeddingService: &VideoEmbeddingService{
			configProvider: SettingsAITaggingConfigProvider{},
			localMLRuntime: runtime,
		},
	}

	videos, err := svc.SearchVideosSmart("舞台上的灯光和观众", nil, 0, 0, 0, 0, 0, 0, 0, 10)
	if err != nil {
		t.Fatalf("API 模式智能搜索失败: %v", err)
	}
	if len(videos) != 0 {
		t.Fatalf("API 模式不应使用本地向量召回结果 got=%v", videoIDs(videos))
	}
	if runtime.embedCalls != 0 {
		t.Fatalf("API 模式不应调用本地 ML runtime，实际 %d", runtime.embedCalls)
	}
}

func TestSearchVideosSmartUsesAPIEmbeddingsInAPIMode(t *testing.T) {
	setupVideoServiceTestDB(t)
	var embeddingCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("API 语义搜索应调用 embeddings endpoint，实际 %s", r.URL.Path)
		}
		embeddingCalls++
		var body struct {
			Model string `json:"model"`
			Input any    `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("解析 embeddings 请求失败: %v", err)
		}
		if body.Model != "text-embedding-3-small" {
			t.Fatalf("embedding model 不正确: %q", body.Model)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 0, "embedding": []float64{1, 0}},
			},
			"model": body.Model,
		})
	}))
	defer server.Close()

	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(map[string]interface{}{
		"ai_backend_mode":     string(AIBackendModeAPI),
		"ai_tagging_base_url": server.URL + "/v1",
		"ai_tagging_model":    "vision-model",
		"ai_embedding_model":  "text-embedding-3-small",
	}).Error; err != nil {
		t.Fatalf("更新 API embedding 设置失败: %v", err)
	}

	stageVideo := models.Video{Name: "stage.mp4", Path: "/media/stage.mp4", Directory: "/media", Size: 10}
	kitchenVideo := models.Video{Name: "kitchen.mp4", Path: "/media/kitchen.mp4", Directory: "/media", Size: 20}
	if err := database.DB.Create(&stageVideo).Error; err != nil {
		t.Fatalf("创建舞台视频失败: %v", err)
	}
	if err := database.DB.Create(&kitchenVideo).Error; err != nil {
		t.Fatalf("创建厨房视频失败: %v", err)
	}
	createVideoEmbedding(t, stageVideo.ID, "text-embedding-3-small", []float32{1, 0})
	createVideoEmbedding(t, kitchenVideo.ID, "text-embedding-3-small", []float32{0, 1})

	svc := &VideoService{}
	videos, err := svc.SearchVideosSmart("舞台灯光", nil, 0, 0, 0, 0, 0, 0, 0, 10)
	if err != nil {
		t.Fatalf("API embedding 智能搜索失败: %v", err)
	}
	if len(videos) != 1 || videos[0].ID != stageVideo.ID {
		t.Fatalf("API embedding 搜索应召回舞台视频 got=%v want=%d", videoIDs(videos), stageVideo.ID)
	}
	if embeddingCalls != 1 {
		t.Fatalf("应调用一次 API embedding，实际 %d", embeddingCalls)
	}
}

func TestVideoEmbeddingServiceIndexesVideosWithLocalRuntime(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(map[string]interface{}{
		"ai_backend_mode": string(AIBackendModeLocal),
		"local_ml_model":  defaultLocalMLModel,
	}).Error; err != nil {
		t.Fatalf("更新本地 ML 设置失败: %v", err)
	}
	video := models.Video{Name: "clip-a.mp4", Path: "/media/clip-a.mp4", Directory: "/media", Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	runtime := &fakeLocalMLRuntime{
		model:     defaultLocalMLModel,
		embedding: []float32{0.25, 0.75},
	}
	svc := &VideoEmbeddingService{
		configProvider: SettingsAITaggingConfigProvider{},
		localMLRuntime: runtime,
		extractor:      NewAITaggingExtractor(),
	}

	result, err := svc.IndexPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("本地 ML 索引失败: %v", err)
	}
	if result.Indexed != 1 || result.Requested != 1 || result.Failed != 0 {
		t.Fatalf("索引结果不正确: %+v", result)
	}
	if runtime.starts != 1 || runtime.embedCalls != 1 {
		t.Fatalf("本地 ML runtime 调用不正确: %+v", runtime)
	}
	var stored models.VideoEmbedding
	if err := database.DB.Where("video_id = ? AND model = ? AND kind = ?", video.ID, defaultLocalMLModel, "semantic").First(&stored).Error; err != nil {
		t.Fatalf("应保存视频 embedding: %v", err)
	}
	if stored.Source != "fake-local-ml" || stored.Dimension != 2 {
		t.Fatalf("embedding 元数据不正确: %+v", stored)
	}
	var vector []float32
	if err := json.Unmarshal([]byte(stored.VectorJSON), &vector); err != nil {
		t.Fatalf("embedding JSON 不正确: %v", err)
	}
	if len(vector) != 2 || vector[0] != 0.25 || vector[1] != 0.75 {
		t.Fatalf("embedding 向量不正确: %v", vector)
	}
}

func TestVideoEmbeddingServiceIndexesVideosWithAPIEmbeddingModel(t *testing.T) {
	setupVideoServiceTestDB(t)
	binDir := t.TempDir()
	ffmpegMarker := filepath.Join(t.TempDir(), "ffmpeg-called")
	fakeFFmpeg := filepath.Join(binDir, "ffmpeg")
	script := fmt.Sprintf("#!/bin/sh\nprintf called > %q\nexit 1\n", ffmpegMarker)
	if err := os.WriteFile(fakeFFmpeg, []byte(script), 0755); err != nil {
		t.Fatalf("创建 fake ffmpeg 失败: %v", err)
	}
	t.Setenv("PATH", binDir)

	var embeddingCalls int
	var embeddingInputs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("API 索引应调用 embeddings endpoint，实际 %s", r.URL.Path)
		}
		embeddingCalls++
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("解析 embeddings 请求失败: %v", err)
		}
		embeddingInputs = append(embeddingInputs, body.Input...)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "provider-normalized-model-name",
			"data": []map[string]any{
				{"index": 0, "embedding": []float64{0.25, 0.75}},
			},
		})
	}))
	defer server.Close()

	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(map[string]interface{}{
		"ai_backend_mode":     string(AIBackendModeAPI),
		"ai_tagging_base_url": server.URL + "/v1",
		"ai_tagging_model":    "vision-model",
		"ai_embedding_model":  "text-embedding-3-small",
	}).Error; err != nil {
		t.Fatalf("更新 API embedding 设置失败: %v", err)
	}
	videoPath := filepath.Join(t.TempDir(), "clip-api.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "clip-api.mp4", Path: videoPath, Directory: filepath.Dir(videoPath), Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	svc := &VideoService{}
	result, err := svc.IndexAIEmbeddings(context.Background(), 10)
	if err != nil {
		t.Fatalf("API embedding 索引失败: %v", err)
	}
	if result.Indexed != 1 || result.Requested != 1 || result.Failed != 0 {
		t.Fatalf("索引结果不正确: %+v", result)
	}
	if embeddingCalls != 1 {
		t.Fatalf("应调用一次 API embedding，实际 %d", embeddingCalls)
	}
	if len(embeddingInputs) != 1 || !strings.Contains(embeddingInputs[0], "clip-api.mp4") {
		t.Fatalf("API embedding 应收到视频文本证据，实际 %#v", embeddingInputs)
	}
	var stored models.VideoEmbedding
	if err := database.DB.Where("video_id = ? AND model = ? AND kind = ?", video.ID, "text-embedding-3-small", "semantic").First(&stored).Error; err != nil {
		t.Fatalf("应按用户配置的 embedding 模型保存 API 视频 embedding: %v", err)
	}
	if stored.Source != "api-embedding" || stored.Dimension != 2 {
		t.Fatalf("API embedding 元数据不正确: %+v", stored)
	}
	if _, err := os.Stat(ffmpegMarker); err == nil {
		t.Fatalf("API embedding 索引只需要文本证据，不应触发 ffmpeg 抽帧")
	} else if !os.IsNotExist(err) {
		t.Fatalf("检查 fake ffmpeg marker 失败: %v", err)
	}
}

func createVideoEmbedding(t *testing.T, videoID uint, model string, vector []float32) {
	t.Helper()
	data, err := json.Marshal(vector)
	if err != nil {
		t.Fatalf("序列化 embedding 失败: %v", err)
	}
	if err := database.DB.Create(&models.VideoEmbedding{
		VideoID:    videoID,
		Model:      model,
		Kind:       "semantic",
		VectorJSON: string(data),
		Dimension:  len(vector),
		Source:     "test",
	}).Error; err != nil {
		t.Fatalf("创建视频 embedding 失败: %v", err)
	}
}

func videoIDs(videos []models.Video) []uint {
	ids := make([]uint, 0, len(videos))
	for _, video := range videos {
		ids = append(ids, video.ID)
	}
	return ids
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

func TestAddTagToVideoIsIdempotent(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}

	tag := models.Tag{Name: "idempotent", Color: "#fff"}
	video := models.Video{Name: "idempotent.mp4", Path: "/tmp/idempotent.mp4", Directory: "/tmp", Size: 1}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	if err := svc.AddTagToVideo(video.ID, tag.ID); err != nil {
		t.Fatalf("首次添加标签失败: %v", err)
	}
	if err := svc.AddTagToVideo(video.ID, tag.ID); err != nil {
		t.Fatalf("重复添加标签应保持幂等，实际失败: %v", err)
	}

	var count int64
	if err := database.DB.Table("video_tags").
		Where("video_id = ? AND tag_id = ?", video.ID, tag.ID).
		Count(&count).Error; err != nil {
		t.Fatalf("统计视频标签失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复添加后应只有 1 条关联，实际 %d", count)
	}
}

func TestBatchRemoveTagFromVideosReportsPartialFailures(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}

	tag := models.Tag{Name: "batch-remove", Color: "#fff"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	videoA := models.Video{Name: "a.mp4", Path: "/tmp/batch-remove-a.mp4", Directory: "/tmp", Size: 1}
	videoB := models.Video{Name: "b.mp4", Path: "/tmp/batch-remove-b.mp4", Directory: "/tmp", Size: 1}
	if err := database.DB.Create(&videoA).Error; err != nil {
		t.Fatalf("创建视频A失败: %v", err)
	}
	if err := database.DB.Create(&videoB).Error; err != nil {
		t.Fatalf("创建视频B失败: %v", err)
	}
	if err := database.DB.Model(&videoA).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("视频A添加标签失败: %v", err)
	}
	if err := database.DB.Model(&videoB).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("视频B添加标签失败: %v", err)
	}

	result := svc.BatchRemoveTagFromVideos([]uint{videoA.ID, 999999, videoB.ID}, tag.ID)
	if result.Requested != 3 || result.Succeeded != 2 || result.Failed != 1 {
		t.Fatalf("批量移除结果错误: %#v", result)
	}

	var loaded models.Video
	if err := database.DB.Preload("Tags").First(&loaded, videoA.ID).Error; err != nil {
		t.Fatalf("读取视频标签失败: %v", err)
	}
	if len(loaded.Tags) != 0 {
		t.Fatalf("期望视频A标签已移除，实际 %#v", loaded.Tags)
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
