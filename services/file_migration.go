package services

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
)

type FolderMigrationResult struct {
	Source                string `json:"source"`
	Destination           string `json:"destination"`
	VideosUpdated         int    `json:"videos_updated"`
	DirectoriesUpdated    int    `json:"directories_updated"`
	TrashEntriesUpdated   int    `json:"trash_entries_updated"`
	ScanExclusionsUpdated int    `json:"scan_exclusions_updated"`
	Warning               string `json:"warning,omitempty"`
}

type FileMigrationResult struct {
	VideoID     uint   `json:"video_id"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Warning     string `json:"warning,omitempty"`
}

// MoveVideo moves a managed video and its sibling SRT into an existing directory.
// The filesystem move is rolled back when the database path update fails.
func (s *VideoService) MoveVideo(id uint, destinationDirectory string) (*FileMigrationResult, error) {
	libraryPathMutationMu.Lock()
	defer libraryPathMutationMu.Unlock()

	destinationDirectory, err := existingDirectory(destinationDirectory)
	if err != nil {
		return nil, err
	}

	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return nil, fmt.Errorf("视频不存在: %w", err)
	}
	sourceInfo, err := os.Stat(video.Path)
	if err != nil {
		return nil, fmt.Errorf("源文件不存在: %w", err)
	}
	if sourceInfo.IsDir() {
		return nil, fmt.Errorf("视频路径不能是文件夹: %s", video.Path)
	}

	oldPath := filepath.Clean(video.Path)
	newPath := filepath.Join(destinationDirectory, filepath.Base(oldPath))
	if oldPath == newPath {
		return &FileMigrationResult{VideoID: id, Source: oldPath, Destination: newPath}, nil
	}
	if err := requireMissingPath(newPath, "目标文件"); err != nil {
		return nil, err
	}
	var occupied models.Video
	if err := database.DB.Where("path = ? AND id <> ?", newPath, video.ID).First(&occupied).Error; err == nil {
		return nil, fmt.Errorf("目标路径已被其他视频记录占用: %s", newPath)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("检查目标路径失败: %w", err)
	}

	oldSubtitlePath := subtitleparser.SRTPathForVideo(oldPath)
	newSubtitlePath := subtitleparser.SRTPathForVideo(newPath)
	subtitleExists, err := pathExists(oldSubtitlePath)
	if err != nil {
		return nil, fmt.Errorf("检查字幕文件失败: %w", err)
	}
	if subtitleExists {
		if err := requireMissingPath(newSubtitlePath, "目标字幕文件"); err != nil {
			return nil, err
		}
	}

	videoRetainedSource, err := moveFileNoReplace(oldPath, newPath)
	if err != nil {
		return nil, fmt.Errorf("迁移视频文件失败: %w", err)
	}
	subtitleMoved := false
	subtitleRetainedSource := ""
	if subtitleExists {
		subtitleRetainedSource, err = moveFileNoReplace(oldSubtitlePath, newSubtitlePath)
		if err != nil {
			rollbackErr := rollbackMovedFile(oldPath, newPath, videoRetainedSource)
			if rollbackErr != nil {
				return nil, errors.Join(fmt.Errorf("迁移字幕文件失败: %w", err), fmt.Errorf("回滚视频文件失败: %w", rollbackErr))
			}
			return nil, fmt.Errorf("迁移字幕文件失败: %w", err)
		}
		subtitleMoved = true
	}

	updateErr := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&video).Updates(map[string]interface{}{
			"path":         newPath,
			"directory":    destinationDirectory,
			"is_stale":     false,
			"stale_reason": "",
		}).Error; err != nil {
			return err
		}
		if err := registerStagedSources(tx, &video.ID, []stagedSourceInput{
			{Original: oldPath, Staged: videoRetainedSource},
			{Original: oldSubtitlePath, Staged: subtitleRetainedSource},
		}); err != nil {
			return err
		}
		return syncShortVideoTagForVideo(tx, video.ID)
	})
	if updateErr != nil {
		rollbackErrors := []error{fmt.Errorf("更新数据库失败: %w", updateErr)}
		if subtitleMoved {
			if err := rollbackMovedFile(oldSubtitlePath, newSubtitlePath, subtitleRetainedSource); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("回滚字幕文件失败: %w", err))
			}
		}
		if err := rollbackMovedFile(oldPath, newPath, videoRetainedSource); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("回滚视频文件失败: %w", err))
		}
		return nil, errors.Join(rollbackErrors...)
	}

	if subtitleExists {
		if err := indexSubtitleFileForVideoID(video.ID, newSubtitlePath); err != nil {
			log.Printf("视频迁移后刷新字幕索引失败 id=%d path=%s err=%v", video.ID, newSubtitlePath, err)
			if deleteErr := deleteSubtitleIndex(video.ID); deleteErr != nil {
				log.Printf("视频迁移后清理失效字幕索引失败 id=%d err=%v", video.ID, deleteErr)
			}
		}
	} else if err := deleteSubtitleIndex(video.ID); err != nil {
		log.Printf("视频迁移后清理无字幕索引失败 id=%d err=%v", video.ID, err)
	}
	result := &FileMigrationResult{VideoID: id, Source: oldPath, Destination: newPath}
	retained := make([]string, 0, 2)
	if videoRetainedSource != "" {
		retained = append(retained, videoRetainedSource)
	}
	if subtitleRetainedSource != "" {
		retained = append(retained, subtitleRetainedSource)
	}
	if len(retained) > 0 {
		result.Warning = fmt.Sprintf("跨文件系统复制已完成；为防止外部写入导致数据丢失，源文件保留在: %s", strings.Join(retained, "、"))
	}
	return result, nil
}

func (s *VideoService) BatchMoveVideos(videoIDs []uint, destinationDirectory string) *BatchVideoOperationResult {
	result := newBatchVideoOperationResult(videoIDs)
	for _, videoID := range videoIDs {
		migration, err := s.MoveVideo(videoID, destinationDirectory)
		result.record(videoID, err)
		if migration != nil && migration.Warning != "" {
			result.Warnings = append(result.Warnings, BatchVideoOperationWarning{VideoID: videoID, Warning: migration.Warning})
		}
	}
	return result
}

// MoveDirectory moves a directory below destinationParent and rewrites every
// managed video and configured scan-directory path contained by the source.
func (s *VideoService) MoveDirectory(sourceDirectory, destinationParent string) (*FolderMigrationResult, error) {
	libraryPathMutationMu.Lock()
	defer libraryPathMutationMu.Unlock()

	sourceDirectory, err := existingSourceDirectory(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("源文件夹无效: %w", err)
	}
	destinationParent, err = existingDirectory(destinationParent)
	if err != nil {
		return nil, fmt.Errorf("目标文件夹无效: %w", err)
	}
	realSourceDirectory, err := filepath.EvalSymlinks(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("解析源文件夹真实路径失败: %w", err)
	}
	realDestinationParent, err := filepath.EvalSymlinks(destinationParent)
	if err != nil {
		return nil, fmt.Errorf("解析目标文件夹真实路径失败: %w", err)
	}
	if realSourceDirectory == realDestinationParent || isPathInside(realDestinationParent, realSourceDirectory) {
		return nil, fmt.Errorf("目标文件夹不能是源文件夹本身或其子目录")
	}

	destinationDirectory := filepath.Join(destinationParent, filepath.Base(sourceDirectory))
	realDestinationDirectory := filepath.Join(realDestinationParent, filepath.Base(realSourceDirectory))
	if realSourceDirectory == realDestinationDirectory {
		return &FolderMigrationResult{Source: sourceDirectory, Destination: destinationDirectory}, nil
	}
	if err := requireMissingPath(destinationDirectory, "目标文件夹"); err != nil {
		return nil, err
	}

	var videos []models.Video
	if err := database.DB.Unscoped().Find(&videos).Error; err != nil {
		return nil, fmt.Errorf("读取视频记录失败: %w", err)
	}
	occupiedPaths := make(map[string]uint)
	for i := range videos {
		if videos[i].DeletedAt.IsValid() || pathIsEqualOrInside(videos[i].Path, sourceDirectory) {
			continue
		}
		occupiedPaths[filepath.Clean(videos[i].Path)] = videos[i].ID
	}
	projectedPaths := make(map[string]uint)
	for i := range videos {
		if videos[i].DeletedAt.IsValid() || !pathIsEqualOrInside(videos[i].Path, sourceDirectory) {
			continue
		}
		newPath, replaceErr := replacePathPrefix(videos[i].Path, sourceDirectory, destinationDirectory)
		if replaceErr != nil {
			return nil, replaceErr
		}
		cleanedNewPath := filepath.Clean(newPath)
		if occupiedID, exists := occupiedPaths[cleanedNewPath]; exists {
			return nil, fmt.Errorf("迁移后的路径已被视频记录 %d 占用: %s", occupiedID, cleanedNewPath)
		}
		if projectedID, exists := projectedPaths[cleanedNewPath]; exists {
			return nil, fmt.Errorf("视频记录 %d 和 %d 会迁移到同一路径: %s", projectedID, videos[i].ID, cleanedNewPath)
		}
		projectedPaths[cleanedNewPath] = videos[i].ID
	}

	if err := copyDirectoryNoReplace(sourceDirectory, destinationDirectory); err != nil {
		return nil, fmt.Errorf("迁移文件夹失败: %w", err)
	}
	stagingDirectory, err := migrationStagingPath(sourceDirectory)
	if err != nil {
		_ = os.RemoveAll(destinationDirectory)
		return nil, fmt.Errorf("创建源文件夹暂存路径失败: %w", err)
	}
	if err := os.Rename(sourceDirectory, stagingDirectory); err != nil {
		cleanupErr := os.RemoveAll(destinationDirectory)
		if cleanupErr != nil {
			return nil, errors.Join(fmt.Errorf("暂存源文件夹失败: %w", err), fmt.Errorf("清理目标副本失败: %w", cleanupErr))
		}
		return nil, fmt.Errorf("暂存源文件夹失败: %w", err)
	}
	rollbackCopiedDirectory := func(cause error) error {
		rollbackErrors := []error{cause}
		if exists, checkErr := pathExists(sourceDirectory); checkErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("检查源文件夹回滚目标失败: %w", checkErr))
		} else if exists {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("源路径已被占用，原文件夹保留在: %s", stagingDirectory))
		} else if rollbackErr := os.Rename(stagingDirectory, sourceDirectory); rollbackErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("回滚源文件夹失败，原文件夹保留在 %s: %w", stagingDirectory, rollbackErr))
		}
		if cleanupErr := os.RemoveAll(destinationDirectory); cleanupErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("清理目标副本失败: %w", cleanupErr))
		}
		return errors.Join(rollbackErrors...)
	}
	if err := verifyDirectoryCopy(stagingDirectory, destinationDirectory); err != nil {
		return nil, rollbackCopiedDirectory(fmt.Errorf("源文件夹在迁移期间发生变化: %w", err))
	}
	independentCopy, err := directoryCopyUsesIndependentFiles(stagingDirectory, destinationDirectory)
	if err != nil {
		return nil, rollbackCopiedDirectory(fmt.Errorf("检查跨文件系统副本失败: %w", err))
	}

	result := &FolderMigrationResult{Source: sourceDirectory, Destination: destinationDirectory}
	stagedSize := int64(0)
	if independentCopy {
		stagedSize = directoryFileBytes(stagingDirectory)
	}
	updateErr := database.Transaction(func(tx *gorm.DB) error {
		counts, err := rewriteLibraryPathPrefixTx(tx, sourceDirectory, destinationDirectory)
		if err != nil {
			return err
		}
		result.VideosUpdated = counts.Videos
		result.DirectoriesUpdated = counts.Directories
		result.TrashEntriesUpdated = counts.TrashEntries
		result.ScanExclusionsUpdated = counts.ScanExclusions
		if independentCopy {
			// 跨盘时源文件夹被保留为暂存路径，与数据库切换同一事务登记，之后才有入口管理它。
			if err := registerStagedSources(tx, nil, []stagedSourceInput{{Original: sourceDirectory, Staged: stagingDirectory, Size: stagedSize}}); err != nil {
				return err
			}
		}
		return syncShortVideoTags(tx)
	})
	if updateErr != nil {
		return nil, rollbackCopiedDirectory(fmt.Errorf("更新迁移路径失败: %w", updateErr))
	}
	if err := verifyDirectoryCopy(stagingDirectory, destinationDirectory); err != nil {
		result.Warning = fmt.Sprintf("目标副本和数据库已更新，但源文件夹随后发生变化；为避免数据丢失，源数据保留在 %s: %v", stagingDirectory, err)
		log.Printf("文件夹迁移最终校验失败 staging=%s destination=%s err=%v", stagingDirectory, destinationDirectory, err)
	} else if independentCopy {
		result.Warning = fmt.Sprintf("跨文件系统复制已完成；为防止外部写入导致数据丢失，源文件夹保留在: %s", stagingDirectory)
	} else if err := os.RemoveAll(stagingDirectory); err != nil {
		result.Warning = fmt.Sprintf("目标副本和数据库已更新，但清理源文件夹失败；剩余数据位于 %s: %v", stagingDirectory, err)
		log.Printf("文件夹迁移清理源目录失败 staging=%s destination=%s err=%v", stagingDirectory, destinationDirectory, err)
	}

	reindexSubtitlesUnderDirectory(destinationDirectory, "文件夹迁移后")
	return result, nil
}

// RenameDirectory renames a managed directory in place and rewrites every
// persisted path that points at the directory or one of its descendants.
func (s *VideoService) RenameDirectory(sourceDirectory, newName string) (*FolderMigrationResult, error) {
	libraryPathMutationMu.Lock()
	defer libraryPathMutationMu.Unlock()

	sourceDirectory, err := existingSourceDirectory(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("源文件夹无效: %w", err)
	}
	newName, err = validDirectoryName(newName)
	if err != nil {
		return nil, err
	}
	parentDirectory := filepath.Dir(sourceDirectory)
	if parentDirectory == sourceDirectory {
		return nil, fmt.Errorf("不支持重命名文件系统根目录")
	}
	destinationDirectory := filepath.Join(parentDirectory, newName)
	result := &FolderMigrationResult{Source: sourceDirectory, Destination: destinationDirectory}
	if destinationDirectory == sourceDirectory {
		return result, nil
	}

	sourceInfo, err := os.Lstat(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("读取源文件夹失败: %w", err)
	}
	if destinationInfo, destinationErr := os.Lstat(destinationDirectory); destinationErr == nil {
		if !os.SameFile(sourceInfo, destinationInfo) {
			return nil, fmt.Errorf("目标文件夹已存在: %s", destinationDirectory)
		}
	} else if !errors.Is(destinationErr, os.ErrNotExist) {
		return nil, fmt.Errorf("检查目标文件夹失败: %w", destinationErr)
	}

	var videos []models.Video
	if err := database.DB.Unscoped().Find(&videos).Error; err != nil {
		return nil, fmt.Errorf("读取视频记录失败: %w", err)
	}
	var directories []models.ScanDirectory
	if err := database.DB.Unscoped().Find(&directories).Error; err != nil {
		return nil, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	affected := false
	occupiedPaths := make(map[string]uint)
	for i := range videos {
		if pathIsEqualOrInside(videos[i].Path, sourceDirectory) {
			affected = true
			continue
		}
		if !videos[i].DeletedAt.IsValid() {
			occupiedPaths[filepath.Clean(videos[i].Path)] = videos[i].ID
		}
	}
	projectedPaths := make(map[string]uint)
	for i := range videos {
		if videos[i].DeletedAt.IsValid() || !pathIsEqualOrInside(videos[i].Path, sourceDirectory) {
			continue
		}
		newPath, replaceErr := replacePathPrefix(videos[i].Path, sourceDirectory, destinationDirectory)
		if replaceErr != nil {
			return nil, replaceErr
		}
		cleanedNewPath := filepath.Clean(newPath)
		if occupiedID, exists := occupiedPaths[cleanedNewPath]; exists {
			return nil, fmt.Errorf("重命名后的路径已被视频记录 %d 占用: %s", occupiedID, cleanedNewPath)
		}
		if projectedID, exists := projectedPaths[cleanedNewPath]; exists {
			return nil, fmt.Errorf("视频记录 %d 和 %d 会映射到同一路径: %s", projectedID, videos[i].ID, cleanedNewPath)
		}
		projectedPaths[cleanedNewPath] = videos[i].ID
	}
	for i := range directories {
		if pathIsEqualOrInside(directories[i].Path, sourceDirectory) {
			affected = true
			break
		}
	}
	if !affected {
		return nil, fmt.Errorf("所选文件夹不包含已收录视频，也不是已配置的扫描目录")
	}

	if err := os.Rename(sourceDirectory, destinationDirectory); err != nil {
		return nil, fmt.Errorf("重命名文件夹失败: %w", err)
	}
	rollbackFilesystem := func(cause error) error {
		if rollbackErr := os.Rename(destinationDirectory, sourceDirectory); rollbackErr != nil {
			return errors.Join(cause, fmt.Errorf("回滚文件夹名称失败，新路径保留在 %s: %w", destinationDirectory, rollbackErr))
		}
		return cause
	}

	updateErr := database.Transaction(func(tx *gorm.DB) error {
		counts, err := rewriteLibraryPathPrefixTx(tx, sourceDirectory, destinationDirectory)
		if err != nil {
			return err
		}
		result.VideosUpdated = counts.Videos
		result.DirectoriesUpdated = counts.Directories
		result.TrashEntriesUpdated = counts.TrashEntries
		result.ScanExclusionsUpdated = counts.ScanExclusions
		return syncShortVideoTags(tx)
	})
	if updateErr != nil {
		return nil, rollbackFilesystem(fmt.Errorf("更新重命名路径失败: %w", updateErr))
	}

	reindexSubtitlesUnderDirectory(destinationDirectory, "文件夹重命名后")
	return result, nil
}

func validDirectoryName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("文件夹名称不能为空或使用 . / ..")
	}
	if strings.ContainsAny(name, `/\\`) || filepath.Base(name) != name {
		return "", fmt.Errorf("文件夹名称不能包含路径分隔符")
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("文件夹名称包含无效字符")
	}
	return name, nil
}

func existingDirectory(path string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("文件夹路径不能为空")
	}
	absolute, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("解析文件夹路径失败: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("文件夹不存在: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("路径不是文件夹: %s", absolute)
	}
	return absolute, nil
}

func existingSourceDirectory(path string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("文件夹路径不能为空")
	}
	absolute, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("解析文件夹路径失败: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("文件夹不存在: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("不支持迁移符号链接文件夹，请选择其真实目录: %s", absolute)
	}
	return existingDirectory(absolute)
}

func requireMissingPath(path, label string) error {
	exists, err := pathExists(path)
	if err != nil {
		return fmt.Errorf("检查%s失败: %w", label, err)
	}
	if exists {
		return fmt.Errorf("%s已存在: %s", label, path)
	}
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func isPathInside(path, parent string) bool {
	return pathIsEqualOrInside(path, parent) && filepath.Clean(path) != filepath.Clean(parent)
}

func pathIsEqualOrInside(path, parent string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func replacePathPrefix(path, oldPrefix, newPrefix string) (string, error) {
	rel, err := filepath.Rel(filepath.Clean(oldPrefix), filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径 %s 不在源文件夹 %s 内", path, oldPrefix)
	}
	if rel == "." {
		return filepath.Clean(newPrefix), nil
	}
	return filepath.Join(newPrefix, rel), nil
}

// PathRewriteCounts 是一次前缀改写各类持久化路径的更新条数。
type PathRewriteCounts struct {
	Videos          int `json:"videos"`
	Directories     int `json:"directories"`
	TrashEntries    int `json:"trash_entries"`
	ScanExclusions  int `json:"scan_exclusions"`
	SubtitleIndexes int `json:"subtitle_indexes"`
}

// rewriteLibraryPathPrefixTx 是「路径前缀改写」的唯一实现（D-PC07）：文件夹改名、
// 文件夹迁移、编辑扫描目录（重映射）三处共用。覆盖视频（含软删，并清除失效标记与原因）、
// 回收站条目的 original_path / trash_path、扫描黑名单、字幕索引路径与扫描目录。
// 只改数据库，不碰磁盘；调用方负责事务、锁与文件系统侧的回滚。
func rewriteLibraryPathPrefixTx(tx *gorm.DB, oldPrefix, newPrefix string) (PathRewriteCounts, error) {
	var counts PathRewriteCounts
	oldPrefix = filepath.Clean(strings.TrimSpace(oldPrefix))
	newPrefix = filepath.Clean(strings.TrimSpace(newPrefix))
	if oldPrefix == "" || oldPrefix == "." || newPrefix == "" || newPrefix == "." {
		return counts, fmt.Errorf("路径前缀为空")
	}
	like := escapeSQLLikePrefix(scanRootChildPrefix(oldPrefix)) + "%"
	rewrite := func(path string) (string, bool, error) {
		if !pathIsEqualOrInside(path, oldPrefix) {
			return "", false, nil
		}
		rewritten, err := replacePathPrefix(path, oldPrefix, newPrefix)
		return rewritten, err == nil, err
	}

	var videos []models.Video
	if err := tx.Unscoped().Select("id", "path").
		Where(`path = ? OR path LIKE ? ESCAPE '\'`, oldPrefix, like).Find(&videos).Error; err != nil {
		return counts, fmt.Errorf("读取视频记录失败: %w", err)
	}
	for _, video := range videos {
		newPath, ok, err := rewrite(video.Path)
		if err != nil {
			return counts, err
		}
		if !ok {
			continue
		}
		if err := tx.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).Updates(map[string]interface{}{
			"path": newPath, "directory": filepath.Dir(newPath), "is_stale": false, "stale_reason": "",
		}).Error; err != nil {
			return counts, err
		}
		counts.Videos++
	}

	var directories []models.ScanDirectory
	if err := tx.Unscoped().Find(&directories).Error; err != nil {
		return counts, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	for _, directory := range directories {
		newPath, ok, err := rewrite(directory.Path)
		if err != nil {
			return counts, err
		}
		if !ok {
			continue
		}
		if err := tx.Unscoped().Model(&models.ScanDirectory{}).Where("id = ?", directory.ID).Update("path", newPath).Error; err != nil {
			return counts, err
		}
		counts.Directories++
	}

	var trashEntries []models.VideoTrashEntry
	if err := tx.Where(`original_path = ? OR original_path LIKE ? ESCAPE '\' OR trash_path = ? OR trash_path LIKE ? ESCAPE '\'`,
		oldPrefix, like, oldPrefix, like).Find(&trashEntries).Error; err != nil {
		return counts, fmt.Errorf("读取回收站记录失败: %w", err)
	}
	for _, entry := range trashEntries {
		updates := make(map[string]interface{})
		if newPath, ok, err := rewrite(entry.OriginalPath); err != nil {
			return counts, err
		} else if ok {
			updates["original_path"] = newPath
		}
		if entry.TrashPath != "" {
			if newPath, ok, err := rewrite(entry.TrashPath); err != nil {
				return counts, err
			} else if ok {
				updates["trash_path"] = newPath
			}
		}
		if len(updates) == 0 {
			continue
		}
		if err := tx.Model(&models.VideoTrashEntry{}).Where("id = ?", entry.ID).Updates(updates).Error; err != nil {
			return counts, err
		}
		counts.TrashEntries++
	}

	var settings models.Settings
	if err := tx.First(&settings).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return counts, fmt.Errorf("读取扫描设置失败: %w", err)
		}
	} else {
		excluded := parseScanExcludePaths(settings.ScanExcludePaths)
		changed := 0
		for i := range excluded {
			newPath, ok, err := rewrite(excluded[i])
			if err != nil {
				return counts, err
			}
			if ok {
				excluded[i] = newPath
				changed++
			}
		}
		if changed > 0 {
			normalized := normalizeScanExcludePaths(strings.Join(excluded, "\n"))
			if err := tx.Model(&models.Settings{}).Where("id = ?", settings.ID).Update("scan_exclude_paths", normalized).Error; err != nil {
				return counts, err
			}
			counts.ScanExclusions = changed
		}
	}

	var indexes []models.SubtitleIndexState
	if err := tx.Select("id", "subtitle_path").
		Where(`subtitle_path = ? OR subtitle_path LIKE ? ESCAPE '\'`, oldPrefix, like).Find(&indexes).Error; err != nil {
		return counts, fmt.Errorf("读取字幕索引失败: %w", err)
	}
	for _, index := range indexes {
		newPath, ok, err := rewrite(index.SubtitlePath)
		if err != nil {
			return counts, err
		}
		if !ok {
			continue
		}
		if err := tx.Model(&models.SubtitleIndexState{}).Where("id = ?", index.ID).Update("subtitle_path", newPath).Error; err != nil {
			return counts, err
		}
		counts.SubtitleIndexes++
	}
	return counts, nil
}

