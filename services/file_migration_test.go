package services

import (
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestMoveVideoMovesFileSubtitleAndDatabasePath(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source")
	destinationDirectory := filepath.Join(root, "destination")
	if err := os.MkdirAll(destinationDirectory, 0755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}
	oldPath := filepath.Join(sourceDirectory, "clip.mp4")
	oldSRTPath := filepath.Join(sourceDirectory, "clip.srt")
	mustCreateFile(t, oldPath)
	if err := os.WriteFile(oldSRTPath, []byte("1\n00:00:01,000 --> 00:00:02,000\nmoved subtitle\n\n"), 0644); err != nil {
		t.Fatalf("创建字幕失败: %v", err)
	}
	video := models.Video{Name: "clip.mp4", Path: oldPath, Directory: sourceDirectory, Duration: 30}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频记录失败: %v", err)
	}

	if _, err := (&VideoService{}).MoveVideo(video.ID, destinationDirectory); err != nil {
		t.Fatalf("迁移视频失败: %v", err)
	}
	newPath := filepath.Join(destinationDirectory, "clip.mp4")
	newSRTPath := filepath.Join(destinationDirectory, "clip.srt")
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("源视频应已迁移, err=%v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("目标视频不存在: %v", err)
	}
	if _, err := os.Stat(newSRTPath); err != nil {
		t.Fatalf("目标字幕不存在: %v", err)
	}
	var loaded models.Video
	if err := database.DB.Preload("Tags").First(&loaded, video.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if loaded.Path != newPath || loaded.Directory != destinationDirectory {
		t.Fatalf("数据库路径未更新: %+v", loaded)
	}
	if len(loaded.Tags) != 1 || loaded.Tags[0].Name != ShortVideoTagName {
		t.Fatalf("迁移后应同步短视频标签: %+v", loaded.Tags)
	}
	var state models.SubtitleIndexState
	if err := database.DB.Where("video_id = ?", video.ID).First(&state).Error; err != nil {
		t.Fatalf("字幕索引未建立: %v", err)
	}
	if state.SubtitlePath != newSRTPath {
		t.Fatalf("字幕索引路径错误: %s", state.SubtitlePath)
	}
}

func TestMoveVideoRejectsDestinationConflictWithoutChangingSource(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source")
	destinationDirectory := filepath.Join(root, "destination")
	oldPath := filepath.Join(sourceDirectory, "clip.mp4")
	conflictPath := filepath.Join(destinationDirectory, "clip.mp4")
	mustCreateFile(t, oldPath)
	mustCreateFile(t, conflictPath)
	video := models.Video{Name: "clip.mp4", Path: oldPath, Directory: sourceDirectory}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频记录失败: %v", err)
	}

	if _, err := (&VideoService{}).MoveVideo(video.ID, destinationDirectory); err == nil {
		t.Fatal("目标冲突时迁移应失败")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("失败后源文件应保留: %v", err)
	}
	var loaded models.Video
	if err := database.DB.First(&loaded, video.ID).Error; err != nil || loaded.Path != oldPath {
		t.Fatalf("失败后数据库路径不应变化: %+v err=%v", loaded, err)
	}
}

func TestMoveVideoRejectsDatabasePathConflictBeforeMovingFile(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source")
	destinationDirectory := filepath.Join(root, "destination")
	if err := os.MkdirAll(destinationDirectory, 0755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}
	oldPath := filepath.Join(sourceDirectory, "clip.mp4")
	newPath := filepath.Join(destinationDirectory, "clip.mp4")
	mustCreateFile(t, oldPath)
	video := models.Video{Name: "clip.mp4", Path: oldPath, Directory: sourceDirectory}
	conflictingRecord := models.Video{Name: "missing.mp4", Path: newPath, Directory: destinationDirectory}
	if err := database.DB.Create(&[]*models.Video{&video, &conflictingRecord}).Error; err != nil {
		t.Fatalf("创建视频记录失败: %v", err)
	}

	if _, err := (&VideoService{}).MoveVideo(video.ID, destinationDirectory); err == nil {
		t.Fatal("数据库目标路径冲突时迁移应失败")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("数据库冲突时源文件应保留: %v", err)
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("数据库冲突时目标文件不应出现, err=%v", err)
	}
	var loaded models.Video
	if err := database.DB.First(&loaded, video.ID).Error; err != nil || loaded.Path != oldPath {
		t.Fatalf("数据库冲突时源记录不应变化: %+v err=%v", loaded, err)
	}
}

