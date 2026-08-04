package services

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
	if err != nil || len(matches) != 1 || matches[0].Video.ID != video.ID {
		t.Fatalf("重命名后字幕搜索未命中: matches=%#v err=%v", matches, err)
	}
}

func TestRenameVideoChangingOnlyExtensionKeepsSiblingSubtitle(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	oldVideoPath := filepath.Join(root, "movie.mp4")
	newVideoPath := filepath.Join(root, "movie.mkv")
	srtPath := filepath.Join(root, "movie.srt")
	mustCreateFile(t, oldVideoPath)
	if err := os.WriteFile(srtPath, []byte("1\n00:00:01,000 --> 00:00:03,000\nextension rename\n\n"), 0644); err != nil {
		t.Fatalf("写入字幕文件失败: %v", err)
	}
	video := models.Video{Name: "movie.mp4", Path: oldVideoPath, Directory: root, Size: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := indexSubtitleFileForVideoID(video.ID, srtPath); err != nil {
		t.Fatalf("索引字幕失败: %v", err)
	}

	if err := svc.RenameVideo(video.ID, "movie.mkv"); err != nil {
		t.Fatalf("修改视频扩展名失败: %v", err)
	}
	if _, err := os.Stat(newVideoPath); err != nil {
		t.Fatalf("新视频文件不存在: %v", err)
	}
	if _, err := os.Stat(srtPath); err != nil {
		t.Fatalf("同名字幕应保留在原路径: %v", err)
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

func TestScanDirectoryBlacklistSkipsSubtreeWithoutDeletingExistingRecords(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	excluded := filepath.Join(root, "private")
	visiblePath := filepath.Join(root, "visible.mp4")
	excludedPath := filepath.Join(excluded, "hidden.mp4")
	for _, path := range []string{visiblePath, excludedPath} {
		mustCreateFile(t, path)
		mustSetFileModTime(t, path, time.Now().Add(-10*time.Minute))
	}
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", excluded).Error; err != nil {
		t.Fatalf("保存扫描黑名单失败: %v", err)
	}
	existing := models.Video{Name: filepath.Base(excludedPath), Path: excludedPath, Directory: excluded, Size: 1}
	if err := database.DB.Create(&existing).Error; err != nil {
		t.Fatalf("创建黑名单内既有记录失败: %v", err)
	}

	files, err := svc.ScanDirectory(root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(files) != 1 || files[0] != visiblePath {
		t.Fatalf("黑名单子树不应进入扫描结果: %v", files)
	}
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root, Alias: "root"}})
	if result.Deleted != 0 {
		t.Fatalf("黑名单内既有记录不得被误判为缺失: %+v", result)
	}
	if err := database.DB.First(&models.Video{}, existing.ID).Error; err != nil {
		t.Fatalf("黑名单内既有记录应保留: %v", err)
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

	var entry struct {
		VideoID      uint
		OriginalPath string
		TrashPath    string
		FileMoved    bool
	}
	if err := database.DB.Table("video_trash_entries").Where("video_id = ?", video.ID).Take(&entry).Error; err != nil {
		t.Fatalf("期望删除操作创建可恢复条目: %v", err)
	}
	if entry.VideoID != video.ID || entry.OriginalPath != videoPath || entry.TrashPath != trashPath || !entry.FileMoved {
		t.Fatalf("回收站条目与删除结果不一致: %#v", entry)
	}
}

func TestListTrashEntriesReturnsNewestFirst(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	first := models.Video{Name: "first.mp4", Path: filepath.Join(root, "first.mp4"), Directory: root, Size: 1}
	second := models.Video{Name: "second.mp4", Path: filepath.Join(root, "second.mp4"), Directory: root, Size: 1}
	mustCreateFile(t, first.Path)
	mustCreateFile(t, second.Path)
	if err := database.DB.Create(&first).Error; err != nil {
		t.Fatalf("创建第一个视频失败: %v", err)
	}
	if err := database.DB.Create(&second).Error; err != nil {
		t.Fatalf("创建第二个视频失败: %v", err)
	}
	if err := svc.DeleteVideo(first.ID, false); err != nil {
		t.Fatalf("删除第一个视频记录失败: %v", err)
	}
	if err := svc.DeleteVideo(second.ID, false); err != nil {
		t.Fatalf("删除第二个视频记录失败: %v", err)
	}

	lister, ok := any(svc).(interface {
		ListTrashEntries() ([]models.VideoTrashEntry, error)
	})
	if !ok {
		t.Fatalf("VideoService 应提供 ListTrashEntries")
	}
	entries, err := lister.ListTrashEntries()
	if err != nil {
		t.Fatalf("列出回收站失败: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("回收站条目数量错误: %#v", entries)
	}
	if entries[0].VideoID != second.ID || entries[1].VideoID != first.ID {
		t.Fatalf("回收站应按最新删除优先排序: %#v", entries)
	}
	if entries[0].FileMoved || entries[0].TrashPath != "" {
		t.Fatalf("仅删除记录不应标记文件已移动: %#v", entries[0])
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatalf("仅删除记录后原文件应保留: %v", err)
	}
}

func TestRestoreTrashEntryRestoresMovedFileAndSubtitleIndex(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "restorable.mp4")
	srtPath := filepath.Join(root, "restorable.srt")
	mustCreateFile(t, videoPath)
	if err := os.WriteFile(srtPath, []byte("1\n00:00:04,000 --> 00:00:06,000\nrestored subtitle phrase\n\n"), 0644); err != nil {
		t.Fatalf("写入字幕失败: %v", err)
	}
	video := models.Video{Name: "restorable.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := indexSubtitleFileForVideoID(video.ID, srtPath); err != nil {
		t.Fatalf("创建字幕索引失败: %v", err)
	}
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatalf("删除视频失败: %v", err)
	}
	entries, err := svc.ListTrashEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("读取回收站条目失败: entries=%#v err=%v", entries, err)
	}

	restorer, ok := any(svc).(interface {
		RestoreTrashEntry(uint) (*models.Video, error)
	})
	if !ok {
		t.Fatalf("VideoService 应提供 RestoreTrashEntry")
	}
	restored, err := restorer.RestoreTrashEntry(entries[0].ID)
	if err != nil {
		t.Fatalf("恢复视频失败: %v", err)
	}
	if restored.ID != video.ID || restored.Path != videoPath {
		t.Fatalf("恢复结果错误: %#v", restored)
	}
	if _, err := os.Stat(videoPath); err != nil {
		t.Fatalf("视频文件未恢复到原路径: %v", err)
	}
	if _, err := os.Stat(entries[0].TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("回收站文件应已移回原路径: %v", err)
	}
	var active models.Video
	if err := database.DB.First(&active, video.ID).Error; err != nil {
		t.Fatalf("视频记录未恢复为活动状态: %v", err)
	}
	if remaining, err := svc.ListTrashEntries(); err != nil || len(remaining) != 0 {
		t.Fatalf("恢复后应移除回收站条目: entries=%#v err=%v", remaining, err)
	}
	matches, err := (&SubtitleSearchService{}).SearchSubtitleMatches("restored subtitle", 10)
	if err != nil {
		t.Fatalf("搜索恢复后的字幕失败: %v", err)
	}
	if len(matches) != 1 || matches[0].Video.ID != video.ID || matches[0].Segment.StartTimeMs != 4000 {
		t.Fatalf("恢复后字幕索引未重建: %#v", matches)
	}
}

