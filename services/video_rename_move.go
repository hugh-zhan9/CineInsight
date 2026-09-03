package services

import (
	"context"
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

// RelocateVideo 更新视频路径（文件迁移场景，保留标签等元数据）
func (s *VideoService) RelocateVideo(id uint, newPath string) error {
	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()
	return s.relocateVideo(id, newPath)
}

func (s *VideoService) relocateVideo(id uint, newPath string) error {
	newPath = filepath.Clean(strings.TrimSpace(newPath))

	// 验证新路径文件存在
	info, err := os.Stat(newPath)
	if err != nil {
		return fmt.Errorf("目标文件不存在: %w", err)
	}

	// 检查新路径是否已被其他记录占用
	var existing models.Video
	if err := database.DB.Where("path = ? AND id != ?", newPath, id).First(&existing).Error; err == nil {
		return fmt.Errorf("目标路径已被其他记录占用: %s", newPath)
	}

	if err := database.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.Video{}).Where("id = ?", id).Updates(map[string]interface{}{
			"path":      newPath,
			"directory": filepath.Dir(newPath),
			"name":      filepath.Base(newPath),
			"size":      info.Size(),
		})
		if result.Error != nil {
			return result.Error
		}
		return syncShortVideoTagForVideo(tx, id)
	}); err != nil {
		return err
	}
	if probeErr := s.technicalProbe().Refresh(context.Background(), id); probeErr != nil {
		log.Printf("视频迁移完成但技术信息读取失败 id=%d newPath=%s err=%v", id, newPath, probeErr)
	} else if syncErr := database.Transaction(func(tx *gorm.DB) error { return syncShortVideoTagForVideo(tx, id) }); syncErr != nil {
		log.Printf("视频迁移技术信息读取成功但短视频标签同步失败 id=%d newPath=%s err=%v", id, newPath, syncErr)
	}
	log.Printf("视频迁移并更新元数据 id=%d newPath=%s", id, newPath)
	return nil
}

// RenameVideo 重命名视频文件及数据库记录
func (s *VideoService) RenameVideo(id uint, newName string) error {
	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()

	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("文件名不能为空")
	}
	// 禁止路径分隔符
	if strings.ContainsAny(newName, "/\\") {
		return fmt.Errorf("文件名不能包含路径分隔符")
	}

	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return fmt.Errorf("视频不存在: %w", err)
	}

	// 保留原始扩展名（如果新名称没带扩展名）
	oldExt := filepath.Ext(video.Name)
	if filepath.Ext(newName) == "" {
		newName = newName + oldExt
	}

	oldPath := video.Path
	newPath := filepath.Join(video.Directory, newName)
	oldSubtitlePath := subtitleparser.SRTPathForVideo(oldPath)
	newSubtitlePath := subtitleparser.SRTPathForVideo(newPath)
	subtitlePathChanged := filepath.Clean(oldSubtitlePath) != filepath.Clean(newSubtitlePath)

	// 新旧路径相同则跳过
	if oldPath == newPath {
		return nil
	}

	// 检查目标路径是否已存在
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("目标文件已存在: %s", newName)
	}
	subtitleExists := false
	if _, err := os.Stat(oldSubtitlePath); err == nil {
		subtitleExists = true
		if subtitlePathChanged {
			if _, err := os.Stat(newSubtitlePath); err == nil {
				return fmt.Errorf("目标字幕文件已存在: %s", filepath.Base(newSubtitlePath))
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("检查目标字幕文件失败: %w", err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查字幕文件失败: %w", err)
	}

	subtitleMoved := subtitleExists && subtitlePathChanged
	if subtitleMoved {
		if err := os.Rename(oldSubtitlePath, newSubtitlePath); err != nil {
			return fmt.Errorf("重命名字幕文件失败: %w", err)
		}
	}

	// 重命名磁盘文件
	if err := os.Rename(oldPath, newPath); err != nil {
		if subtitleMoved {
			if rollbackErr := os.Rename(newSubtitlePath, oldSubtitlePath); rollbackErr != nil {
				return errors.Join(fmt.Errorf("重命名文件失败: %w", err), fmt.Errorf("回滚字幕文件失败: %w", rollbackErr))
			}
		}
		return fmt.Errorf("重命名文件失败: %w", err)
	}

	// 更新数据库记录
	if err := database.DB.Model(&video).Updates(map[string]interface{}{
		"name": newName,
		"path": newPath,
	}).Error; err != nil {
		rollbackErrors := []error{fmt.Errorf("更新数据库失败: %w", err)}
		if rollbackErr := os.Rename(newPath, oldPath); rollbackErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("回滚视频文件失败: %w", rollbackErr))
		}
		if subtitleMoved {
			if rollbackErr := os.Rename(newSubtitlePath, oldSubtitlePath); rollbackErr != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("回滚字幕文件失败: %w", rollbackErr))
			}
		}
		return errors.Join(rollbackErrors...)
	}
	if subtitleExists {
		if err := indexSubtitleFileForVideoID(id, newSubtitlePath); err != nil {
			log.Printf("视频重命名后刷新字幕索引失败 id=%d path=%s err=%v", id, newSubtitlePath, err)
		}
	}

	log.Printf("视频重命名 id=%d oldName=%s newName=%s", id, video.Name, newName)
	return nil
}
