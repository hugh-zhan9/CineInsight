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
	return s.RelocateVideoContext(context.Background(), id, newPath)
}
func (s *VideoService) RelocateVideoContext(ctx context.Context, id uint, newPath string) error {
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return database.WithOperationContext(ctx, func(db *gorm.DB) error { return s.relocateVideoFrom(db, id, newPath) })
}

func (s *VideoService) relocateVideo(id uint, newPath string) error {
	return s.relocateVideoFrom(database.DB, id, newPath)
}
func (s *VideoService) relocateVideoFrom(db *gorm.DB, id uint, newPath string) error {
	newPath = filepath.Clean(strings.TrimSpace(newPath))
	if err := s.relocateVideoRecord(db, id, newPath); err != nil {
		return err
	}
	s.refreshRelocatedVideo(db, id, newPath)
	log.Printf("视频迁移并更新元数据 id=%d newPath=%s", id, newPath)
	return nil
}

// relocateVideoRecord 是重新定位的数据库部分（校验新路径、改写路径与大小、清除失效、同步短视频标签），不含补探测。
// 失联找回在路径写锁内只做这一步，放开锁之后再补探测（D-LD-03）。
func (s *VideoService) relocateVideoRecord(db *gorm.DB, id uint, newPath string) error {
	if err := s.checkBTPath(newPath); err != nil {
		return err
	}
	var old models.Video
	if err := db.First(&old, id).Error; err != nil {
		return err
	}
	if err := s.checkBTPath(old.Path); err != nil {
		return err
	}
	newPath = filepath.Clean(strings.TrimSpace(newPath))

	// 验证新路径文件存在。错误文案会直接显示给用户，不带路径（G-3）。
	info, err := os.Stat(newPath)
	if err != nil {
		return fmt.Errorf("目标文件不存在: %w", pathlessError(err))
	}

	// 检查新路径是否已被其他记录占用
	var existing models.Video
	if err := db.Where("path = ? AND id != ?", newPath, id).First(&existing).Error; err == nil {
		return errors.New("目标路径已被其他记录占用")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// 改了文件名：经 UpdateVideoTitleFields 一并写片名排序键（UI 二期 §2.3）。
		result := UpdateVideoTitleFields(tx.Model(&models.Video{}).Where("id = ?", id), id, map[string]interface{}{
			"path":         newPath,
			"directory":    filepath.Dir(newPath),
			"name":         filepath.Base(newPath),
			"size":         info.Size(),
			"is_stale":     false,
			"stale_reason": "",
		})
		if result.Error != nil {
			return result.Error
		}
		return syncShortVideoTagForVideo(tx, id)
	})
}

// refreshRelocatedVideo 是路径改写提交之后的补探测：重读技术信息，成功后再同步一次短视频标签。
// 失败只记日志（路径已经改好）。失联找回把扫描器回收站行改回活跃后也走这里（D-LD-03）。
func (s *VideoService) refreshRelocatedVideo(db *gorm.DB, id uint, newPath string) {
	if probeErr := s.technicalProbe().Refresh(db.Statement.Context, id); probeErr != nil {
		log.Printf("视频迁移完成但技术信息读取失败 id=%d newPath=%s err=%v", id, newPath, probeErr)
	} else if syncErr := db.Transaction(func(tx *gorm.DB) error { return syncShortVideoTagForVideo(tx, id) }); syncErr != nil {
		log.Printf("视频迁移技术信息读取成功但短视频标签同步失败 id=%d newPath=%s err=%v", id, newPath, syncErr)
	}
}

// RenameVideo 重命名视频文件及数据库记录
func (s *VideoService) RenameVideo(id uint, newName string) error {
	unlock, err := rLockLibraryPaths()
	if err != nil {
		return err
	}
	defer unlock()

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

	// 只有「视频扩展名」设置里列出的后缀才算扩展名；The.Matrix.1999.1080p 里的 .1080p
	// 不是，要补回原扩展名（LIB-03）。
	oldExt := filepath.Ext(video.Name)
	if ext := filepath.Ext(newName); ext == "" || !isConfiguredVideoExtension(ext) {
		newName = newName + oldExt
	}
	if newName == video.Name {
		return nil
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
	if err := UpdateVideoTitleFields(database.DB.Model(&video), video.ID, map[string]interface{}{
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

// ConfiguredVideoExtensions 返回「视频扩展名」设置里的后缀（小写、带点、去重）；设置为空或读不到时用默认集合。
func ConfiguredVideoExtensions() []string {
	raw := defaultVideoExtensions
	var settings models.Settings
	if database.DB != nil {
		if err := database.DB.Select("video_extensions").First(&settings).Error; err == nil && strings.TrimSpace(settings.VideoExtensions) != "" {
			raw = settings.VideoExtensions
		}
	}
	seen := map[string]bool{}
	exts := []string{}
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if !strings.HasPrefix(candidate, ".") {
			candidate = "." + candidate
		}
		if !seen[candidate] {
			seen[candidate] = true
			exts = append(exts, candidate)
		}
	}
	return exts
}

// isConfiguredVideoExtension 判断后缀（带点）是否在「视频扩展名」设置里；设置为空时用默认集合。
func isConfiguredVideoExtension(ext string) bool {
	ext = strings.ToLower(ext)
	for _, candidate := range ConfiguredVideoExtensions() {
		if candidate == ext {
			return true
		}
	}
	return false
}