func TestRestoreTrashEntryRejectsOccupiedOriginalPath(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "occupied.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "occupied.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatalf("删除视频失败: %v", err)
	}
	entries, err := svc.ListTrashEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("读取回收站条目失败: entries=%#v err=%v", entries, err)
	}
	if err := os.WriteFile(videoPath, []byte("replacement"), 0644); err != nil {
		t.Fatalf("创建占位文件失败: %v", err)
	}

	if _, err := svc.RestoreTrashEntry(entries[0].ID); err == nil {
		t.Fatalf("原路径被占用时恢复应失败")
	}
	content, err := os.ReadFile(videoPath)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("恢复失败不应覆盖占位文件: content=%q err=%v", string(content), err)
	}
	if _, err := os.Stat(entries[0].TrashPath); err != nil {
		t.Fatalf("恢复失败后回收站文件应保留: %v", err)
	}
	if remaining, err := svc.ListTrashEntries(); err != nil || len(remaining) != 1 {
		t.Fatalf("恢复失败后条目应保留: entries=%#v err=%v", remaining, err)
	}
}

func TestRestoreTrashEntryRestoresRecordOnlyDeletion(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "record-only.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "record-only.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := svc.DeleteVideo(video.ID, false); err != nil {
		t.Fatalf("仅删除记录失败: %v", err)
	}
	entries, err := svc.ListTrashEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("读取回收站条目失败: entries=%#v err=%v", entries, err)
	}
	if entries[0].FileMoved || entries[0].TrashPath != "" {
		t.Fatalf("仅删除记录不应标记文件移动: %#v", entries[0])
	}

	restored, err := svc.RestoreTrashEntry(entries[0].ID)
	if err != nil {
		t.Fatalf("恢复记录失败: %v", err)
	}
	if restored.ID != video.ID {
		t.Fatalf("恢复了错误的视频: %#v", restored)
	}
	if _, err := os.Stat(videoPath); err != nil {
		t.Fatalf("记录恢复后原文件应保持不变: %v", err)
	}
}