// reindexSubtitlesUnderDirectory 在路径改写提交后按磁盘现状重建目录下活跃视频的字幕索引。
// 失败只记日志：索引可以随时由扫描重建，不该让已经完成的迁移报错。
func reindexSubtitlesUnderDirectory(directory, action string) {
	videos, err := activeVideosUnderRoots([]string{directory})
	if err != nil {
		log.Printf("%s读取视频失败 dir=%s err=%v", action, directory, err)
		return
	}
	for _, video := range videos {
		if !pathIsEqualOrInside(video.Path, directory) {
			continue
		}
		srtPath := subtitleparser.SRTPathForVideo(video.Path)
		exists, checkErr := pathExists(srtPath)
		if checkErr == nil && exists {
			if indexErr := indexSubtitleFileForVideoID(video.ID, srtPath); indexErr != nil {
				log.Printf("%s刷新字幕索引失败 id=%d path=%s err=%v", action, video.ID, srtPath, indexErr)
				if deleteErr := deleteSubtitleIndex(video.ID); deleteErr != nil {
					log.Printf("%s清理失效字幕索引失败 id=%d err=%v", action, video.ID, deleteErr)
				}
			}
			continue
		}
		if checkErr != nil {
			log.Printf("%s检查字幕失败 id=%d path=%s err=%v", action, video.ID, srtPath, checkErr)
		}
		if deleteErr := deleteSubtitleIndex(video.ID); deleteErr != nil {
			log.Printf("%s清理无效字幕索引失败 id=%d err=%v", action, video.ID, deleteErr)
		}
	}
}

