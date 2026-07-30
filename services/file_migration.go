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
	Source             string `json:"source"`
	Destination        string `json:"destination"`
	VideosUpdated      int    `json:"videos_updated"`
	DirectoriesUpdated int    `json:"directories_updated"`
	Warning            string `json:"warning,omitempty"`
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

	updateErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&video).Updates(map[string]interface{}{
			"path":      newPath,
			"directory": destinationDirectory,
			"is_stale":  false,
		}).Error; err != nil {
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
	var directories []models.ScanDirectory
	if err := database.DB.Unscoped().Find(&directories).Error; err != nil {
		return nil, fmt.Errorf("读取扫描目录失败: %w", err)
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
	updateErr := database.DB.Transaction(func(tx *gorm.DB) error {
		for i := range videos {
			if !pathIsEqualOrInside(videos[i].Path, sourceDirectory) {
				continue
			}
			newPath, err := replacePathPrefix(videos[i].Path, sourceDirectory, destinationDirectory)
			if err != nil {
				return err
			}
			if err := tx.Unscoped().Model(&models.Video{}).Where("id = ?", videos[i].ID).Updates(map[string]interface{}{
				"path":      newPath,
				"directory": filepath.Dir(newPath),
				"is_stale":  false,
			}).Error; err != nil {
				return err
			}
			videos[i].Path = newPath
			videos[i].Directory = filepath.Dir(newPath)
			result.VideosUpdated++
		}
		for i := range directories {
			if !pathIsEqualOrInside(directories[i].Path, sourceDirectory) {
				continue
			}
			newPath, err := replacePathPrefix(directories[i].Path, sourceDirectory, destinationDirectory)
			if err != nil {
				return err
			}
			if err := tx.Unscoped().Model(&models.ScanDirectory{}).Where("id = ?", directories[i].ID).Update("path", newPath).Error; err != nil {
				return err
			}
			result.DirectoriesUpdated++
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

	for i := range videos {
		if !pathIsEqualOrInside(videos[i].Path, destinationDirectory) || videos[i].DeletedAt.IsValid() {
			continue
		}
		srtPath := subtitleparser.SRTPathForVideo(videos[i].Path)
		exists, checkErr := pathExists(srtPath)
		if checkErr == nil && exists {
			if indexErr := indexSubtitleFileForVideoID(videos[i].ID, srtPath); indexErr != nil {
				log.Printf("文件夹迁移后刷新字幕索引失败 id=%d path=%s err=%v", videos[i].ID, srtPath, indexErr)
				if deleteErr := deleteSubtitleIndex(videos[i].ID); deleteErr != nil {
					log.Printf("文件夹迁移后清理失效字幕索引失败 id=%d err=%v", videos[i].ID, deleteErr)
				}
			}
		} else {
			if checkErr != nil {
				log.Printf("文件夹迁移后检查字幕失败 id=%d path=%s err=%v", videos[i].ID, srtPath, checkErr)
			}
			if deleteErr := deleteSubtitleIndex(videos[i].ID); deleteErr != nil {
				log.Printf("文件夹迁移后清理无效字幕索引失败 id=%d err=%v", videos[i].ID, deleteErr)
			}
		}
	}
	return result, nil
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