func TestDeleteVideoRestoresFileWhenTrashEntryCannotBeRecorded(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "entry-conflict.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "entry-conflict.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Create(&models.VideoTrashEntry{
		VideoID:      video.ID,
		VideoName:    video.Name,
		OriginalPath: video.Path,
	}).Error; err != nil {
		t.Fatalf("创建冲突条目失败: %v", err)
	}

	err := svc.DeleteVideo(video.ID, true)
	if err == nil {
		t.Fatalf("回收站条目冲突时删除应失败")
	}
	if _, err := os.Stat(videoPath); err != nil {
		t.Fatalf("记录回收站条目失败后应恢复原文件: %v", err)
	}
	trashPath := filepath.Join(root, DefaultTrashDirName, filepath.Base(videoPath))
	if _, err := os.Stat(trashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("补偿后不应残留回收站文件: %v", err)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("删除失败后视频记录应保持活动: %v", err)
	}
}

func TestMoveToTrashNeverOverwritesCollidingCandidates(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, sourcePath)
	trashDir := filepath.Join(root, DefaultTrashDirName)
	if err := os.MkdirAll(trashDir, 0755); err != nil {
		t.Fatalf("创建回收站目录失败: %v", err)
	}
	fixedNow := time.Date(2026, 7, 30, 12, 34, 56, 0, time.Local)
	baseCollision := filepath.Join(trashDir, "movie.mp4")
	timestampCollision := filepath.Join(trashDir, "movie_20260730123456.mp4")
	if err := os.WriteFile(baseCollision, []byte("keep-base"), 0644); err != nil {
		t.Fatalf("创建基础碰撞文件失败: %v", err)
	}
	if err := os.WriteFile(timestampCollision, []byte("keep-timestamp"), 0644); err != nil {
		t.Fatalf("创建时间戳碰撞文件失败: %v", err)
	}

	trashService := NewTrashService()
	trashService.now = func() time.Time { return fixedNow }
	targetPath, err := trashService.MoveToTrash(sourcePath)
	if err != nil {
		t.Fatalf("移动碰撞文件失败: %v", err)
	}
	if targetPath != filepath.Join(trashDir, "movie_20260730123456_2.mp4") {
		t.Fatalf("应跳过所有已占用候选: %s", targetPath)
	}
	for path, want := range map[string]string{
		baseCollision:      "keep-base",
		timestampCollision: "keep-timestamp",
	} {
		content, readErr := os.ReadFile(path)
		if readErr != nil || string(content) != want {
			t.Fatalf("碰撞文件不应被覆盖 path=%s content=%q err=%v", path, content, readErr)
		}
	}
}