func TestMoveDirectoryPreservesRelativePathsAndUpdatesScanDirectories(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "library")
	destinationParent := filepath.Join(root, "archive")
	if err := os.MkdirAll(destinationParent, 0755); err != nil {
		t.Fatalf("创建目标父目录失败: %v", err)
	}
	oldPath := filepath.Join(sourceDirectory, "season", "episode.mp4")
	mustCreateFile(t, oldPath)
	video := models.Video{Name: "episode.mp4", Path: oldPath, Directory: filepath.Dir(oldPath), Duration: 90}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频记录失败: %v", err)
	}
	directory := models.ScanDirectory{Path: sourceDirectory, Alias: "媒体库"}
	if err := database.DB.Create(&directory).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}

	result, err := (&VideoService{}).MoveDirectory(sourceDirectory, destinationParent)
	if err != nil {
		t.Fatalf("迁移文件夹失败: %v", err)
	}
	wantDirectory := filepath.Join(destinationParent, "library")
	wantPath := filepath.Join(wantDirectory, "season", "episode.mp4")
	if result.VideosUpdated != 1 || result.DirectoriesUpdated != 1 || result.Destination != wantDirectory {
		t.Fatalf("迁移结果错误: %+v", result)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("迁移后的文件不存在: %v", err)
	}
	if _, err := os.Stat(sourceDirectory); !os.IsNotExist(err) {
		t.Fatalf("同文件系统迁移成功后源文件夹应被清理, err=%v", err)
	}
	var loaded models.Video
	if err := database.DB.First(&loaded, video.ID).Error; err != nil {
		t.Fatalf("读取迁移后视频失败: %v", err)
	}
	if loaded.Path != wantPath || loaded.Directory != filepath.Dir(wantPath) {
		t.Fatalf("视频相对路径未保留: %+v", loaded)
	}
	var loadedDirectory models.ScanDirectory
	if err := database.DB.First(&loadedDirectory, directory.ID).Error; err != nil {
		t.Fatalf("读取扫描目录失败: %v", err)
	}
	if loadedDirectory.Path != wantDirectory {
		t.Fatalf("扫描目录路径未更新: %+v", loadedDirectory)
	}
}

func TestMoveDirectoryRejectsProjectedDatabasePathConflictBeforeMoving(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "library")
	destinationParent := filepath.Join(root, "archive")
	if err := os.MkdirAll(destinationParent, 0755); err != nil {
		t.Fatalf("创建目标父目录失败: %v", err)
	}
	oldPath := filepath.Join(sourceDirectory, "season", "episode.mp4")
	mustCreateFile(t, oldPath)
	projectedPath := filepath.Join(destinationParent, "library", "season", "episode.mp4")
	video := models.Video{Name: "episode.mp4", Path: oldPath, Directory: filepath.Dir(oldPath)}
	conflictingRecord := models.Video{Name: "missing.mp4", Path: projectedPath, Directory: filepath.Dir(projectedPath)}
	if err := database.DB.Create(&[]*models.Video{&video, &conflictingRecord}).Error; err != nil {
		t.Fatalf("创建视频记录失败: %v", err)
	}

	if _, err := (&VideoService{}).MoveDirectory(sourceDirectory, destinationParent); err == nil {
		t.Fatal("预测目标路径被占用时文件夹迁移应失败")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("冲突时源文件夹应保持不变: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destinationParent, "library")); !os.IsNotExist(err) {
		t.Fatalf("冲突时不应创建目标文件夹, err=%v", err)
	}
}