// MoveTargetCheck 是迁移目标的预检结果（LIB-09）。
type MoveTargetCheck struct {
	// InScanRoots：目标目录位于某个扫描目录之内（或就是扫描目录）。
	// 不在时，迁移后的视频会在下次全量扫描时变成孤儿而失效。
	InScanRoots bool `json:"in_scan_roots"`
}

// CheckMoveTarget 判断迁移目标是否落在扫描根之内。目标目录不要求存在；
// 同时按原路径与解析符号链接后的路径比较，避免 /var 与 /private/var 之类的别名误报。
func (s *VideoService) CheckMoveTarget(targetDir string) (*MoveTargetCheck, error) {
	target := filepath.Clean(strings.TrimSpace(targetDir))
	if target == "" || target == "." {
		return nil, fmt.Errorf("目标文件夹不能为空")
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return nil, fmt.Errorf("解析目标文件夹失败: %w", err)
	}
	candidates := []string{absolute}
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil && resolved != absolute {
		candidates = append(candidates, resolved)
	}
	var dirs []models.ScanDirectory
	if err := database.DB.Find(&dirs).Error; err != nil {
		return nil, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	for _, root := range cleanScanRoots(dirs) {
		roots := []string{root}
		if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil && resolved != root {
			roots = append(roots, resolved)
		}
		for _, candidate := range candidates {
			if pathBelongsToAny(candidate, roots) {
				return &MoveTargetCheck{InScanRoots: true}, nil
			}
		}
	}
	return &MoveTargetCheck{}, nil
}
