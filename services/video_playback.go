package services

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// OpenDirectory 打开文件所在目录
func (s *VideoService) OpenDirectory(videoID uint) error {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return err
	}

	return openPath(video.Directory, true)
}

// PlayVideo 使用系统默认播放器发起正式播放
func (s *VideoService) PlayVideo(videoID uint) (*PlaybackAttemptResult, error) {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return nil, err
	}

	return s.dispatchFormalPlayback(&video, false)
}

var openWithDefaultFn = openPath

// openPath 使用系统默认方式打开路径（文件或目录）
// Windows 下目录用 explorer，文件用 cmd /c start；其他平台统一用 open/xdg-open
func openPath(path string, isDir bool) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		if isDir {
			cmd = exec.Command("explorer", path)
		} else {
			cmd = exec.Command("cmd", "/c", "start", "", path)
		}
	case "linux":
		cmd = exec.Command("xdg-open", path)
	default:
		return ErrUnsupportedOS
	}

	return cmd.Start()
}

// revealPath 在系统文件管理器里定位到该文件本身，而不是只打开所在目录——
// 审阅重复文件时"哪一份被选中了"比"打开哪个文件夹"更有用。
// Linux 没有跨发行版的通用做法，退回打开所在目录。
func revealPath(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return runRevealCommand(exec.Command("open", "-R", path))
	case "windows":
		// explorer 自己解析命令行，Go 会把含空格的整个参数加引号变成 "/select,C:\a b\x.jpg"，
		// 那样 explorer 认不出开关。必须写成 /select,"<path>" 的形式。
		cmd := exec.Command("explorer")
		cmd.SysProcAttr = revealSysProcAttr(`/select,"` + path + `"`)
		if cmd.SysProcAttr == nil {
			return exec.Command("explorer", "/select,"+path).Start()
		}
		return runRevealCommand(cmd)
	case "linux":
		return openPath(filepath.Dir(path), true)
	default:
		return ErrUnsupportedOS
	}
}

// runRevealCommand 等待进程退出以便把失败返回给调用方；这些命令都是即起即退的
// 轻量启动器，等待不会卡住界面。
func runRevealCommand(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Wait()
}

func (s *VideoService) dispatchFormalPlayback(video *models.Video, random bool) (*PlaybackAttemptResult, error) {
	info, err := os.Stat(video.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.buildPlaybackFailureResult(video, "file_missing", "源文件不存在或已被移动。", true), nil
		}
		return s.buildPlaybackFailureResult(video, "path_unreadable", err.Error(), false), nil
	}
	if info.IsDir() {
		return s.buildPlaybackFailureResult(video, "path_is_directory", "当前路径不是可播放文件。", true), nil
	}

	// 续播口径由设置决定：默认交给播放器自己（IINA 会接着上次播），
	// 也可以要求从头播——那种情况下走 iina-cli 显式关掉续播。
	resumeMode := PlaybackResumeModeResume
	var settings models.Settings
	if err := database.DB.Select("playback_resume_mode").First(&settings).Error; err == nil {
		resumeMode = settings.PlaybackResumeMode
	}
	if err := launchPlayback(video, resumeMode); err != nil {
		return s.buildPlaybackFailureResult(video, "dispatch_failed", err.Error(), false), nil
	}

	now := time.Now()
	updates := map[string]interface{}{
		"last_played_at": now,
		"is_stale":       false,
	}
	source := models.PlayEventSourceDesktopPlay
	if random {
		updates["random_play_count"] = gorm.Expr("random_play_count + 1")
		video.RandomPlayCount++
		source = models.PlayEventSourceDesktopRandom
	} else {
		updates["play_count"] = gorm.Expr("play_count + 1")
		video.PlayCount++
	}
	if err := recordFormalPlaybackStats(video, updates, now, source); err != nil {
		log.Printf("更新播放统计失败 id=%d err=%v", video.ID, err)
	}
	video.LastPlayedAt = &now
	video.IsStale = false

	return &PlaybackAttemptResult{
		Video:             video,
		DispatchSucceeded: true,
	}, nil
}