func TestReconcileTrashEntriesCompletesPendingDeleteAfterFileMove(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "pending.mp4")
	mustCreateFile(t, videoPath)
	info, err := os.Stat(videoPath)
	if err != nil {
		t.Fatalf("读取视频信息失败: %v", err)
	}
	video := models.Video{Name: "pending.mp4", Path: videoPath, Directory: root, Size: info.Size()}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	trashService := NewTrashService()
	digest, err := fileSHA256Hex(videoPath)
	if err != nil {
		t.Fatalf("计算视频摘要失败: %v", err)
	}
	entry := models.VideoTrashEntry{
		VideoID:      video.ID,
		VideoName:    video.Name,
		OriginalPath: video.Path,
		TrashPath:    trashService.TrashTargetPath(video.Path, 0),
		FileSize:     info.Size(),
		FileModTime:  info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info),
		FileSHA256:   digest,
		State:        trashStatePendingMove,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建待移动状态失败: %v", err)
	}
	if err := trashService.MoveToTrashAt(video.Path, entry.TrashPath); err != nil {
		t.Fatalf("模拟文件已移动失败: %v", err)
	}

	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatalf("启动对账失败: %v", err)
	}
	var deleted models.Video
	if err := database.DB.Unscoped().First(&deleted, video.ID).Error; err != nil || !deleted.DeletedAt.IsValid() {
		t.Fatalf("对账后视频应完成软删除: %#v err=%v", deleted, err)
	}
	var reconciled models.VideoTrashEntry
	if err := database.DB.First(&reconciled, entry.ID).Error; err != nil {
		t.Fatalf("读取对账条目失败: %v", err)
	}
	if reconciled.State != trashStateDeleted || !reconciled.FileMoved {
		t.Fatalf("待移动条目未完成: %#v", reconciled)
	}
}

func TestReconcilePendingDeleteSkipsUnownedPlannedPath(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "collision-pending.mp4")
	mustCreateFile(t, videoPath)
	info, err := os.Stat(videoPath)
	if err != nil {
		t.Fatalf("读取视频信息失败: %v", err)
	}
	video := models.Video{Name: filepath.Base(videoPath), Path: videoPath, Directory: root, Size: info.Size()}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	trashService := NewTrashService()
	digest, err := fileSHA256Hex(videoPath)
	if err != nil {
		t.Fatalf("计算视频摘要失败: %v", err)
	}
	plannedPath := trashService.TrashTargetPath(videoPath, 0)
	if err := os.MkdirAll(filepath.Dir(plannedPath), 0755); err != nil {
		t.Fatalf("创建回收站目录失败: %v", err)
	}
	if err := os.WriteFile(plannedPath, []byte("unowned"), 0644); err != nil {
		t.Fatalf("创建既有回收站文件失败: %v", err)
	}
	entry := models.VideoTrashEntry{
		VideoID:      video.ID,
		VideoName:    video.Name,
		OriginalPath: video.Path,
		TrashPath:    plannedPath,
		FileSize:     info.Size(),
		FileModTime:  info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info),
		FileSHA256:   digest,
		State:        trashStatePendingMove,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建待移动状态失败: %v", err)
	}

	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatalf("碰撞对账失败: %v", err)
	}
	content, err := os.ReadFile(plannedPath)
	if err != nil || string(content) != "unowned" {
		t.Fatalf("对账不应覆盖或删除既有文件 content=%q err=%v", content, err)
	}
	if err := database.DB.First(&entry, entry.ID).Error; err != nil {
		t.Fatalf("读取对账后条目失败: %v", err)
	}
	if entry.TrashPath == plannedPath || entry.State != trashStateDeleted || !entry.FileMoved {
		t.Fatalf("对账应选择新路径并完成删除: %#v", entry)
	}
	if _, err := os.Stat(entry.TrashPath); err != nil {
		t.Fatalf("视频应移动到新回收站路径: %v", err)
	}
}