func TestMoveDirectoryRejectsSymlinkSourceWithoutChangingTarget(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	realSource := filepath.Join(root, "real-library")
	symlinkSource := filepath.Join(root, "linked-library")
	destinationParent := filepath.Join(root, "archive")
	mustCreateFile(t, filepath.Join(realSource, "clip.mp4"))
	if err := os.Symlink(realSource, symlinkSource); err != nil {
		t.Fatalf("创建源文件夹符号链接失败: %v", err)
	}
	if err := os.MkdirAll(destinationParent, 0755); err != nil {
		t.Fatalf("创建目标父目录失败: %v", err)
	}

	if _, err := (&VideoService{}).MoveDirectory(symlinkSource, destinationParent); err == nil {
		t.Fatal("符号链接源文件夹应被拒绝")
	}
	if _, err := os.Stat(filepath.Join(realSource, "clip.mp4")); err != nil {
		t.Fatalf("拒绝后真实源文件必须保留: %v", err)
	}
	if _, err := os.Lstat(symlinkSource); err != nil {
		t.Fatalf("拒绝后源符号链接必须保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destinationParent, "linked-library")); !os.IsNotExist(err) {
		t.Fatalf("拒绝后不应创建目标目录, err=%v", err)
	}
}

func TestMoveDirectoryRejectsDestinationAliasInsideSource(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	source := filepath.Join(root, "library")
	insideSource := filepath.Join(source, "inner")
	destinationAlias := filepath.Join(root, "destination-alias")
	mustCreateFile(t, filepath.Join(source, "clip.mp4"))
	if err := os.MkdirAll(insideSource, 0755); err != nil {
		t.Fatalf("创建源内目录失败: %v", err)
	}
	if err := os.Symlink(insideSource, destinationAlias); err != nil {
		t.Fatalf("创建目标别名失败: %v", err)
	}

	if _, err := (&VideoService{}).MoveDirectory(source, destinationAlias); err == nil {
		t.Fatal("指向源目录内部的目标别名必须被拒绝")
	}
	if _, err := os.Stat(filepath.Join(source, "clip.mp4")); err != nil {
		t.Fatalf("拒绝后源文件应保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(insideSource, "library")); !os.IsNotExist(err) {
		t.Fatalf("拒绝后不能在源树内创建递归目标, err=%v", err)
	}
}

func TestCopyFileNoReplaceSupportsCopyThenDeleteMigrationWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.mp4")
	destination := filepath.Join(root, "destination.mp4")
	if err := os.WriteFile(source, []byte("video-data"), 0640); err != nil {
		t.Fatalf("创建源文件失败: %v", err)
	}
	if err := copyFileNoReplace(source, destination); err != nil {
		t.Fatalf("排他复制文件失败: %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "video-data" {
		t.Fatalf("目标文件内容错误: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("复制阶段不应删除源文件: %v", err)
	}
	if err := os.WriteFile(destination, []byte("occupied"), 0644); err != nil {
		t.Fatalf("改写目标文件失败: %v", err)
	}
	if err := copyFileNoReplace(source, destination); err == nil {
		t.Fatal("已有目标文件时排他复制必须失败")
	}
	data, err = os.ReadFile(destination)
	if err != nil || string(data) != "occupied" {
		t.Fatalf("冲突目标不能被覆盖: data=%q err=%v", data, err)
	}
}

func TestCopyDirectoryNoReplacePreservesTreeAndRejectsExistingTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	mustCreateFile(t, filepath.Join(source, "nested", "clip.mp4"))
	if err := os.Symlink("clip.mp4", filepath.Join(source, "nested", "clip-link.mp4")); err != nil {
		t.Fatalf("创建符号链接失败: %v", err)
	}
	if err := copyDirectoryNoReplace(source, destination); err != nil {
		t.Fatalf("复制文件夹失败: %v", err)
	}
	if err := verifyDirectoryCopy(source, destination); err != nil {
		t.Fatalf("复制后的目录不一致: %v", err)
	}
	if err := copyDirectoryNoReplace(source, destination); err == nil {
		t.Fatal("已有目标文件夹时必须拒绝复制")
	}
}

func TestVerifyDirectoryCopyDetectsSameSizeContentChange(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatalf("创建源目录失败: %v", err)
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "clip.mp4"), []byte("abc"), 0644); err != nil {
		t.Fatalf("写入源文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, "clip.mp4"), []byte("abd"), 0644); err != nil {
		t.Fatalf("写入目标文件失败: %v", err)
	}
	if err := verifyDirectoryCopy(source, destination); err == nil {
		t.Fatal("同尺寸内容变化必须被最终校验发现")
	}
}

func TestMoveFileRetainsStagingWhenIndependentCopyIsRequired(t *testing.T) {
	root := t.TempDir()
	realSource := filepath.Join(root, "real.mp4")
	symlinkSource := filepath.Join(root, "linked.mp4")
	destination := filepath.Join(root, "destination.mp4")
	if err := os.WriteFile(realSource, []byte("video"), 0644); err != nil {
		t.Fatalf("创建真实源文件失败: %v", err)
	}
	if err := os.Symlink(realSource, symlinkSource); err != nil {
		t.Fatalf("创建源符号链接失败: %v", err)
	}

	retained, err := moveFileNoReplace(symlinkSource, destination)
	if err != nil {
		t.Fatalf("独立复制迁移失败: %v", err)
	}
	if retained == "" {
		t.Fatal("独立复制后必须保留暂存源以避免并发写数据丢失")
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("目标副本不存在: %v", err)
	}
	if _, err := os.Lstat(retained); err != nil {
		t.Fatalf("暂存源未保留: %v", err)
	}
	if err := rollbackMovedFile(symlinkSource, destination, retained); err != nil {
		t.Fatalf("回滚保留源失败: %v", err)
	}
	if _, err := os.Lstat(symlinkSource); err != nil {
		t.Fatalf("回滚后源路径未恢复: %v", err)
	}
}