// recordFormalPlaybackStats 把计数递增与播放事件写在同一个事务里。
//
// 两者必须同生同死：账本要能解释计数是怎么长出来的，一边写成功一边写失败会让
// 洞察页和随机算法看到互相矛盾的两份历史。失败语义沿用改造前——调用方只记一行
// 日志，播放本身已经成功了，不因为统计写不进去就报错给用户。
func recordFormalPlaybackStats(video *models.Video, updates map[string]interface{}, playedAt time.Time, source string) error {
	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(video).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Create(&models.PlayEvent{VideoID: video.ID, PlayedAt: playedAt, Source: source}).Error
	})
}

func (s *VideoService) buildPlaybackFailureResult(video *models.Video, reasonCode string, detail string, shouldReconcile bool) *PlaybackAttemptResult {
	result := &PlaybackAttemptResult{
		Video:             video,
		DispatchSucceeded: false,
		ReasonCode:        reasonCode,
		UserMessage:       fmt.Sprintf("播放失败: %s (%s)\n原因: %s", video.Name, video.Path, detail),
	}
	if shouldReconcile {
		result.ReconcileResult = s.reconcileAfterPlaybackFailure(video, reasonCode)
	}
	return result
}

func (s *VideoService) reconcileAfterPlaybackFailure(video *models.Video, reasonCode string) *PlaybackReconcileResult {
	result := &PlaybackReconcileResult{
		VideoID:    video.ID,
		ReasonCode: reasonCode,
	}

	if err := database.DB.Model(video).Update("is_stale", true).Error; err == nil {
		video.IsStale = true
		result.DidMarkStale = true
	}

	matchedPath, ambiguous, err := s.findRelocatedVideoCandidate(video)
	if err != nil {
		log.Printf("自动纠偏扫描失败 id=%d err=%v", video.ID, err)
		result.NeedsReload = true
		if updatedVideo, loadErr := s.GetVideo(video.ID); loadErr == nil {
			updatedVideo.IsStale = true
			result.UpdatedVideo = updatedVideo
		}
		return result
	}

	if ambiguous {
		result.NeedsReload = true
		if updatedVideo, loadErr := s.GetVideo(video.ID); loadErr == nil {
			result.UpdatedVideo = updatedVideo
		}
		return result
	}

	if matchedPath != "" && matchedPath != video.Path {
		if err := s.RelocateVideo(video.ID, matchedPath); err == nil {
			_ = database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("is_stale", false).Error
			if updatedVideo, loadErr := s.GetVideo(video.ID); loadErr == nil {
				updatedVideo.IsStale = false
				result.DidRelocate = true
				result.UpdatedVideo = updatedVideo
				return result
			}
		}
		result.NeedsReload = true
		return result
	}

	result.NeedsReload = true
	if updatedVideo, loadErr := s.GetVideo(video.ID); loadErr == nil {
		result.UpdatedVideo = updatedVideo
	}
	return result
}

func (s *VideoService) findRelocatedVideoCandidate(video *models.Video) (string, bool, error) {
	var directories []models.ScanDirectory
	if err := database.DB.Order("path asc").Find(&directories).Error; err != nil {
		return "", false, err
	}

	if len(directories) == 0 {
		return "", false, nil
	}

	primary := make([]string, 0, len(directories))
	secondary := make([]string, 0, len(directories))
	for _, dir := range directories {
		cleanPath := filepath.Clean(dir.Path)
		if cleanPath == "" {
			continue
		}
		prefix := cleanPath + string(os.PathSeparator)
		if video.Directory == cleanPath || strings.HasPrefix(video.Directory, prefix) {
			primary = append(primary, cleanPath)
		} else {
			secondary = append(secondary, cleanPath)
		}
	}

	roots := append(primary, secondary...)
	seenCandidates := map[string]struct{}{}
	for _, root := range roots {
		scannedFiles, err := s.ScanDirectoryWithInfo(root)
		if err != nil {
			return "", false, err
		}
		for _, candidate := range scannedFiles {
			if filepath.Base(candidate.Path) != video.Name {
				continue
			}
			if candidate.Size != video.Size {
				continue
			}
			seenCandidates[candidate.Path] = struct{}{}
			if len(seenCandidates) > 1 {
				return "", true, nil
			}
		}
	}

	for candidatePath := range seenCandidates {
		return candidatePath, false, nil
	}

	return "", false, nil
}