func TestRestoreTrashEntryCancelsVisibleInterruptedDelete(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "cancel-pending.mp4")
	mustCreateFile(t, videoPath)
	info, err := os.Stat(videoPath)
	if err != nil {
		t.Fatalf("读取视频信息失败: %v", err)
	}
	digest, err := fileSHA256Hex(videoPath)
	if err != nil {
		t.Fatalf("计算视频摘要失败: %v", err)
	}
	video := models.Video{Name: filepath.Base(videoPath), Path: videoPath, Directory: root, Size: info.Size()}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	trashService := NewTrashService()
	entry := models.VideoTrashEntry{
		VideoID:      video.ID,
		VideoName:    video.Name,
		OriginalPath: video.Path,
		TrashPath:    trashService.TrashTargetPath(video.Path, 0),
		FileSize:     info.Size(),
		FileModTime:  info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info),
		FileSHA256:   digest,
		State:        trashStatePendingMove,
		LastError:    "上次移动中断",
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建中断条目失败: %v", err)
	}
	if err := trashService.MoveToTrashAt(videoPath, entry.TrashPath); err != nil {
		t.Fatalf("模拟中断删除的文件移动失败: %v", err)
	}
	entries, err := svc.ListTrashEntries()
	if err != nil || len(entries) != 1 || entries[0].State != trashStatePendingMove || entries[0].LastError == "" {
		t.Fatalf("中断条目应对用户可见: entries=%#v err=%v", entries, err)
	}

	restored, err := svc.RestoreTrashEntry(entry.ID)
	if err != nil {
		t.Fatalf("恢复中断删除失败: %v", err)
	}
	if restored.ID != video.ID {
		t.Fatalf("恢复了错误的视频: %#v", restored)
	}
	if _, err := os.Stat(videoPath); err != nil {
		t.Fatalf("文件未恢复到原路径: %v", err)
	}
	if remaining, err := svc.ListTrashEntries(); err != nil || len(remaining) != 0 {
		t.Fatalf("恢复后中断条目应清理: entries=%#v err=%v", remaining, err)
	}
}

func TestReconcilePendingDeleteRejectsSameSizeMtimeReplacement(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "identity.mp4")
	if err := os.WriteFile(videoPath, []byte("original"), 0644); err != nil {
		t.Fatalf("创建原视频失败: %v", err)
	}
	info, err := os.Stat(videoPath)
	if err != nil {
		t.Fatalf("读取原视频信息失败: %v", err)
	}
	digest, err := fileSHA256Hex(videoPath)
	if err != nil {
		t.Fatalf("计算原视频摘要失败: %v", err)
	}
	video := models.Video{Name: filepath.Base(videoPath), Path: videoPath, Directory: root, Size: info.Size()}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频记录失败: %v", err)
	}
	entry := models.VideoTrashEntry{
		VideoID:      video.ID,
		VideoName:    video.Name,
		OriginalPath: video.Path,
		TrashPath:    NewTrashService().TrashTargetPath(video.Path, 0),
		FileSize:     info.Size(),
		FileModTime:  info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info),
		FileSHA256:   digest,
		State:        trashStatePendingMove,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建待删除条目失败: %v", err)
	}
	if err := os.Remove(videoPath); err != nil {
		t.Fatalf("移除原视频失败: %v", err)
	}
	if err := os.WriteFile(videoPath, []byte("replaced"), 0644); err != nil {
		t.Fatalf("创建同大小替换文件失败: %v", err)
	}
	if err := os.Chtimes(videoPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("复原替换文件时间失败: %v", err)
	}

	if err := svc.ReconcileTrashEntries(); err == nil {
		t.Fatalf("强身份不一致时对账应失败")
	}
	content, err := os.ReadFile(videoPath)
	if err != nil || string(content) != "replaced" {
		t.Fatalf("对账不应移动替换文件 content=%q err=%v", content, err)
	}
	var retained models.VideoTrashEntry
	if err := database.DB.First(&retained, entry.ID).Error; err != nil || retained.LastError == "" {
		t.Fatalf("失败条目应保留诊断信息: %#v err=%v", retained, err)
	}
}

func TestReconcileTrashEntriesCompletesInterruptedRestore(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "restoring.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "restoring.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatalf("删除视频失败: %v", err)
	}
	entries, err := svc.ListTrashEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("读取回收站失败: entries=%#v err=%v", entries, err)
	}
	entry := entries[0]
	if err := database.DB.Model(&entry).Update("state", trashStateRestoring).Error; err != nil {
		t.Fatalf("标记恢复中失败: %v", err)
	}
	if err := NewTrashService().RestoreFromTrash(entry.TrashPath, entry.OriginalPath); err != nil {
		t.Fatalf("模拟文件已恢复失败: %v", err)
	}

	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatalf("恢复对账失败: %v", err)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("对账后视频应恢复活动状态: %v", err)
	}
	var entryCount int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", entry.ID).Count(&entryCount).Error; err != nil || entryCount != 0 {
		t.Fatalf("对账后条目应删除 count=%d err=%v", entryCount, err)
	}
}

func TestRestoreTrashEntryKeepsRetryStateWhenSubtitleIndexRebuildFails(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	videoPath := filepath.Join(root, "broken-subtitle.mp4")
	srtPath := filepath.Join(root, "broken-subtitle.srt")
	mustCreateFile(t, videoPath)
	if err := os.Mkdir(srtPath, 0755); err != nil {
		t.Fatalf("创建无效字幕路径失败: %v", err)
	}
	video := models.Video{Name: "broken-subtitle.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatalf("删除视频失败: %v", err)
	}
	entries, err := svc.ListTrashEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("读取回收站失败: entries=%#v err=%v", entries, err)
	}

	if _, err := svc.RestoreTrashEntry(entries[0].ID); err == nil || !strings.Contains(err.Error(), "字幕索引") {
		t.Fatalf("字幕索引失败应使恢复失败: %v", err)
	}
	if _, err := os.Stat(entries[0].TrashPath); err != nil {
		t.Fatalf("恢复失败后文件应回滚到回收站: %v", err)
	}
	if _, err := os.Stat(videoPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("恢复失败后原路径不应残留视频: %v", err)
	}
	remaining, err := svc.ListTrashEntries()
	if err != nil || len(remaining) != 1 || remaining[0].State != trashStateDeleted {
		t.Fatalf("恢复失败后应保留可重试条目: entries=%#v err=%v", remaining, err)
	}
}

func TestTrashTransactionOutcomeChecksDistinguishCommitFromRollback(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := models.Video{Name: "outcome.mp4", Path: filepath.Join(root, "outcome.mp4"), Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	entry := models.VideoTrashEntry{
		VideoID:      video.ID,
		VideoName:    video.Name,
		OriginalPath: video.Path,
		TrashPath:    filepath.Join(root, DefaultTrashDirName, video.Name),
		State:        trashStatePendingMove,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建操作日志失败: %v", err)
	}
	committed, rolledBack, err := confirmDeleteTransactionOutcome(video.ID, entry.ID)
	if err != nil || committed || !rolledBack {
		t.Fatalf("活动视频和 pending 日志应判定删除回滚: committed=%v rolledBack=%v err=%v", committed, rolledBack, err)
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&entry).Update("state", trashStateDeleted).Error; err != nil {
			return err
		}
		return tx.Delete(&video).Error
	}); err != nil {
		t.Fatalf("模拟删除提交失败: %v", err)
	}
	committed, rolledBack, err = confirmDeleteTransactionOutcome(video.ID, entry.ID)
	if err != nil || !committed || rolledBack {
		t.Fatalf("软删除视频和 deleted 日志应判定删除已提交: committed=%v rolledBack=%v err=%v", committed, rolledBack, err)
	}
	if err := database.DB.Model(&entry).Update("state", trashStateRestoring).Error; err != nil {
		t.Fatalf("模拟恢复中状态失败: %v", err)
	}
	committed, rolledBack, err = confirmRestoreTransactionOutcome(video.ID, entry.ID)
	if err != nil || committed || !rolledBack {
		t.Fatalf("软删除视频和 restoring 日志应判定恢复回滚: committed=%v rolledBack=%v err=%v", committed, rolledBack, err)
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Video{}).Unscoped().Where("id = ?", video.ID).Update("deleted_at", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&entry).Error
	}); err != nil {
		t.Fatalf("模拟恢复提交失败: %v", err)
	}
	committed, rolledBack, err = confirmRestoreTransactionOutcome(video.ID, entry.ID)
	if err != nil || !committed || rolledBack {
		t.Fatalf("活动视频且日志删除应判定恢复已提交: committed=%v rolledBack=%v err=%v", committed, rolledBack, err)
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

	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Size: 1, Duration: 95}
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
	if session.SeekSprite == nil {
		t.Fatalf("已知时长的 inline 预览应返回 seek sprite 索引")
	}
	if session.SeekSprite.LocatorValue != "/preview/seek-sprite/"+strconv.FormatUint(uint64(video.ID), 10) {
		t.Fatalf("seek sprite locator 错误: %s", session.SeekSprite.LocatorValue)
	}
	if session.SeekSprite.FrameCount != 10 || session.SeekSprite.IntervalSeconds != 9.5 {
		t.Fatalf("seek sprite 索引错误: %+v", session.SeekSprite)
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
