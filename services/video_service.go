package services

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

type VideoService struct {
	scanSyncMu               sync.Mutex
	mediaProbe               *MediaProbeService
	scanDirectoryWithOptions func(string, bool) ([]ScannedFile, error)
	localMetadataObserverMu  sync.RWMutex
	localMetadataObserver    func(uint, bool) error
	// playbackProxy 供预览换源与删除级联使用（D-002、D-004）。
	// 可为 nil：没接代理服务时一切退回本批之前的行为。
	playbackProxyMu sync.RWMutex
	playbackProxy   *PlaybackProxyService
}

func NewVideoService(mediaProbe *MediaProbeService) *VideoService {
	return &VideoService{mediaProbe: mediaProbe}
}

// SetLocalMetadataObserver may run while a scan is in flight, so the observer is guarded.
func (s *VideoService) SetLocalMetadataObserver(observer func(uint, bool) error) {
	s.localMetadataObserverMu.Lock()
	s.localMetadataObserver = observer
	s.localMetadataObserverMu.Unlock()
}

func (s *VideoService) observeLocalMetadata(videoID uint, isNew bool) {
	s.localMetadataObserverMu.RLock()
	observer := s.localMetadataObserver
	s.localMetadataObserverMu.RUnlock()
	if observer == nil {
		return
	}
	if err := observer(videoID, isNew); err != nil {
		log.Printf("本地元数据观察失败 video_id=%d new=%v err=%v", videoID, isNew, err)
	}
}

// SetPlaybackProxyService 注入播放代理服务（D-004）。app 层在构造完之后调用。
func (s *VideoService) SetPlaybackProxyService(service *PlaybackProxyService) {
	s.playbackProxyMu.Lock()
	s.playbackProxy = service
	s.playbackProxyMu.Unlock()
}

// playbackProxies 返回已注入的代理服务；未注入时为 nil，调用方按"没有代理"处理。
func (s *VideoService) playbackProxies() *PlaybackProxyService {
	if s == nil {
		return nil
	}
	s.playbackProxyMu.RLock()
	defer s.playbackProxyMu.RUnlock()
	return s.playbackProxy
}

func (s *VideoService) technicalProbe() *MediaProbeService {
	if s != nil && s.mediaProbe != nil {
		return s.mediaProbe
	}
	return NewMediaProbeService()
}

var libraryPathMutationMu sync.RWMutex

// BeginLibraryMaintenance waits for active path readers/writers and blocks new
// media-path mutations until the returned release function is called.
func BeginLibraryMaintenance() func() {
	libraryPathMutationMu.Lock()
	return libraryPathMutationMu.Unlock
}

type BatchVideoOperationError struct {
	VideoID uint   `json:"video_id"`
	Error   string `json:"error"`
}

type BatchVideoOperationWarning struct {
	VideoID uint   `json:"video_id"`
	Warning string `json:"warning"`
}

type BatchVideoOperationResult struct {
	Requested int                          `json:"requested"`
	Succeeded int                          `json:"succeeded"`
	Failed    int                          `json:"failed"`
	Errors    []BatchVideoOperationError   `json:"errors"`
	Warnings  []BatchVideoOperationWarning `json:"warnings"`
}

type ffprobeStream struct {
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Duration string `json:"duration"`
}

type ffprobePayload struct {
	Streams []ffprobeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func parseFFProbeOutput(output []byte) (duration float64, resolution string, width, height int, err error) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return 0, "", 0, 0, errors.New("empty ffprobe output")
	}

	var data ffprobePayload
	if err := json.Unmarshal(trimmed, &data); err != nil {
		return 0, "", 0, 0, err
	}
	if len(data.Streams) == 0 {
		return 0, "", 0, 0, errors.New("ffprobe returned no video stream")
	}

	stream := data.Streams[0]
	width = stream.Width
	height = stream.Height
	if width > 0 && height > 0 {
		resolution = fmt.Sprintf("%dx%d", width, height)
	}

	durationText := strings.TrimSpace(stream.Duration)
	if durationText == "" {
		durationText = strings.TrimSpace(data.Format.Duration)
	}
	if durationText != "" {
		if _, scanErr := fmt.Sscanf(durationText, "%f", &duration); scanErr != nil {
			return 0, "", 0, 0, fmt.Errorf("invalid duration %q: %w", durationText, scanErr)
		}
	}

	return duration, resolution, width, height, nil
}

func truncateLogSnippet(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "...(truncated)"
}

// getVideoMetadata 使用 ffprobe 获取视频时长、分辨率、宽、高
func (s *VideoService) getVideoMetadata(path string) (duration float64, resolution string, width, height int) {
	ffprobeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		// 尝试常见安装路径 (Homebrew)
		if runtime.GOOS == "darwin" {
			paths := []string{"/opt/homebrew/bin/ffprobe", "/usr/local/bin/ffprobe"}
			for _, p := range paths {
				if _, err := os.Stat(p); err == nil {
					ffprobeBin = p
					break
				}
			}
		}
	}

	if ffprobeBin == "" {
		log.Printf("[VideoService] ffprobe not found, skipping metadata extraction")
		return 0, "", 0, 0
	}

	// 获取时长和分辨率 (JSON 格式)
	cmd := exec.Command(ffprobeBin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,duration:format=duration", "-of", "json", path)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Printf("[VideoService] ffprobe failed for %s: %v stderr=%s", path, err, truncateLogSnippet(stderr.String(), 400))
		return 0, "", 0, 0
	}

	duration, resolution, width, height, err = parseFFProbeOutput(stdout.Bytes())
	if err != nil {
		log.Printf("[VideoService] failed to parse ffprobe output for %s: %v stdout=%s stderr=%s",
			path,
			err,
			truncateLogSnippet(stdout.String(), 400),
			truncateLogSnippet(stderr.String(), 400),
		)
		return 0, "", 0, 0
	}

	return duration, resolution, width, height
}

// AddVideo 添加视频
func (s *VideoService) AddVideo(path string) (*models.Video, error) {
	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()
	return s.addVideo(path)
}

func (s *VideoService) addVideo(path string) (*models.Video, error) {
	path = filepath.Clean(strings.TrimSpace(path))

	// 检查文件是否存在
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("文件不存在: %w", err)
	}
	if isKnownNonVideoSourcePath(path) {
		return nil, fmt.Errorf("不是视频文件: %s", path)
	}

	// 检查是否已存在
	var existingVideo models.Video
	if err := database.DB.Unscoped().Where("path = ?", path).First(&existingVideo).Error; err == nil {
		log.Printf("跳过已存在视频 path=%s", path)
		return &existingVideo, ErrVideoExists
	}

	video := &models.Video{
		Name:      filepath.Base(path),
		Path:      path,
		Directory: filepath.Dir(path),
		Size:      info.Size(),
	}

	err = database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(video).Error; err != nil {
			return err
		}
		return syncShortVideoTagForVideo(tx, video.ID)
	})
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "unique") || strings.Contains(errMsg, "constraint") {
			if findErr := database.DB.Where("path = ?", path).First(&existingVideo).Error; findErr == nil {
				return &existingVideo, ErrVideoExists
			}
		}
		return nil, err
	}
	if probeErr := s.technicalProbe().Refresh(context.Background(), video.ID); probeErr != nil {
		log.Printf("新增视频技术信息读取失败 id=%d err=%v", video.ID, probeErr)
	} else {
		if syncErr := database.Transaction(func(tx *gorm.DB) error { return syncShortVideoTagForVideo(tx, video.ID) }); syncErr != nil {
			log.Printf("新增视频技术信息读取成功但短视频标签同步失败 id=%d err=%v", video.ID, syncErr)
		}
		if refreshed, refreshErr := s.GetVideo(video.ID); refreshErr == nil {
			video = refreshed
		}
	}
	s.observeLocalMetadata(video.ID, true)
	if refreshed, refreshErr := s.GetVideo(video.ID); refreshErr == nil {
		video = refreshed
	}
	log.Printf("新增视频 path=%s", path)
	return video, nil
}

// GetVideo 获取单个视频详情
func (s *VideoService) GetVideo(id uint) (*models.Video, error) {
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return nil, err
	}
	return &video, nil
}

// DeleteVideo 删除视频
func (s *VideoService) DeleteVideo(id uint, deleteFile bool) error {
	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()
	return s.deleteVideo(id, deleteFile)
}

// ListTrashEntries 按最新删除优先返回可恢复条目。
func (s *VideoService) ListTrashEntries() ([]models.VideoTrashEntry, error) {
	var entries []models.VideoTrashEntry
	if err := database.DB.
		Order("created_at DESC, id DESC").
		Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("列出回收站条目失败: %w", err)
	}
	return entries, nil
}

// RestoreTrashEntry 将一个软删除视频恢复到原路径。
func (s *VideoService) RestoreTrashEntry(entryID uint) (*models.Video, error) {
	libraryPathMutationMu.Lock()
	defer libraryPathMutationMu.Unlock()

	var entry models.VideoTrashEntry
	if err := database.DB.First(&entry, entryID).Error; err != nil {
		return nil, fmt.Errorf("读取回收站条目失败: %w", err)
	}
	if entry.State == trashStatePendingMove || entry.State == trashStateRollback {
		return s.cancelInterruptedDeletion(&entry)
	}
	return s.restoreTrashEntry(&entry)
}

func (s *VideoService) restoreTrashEntry(entry *models.VideoTrashEntry) (*models.Video, error) {
	var video models.Video
	if err := database.DB.Unscoped().First(&video, entry.VideoID).Error; err != nil {
		return nil, fmt.Errorf("读取已删除视频失败: %w", err)
	}
	if !video.DeletedAt.IsValid() {
		return nil, fmt.Errorf("视频记录当前不是已删除状态: %d", video.ID)
	}

	if entry.State == trashStateDeleted {
		result := database.DB.Model(entry).
			Where("state = ?", trashStateDeleted).
			Updates(map[string]interface{}{"state": trashStateRestoring, "last_error": ""})
		if result.Error != nil {
			return nil, fmt.Errorf("标记恢复状态失败: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return nil, fmt.Errorf("回收站条目状态已变化: %d", entry.ID)
		}
		entry.State = trashStateRestoring
	} else if entry.State != trashStateRestoring {
		return nil, fmt.Errorf("回收站条目当前不可恢复: %s", entry.State)
	}

	fileAtOriginal := false
	trashService := NewTrashService()
	if entry.FileMoved {
		var err error
		fileAtOriginal, err = ensureTrashEntryFileRestored(trashService, *entry)
		if err != nil {
			_ = markTrashEntryRecoverable(entry.ID, err)
			return nil, err
		}
	} else if info, err := os.Stat(entry.OriginalPath); err != nil {
		restoreErr := fmt.Errorf("原文件不可用，无法恢复记录: %w", err)
		_ = markTrashEntryRecoverable(entry.ID, restoreErr)
		return nil, restoreErr
	} else if info.IsDir() {
		restoreErr := fmt.Errorf("原路径不是视频文件: %s", entry.OriginalPath)
		_ = markTrashEntryRecoverable(entry.ID, restoreErr)
		return nil, restoreErr
	} else if !trashEntryFileMatches(entry.OriginalPath, info, *entry) {
		restoreErr := fmt.Errorf("原路径文件与删除记录不一致: %s", entry.OriginalPath)
		_ = markTrashEntryRecoverable(entry.ID, restoreErr)
		return nil, restoreErr
	}

	var restored models.Video
	err := database.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.Video{}).
			Unscoped().
			Where("id = ? AND deleted_at IS NOT NULL", video.ID).
			Update("deleted_at", nil)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("视频记录已不再处于可恢复状态: %d", video.ID)
		}
		if err := tx.Preload("Tags").First(&restored, video.ID).Error; err != nil {
			return err
		}
		if err := rebuildSubtitleIndexTx(tx, restored); err != nil {
			return fmt.Errorf("重建字幕索引失败: %w", err)
		}
		return tx.Delete(entry).Error
	})
	if err != nil {
		committed, rolledBack, confirmErr := confirmRestoreTransactionOutcome(video.ID, entry.ID)
		if confirmErr != nil {
			_ = recordTrashEntryError(entry.ID, fmt.Errorf("恢复提交结果无法确认: %w", err))
			return nil, fmt.Errorf("恢复提交结果无法确认，已保留当前文件和恢复日志供启动对账: %w", err)
		}
		if committed {
			if loadErr := database.DB.Preload("Tags").First(&restored, video.ID).Error; loadErr != nil {
				return nil, fmt.Errorf("恢复已提交，但读取结果失败: %w", loadErr)
			}
			return &restored, nil
		}
		if !rolledBack {
			_ = recordTrashEntryError(entry.ID, fmt.Errorf("恢复状态不一致: %w", err))
			return nil, fmt.Errorf("恢复状态不一致，未执行文件补偿: %w", err)
		}
		if fileAtOriginal {
			if rollbackErr := trashService.RestoreFromTrash(entry.OriginalPath, entry.TrashPath); rollbackErr != nil {
				_ = recordTrashEntryError(entry.ID, rollbackErr)
				return nil, fmt.Errorf("恢复数据库记录失败: %w；文件回滚失败: %v", err, rollbackErr)
			}
		}
		_ = database.DB.Model(entry).Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": err.Error()}).Error
		return nil, fmt.Errorf("恢复数据库记录失败: %w", err)
	}
	return &restored, nil
}

// deleteVideo 删一条视频记录，成功后连带删掉它的播放代理（D-002）。
//
// 级联放在这一层而不是 DeleteVideo：扫描对账里消失的文件也走 deleteVideo，
// 那些视频的代理同样该跟着走。软删除与永久删除在这里是同一条路——回收站里的
// 视频不需要代理，恢复之后要用再重新触发一次。
func (s *VideoService) deleteVideo(id uint, deleteFile bool) error {
	if err := s.deleteVideoRecord(id, deleteFile); err != nil {
		return err
	}
	s.playbackProxies().DeleteForVideo(id)
	return nil
}

func (s *VideoService) deleteVideoRecord(id uint, deleteFile bool) error {
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return err
	}
	var existingEntry models.VideoTrashEntry
	existingResult := database.DB.Where("video_id = ?", video.ID).Limit(1).Find(&existingEntry)
	if existingResult.Error != nil {
		return fmt.Errorf("检查既有回收站条目失败: %w", existingResult.Error)
	}
	if existingResult.RowsAffected == 1 {
		switch existingEntry.State {
		case trashStatePendingMove:
			if reconcileErr := s.reconcilePendingDelete(&existingEntry); reconcileErr != nil {
				_ = recordTrashEntryError(existingEntry.ID, reconcileErr)
				return fmt.Errorf("继续上次删除失败: %w", reconcileErr)
			}
			return nil
		case trashStateRollback:
			if reconcileErr := reconcileTrashRollback(&existingEntry); reconcileErr != nil {
				_ = recordTrashEntryError(existingEntry.ID, reconcileErr)
				return fmt.Errorf("完成上次删除回滚失败: %w", reconcileErr)
			}
		default:
			return fmt.Errorf("视频已有回收站条目，不能重复删除: %d", existingEntry.ID)
		}
	}

	entry := models.VideoTrashEntry{
		VideoID:      video.ID,
		VideoName:    video.Name,
		OriginalPath: video.Path,
		State:        trashStateDeleted,
	}
	var sourceInfo os.FileInfo
	var err error
	sourceInfo, err = os.Stat(video.Path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("检查待删除文件失败: %w", err)
	}
	if err == nil {
		if sourceInfo.IsDir() {
			return fmt.Errorf("视频路径不是文件: %s", video.Path)
		}
		entry.FileSize = sourceInfo.Size()
		entry.FileModTime = sourceInfo.ModTime().UnixNano()
		entry.FileIdentity = stableFileIdentity(sourceInfo)
		entry.FileSHA256, err = fileSHA256Hex(video.Path)
		if err != nil {
			return fmt.Errorf("计算待删除文件摘要失败: %w", err)
		}
	}

	shouldMoveFile := deleteFile && sourceInfo != nil && !isTrashPath(video.Path)
	if !shouldMoveFile {
		return database.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
			return finalizeVideoDeletionTx(tx, &video)
		})
	}

	trashService := NewTrashService()
	entry.State = trashStatePendingMove
	if err := createPendingTrashEntry(&entry, trashService); err != nil {
		return fmt.Errorf("记录待删除文件失败: %w", err)
	}
	if err := movePendingTrashEntryFile(&entry, trashService); err != nil {
		_ = recordTrashEntryError(entry.ID, err)
		return fmt.Errorf("移动文件到回收站失败: %w", err)
	}
	trashInfo, err := os.Stat(entry.TrashPath)
	if err != nil {
		return fmt.Errorf("读取回收站文件信息失败: %w", err)
	}
	if !trashEntryFileMatches(entry.TrashPath, trashInfo, entry) {
		return fmt.Errorf("回收站文件与删除前内容不一致: %s", entry.TrashPath)
	}
	log.Printf("视频已移入回收站 src=%s dst=%s", video.Path, entry.TrashPath)

	err = database.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.VideoTrashEntry{}).
			Where("id = ? AND state = ?", entry.ID, trashStatePendingMove).
			Updates(map[string]interface{}{
				"state":       trashStateDeleted,
				"file_moved":  true,
				"file_sha256": entry.FileSHA256,
				"last_error":  "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("待删除条目状态已变化: %d", entry.ID)
		}
		return finalizeVideoDeletionTx(tx, &video)
	})
	if err == nil {
		return nil
	}
	committed, rolledBack, confirmErr := confirmDeleteTransactionOutcome(video.ID, entry.ID)
	if confirmErr != nil {
		_ = recordTrashEntryError(entry.ID, fmt.Errorf("删除提交结果无法确认: %w", err))
		return fmt.Errorf("删除提交结果无法确认，文件和操作日志已保留供启动对账: %w", err)
	}
	if committed {
		return nil
	}
	if !rolledBack {
		_ = recordTrashEntryError(entry.ID, fmt.Errorf("删除状态不一致: %w", err))
		return fmt.Errorf("删除状态不一致，未执行文件补偿: %w", err)
	}

	_ = database.DB.Model(&entry).Update("state", trashStateRollback).Error
	if rollbackErr := trashService.RestoreFromTrash(entry.TrashPath, video.Path); rollbackErr != nil {
		_ = recordTrashEntryError(entry.ID, rollbackErr)
		return fmt.Errorf("删除数据库记录失败: %w；文件回滚失败: %v", err, rollbackErr)
	}
	if cleanupErr := database.DB.Delete(&entry).Error; cleanupErr != nil {
		_ = recordTrashEntryError(entry.ID, cleanupErr)
		return fmt.Errorf("删除数据库记录失败: %w；清理待删除条目失败: %v", err, cleanupErr)
	}
	return fmt.Errorf("删除数据库记录失败: %w", err)
}

func finalizeVideoDeletionTx(tx *gorm.DB, video *models.Video) error {
	if err := tx.Where("video_id = ?", video.ID).Delete(&models.SubtitleSegment{}).Error; err != nil {
		return err
	}
	if err := tx.Where("video_id = ?", video.ID).Delete(&models.SubtitleIndexState{}).Error; err != nil {
		return err
	}
	return tx.Delete(video).Error
}

func createPendingTrashEntry(entry *models.VideoTrashEntry, trashService *TrashService) error {
	for attempt := 0; attempt < 10000; attempt++ {
		entry.TrashPath = trashService.TrashTargetPath(entry.OriginalPath, attempt)
		if err := database.DB.Create(entry).Error; err == nil {
			return nil
		} else if !trashPathAlreadyRecorded(entry.TrashPath) {
			return err
		}
		entry.ID = 0
		entry.CreatedAt = time.Time{}
		entry.UpdatedAt = time.Time{}
	}
	return fmt.Errorf("无法记录唯一回收站路径: %s", entry.OriginalPath)
}

func movePendingTrashEntryFile(entry *models.VideoTrashEntry, trashService *TrashService) error {
	for attempt := 0; attempt < 10000; attempt++ {
		info, err := os.Stat(entry.OriginalPath)
		if err != nil {
			return err
		}
		if !trashEntryFileMatches(entry.OriginalPath, info, *entry) {
			return fmt.Errorf("原文件与待删除记录的强身份不一致: %s", entry.OriginalPath)
		}
		if err := trashService.MoveToTrashAt(entry.OriginalPath, entry.TrashPath); err == nil {
			return nil
		} else if !errors.Is(err, ErrTrashTargetExists) {
			return err
		}

		updatedPath := false
		for nextAttempt := attempt + 1; nextAttempt < 10000; nextAttempt++ {
			nextPath := trashService.TrashTargetPath(entry.OriginalPath, nextAttempt)
			result := database.DB.Model(entry).
				Where("state = ?", trashStatePendingMove).
				Update("trash_path", nextPath)
			if result.Error == nil && result.RowsAffected == 1 {
				entry.TrashPath = nextPath
				attempt = nextAttempt - 1
				updatedPath = true
				break
			}
			if result.Error == nil {
				return fmt.Errorf("待删除条目状态已变化: %d", entry.ID)
			}
			if result.Error != nil && !trashPathAlreadyRecorded(nextPath) {
				return result.Error
			}
		}
		if !updatedPath {
			return fmt.Errorf("无法记录新的回收站路径: %s", entry.OriginalPath)
		}
	}
	return fmt.Errorf("无法生成未占用的回收站路径: %s", entry.OriginalPath)
}

func trashPathAlreadyRecorded(path string) bool {
	var count int64
	return database.DB.Model(&models.VideoTrashEntry{}).Where("trash_path = ?", path).Count(&count).Error == nil && count > 0
}

func ensureTrashEntryFileRestored(trashService *TrashService, entry models.VideoTrashEntry) (bool, error) {
	originalInfo, originalExists, err := regularFileState(entry.OriginalPath)
	if err != nil {
		return false, err
	}
	trashInfo, trashExists, err := regularFileState(entry.TrashPath)
	if err != nil {
		return false, err
	}
	if originalExists && trashExists {
		if os.SameFile(originalInfo, trashInfo) {
			if err := os.Remove(entry.TrashPath); err != nil {
				return false, fmt.Errorf("清理已恢复的回收站副本失败: %w", err)
			}
			return true, nil
		}
		return false, fmt.Errorf("原路径已被占用，拒绝覆盖: %s", entry.OriginalPath)
	}
	if originalExists {
		if !trashEntryFileMatches(entry.OriginalPath, originalInfo, entry) {
			return false, fmt.Errorf("原路径文件与删除记录不一致: %s", entry.OriginalPath)
		}
		return true, nil
	}
	if !trashExists {
		return false, fmt.Errorf("回收站文件不存在: %s", entry.TrashPath)
	}
	if !trashEntryFileMatches(entry.TrashPath, trashInfo, entry) {
		return false, fmt.Errorf("回收站文件与删除记录不一致: %s", entry.TrashPath)
	}
	if err := trashService.RestoreFromTrash(entry.TrashPath, entry.OriginalPath); err != nil {
		return false, err
	}
	return true, nil
}

func regularFileState(path string) (os.FileInfo, bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.IsDir() {
		return nil, false, fmt.Errorf("路径不是文件: %s", path)
	}
	return info, true, nil
}

func trashEntryFileMatches(path string, info os.FileInfo, entry models.VideoTrashEntry) bool {
	if info == nil {
		return false
	}
	if entry.FileSize != 0 && info.Size() != entry.FileSize {
		return false
	}
	if entry.FileIdentity != "" && stableFileIdentity(info) == entry.FileIdentity {
		return true
	}
	if entry.FileSHA256 == "" {
		return false
	}
	digest, err := fileSHA256Hex(path)
	return err == nil && digest == entry.FileSHA256
}

func fileSHA256Hex(path string) (string, error) {
	digest, err := fileSHA256(path)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest[:]), nil
}

func confirmDeleteTransactionOutcome(videoID uint, entryID uint) (bool, bool, error) {
	var video models.Video
	if err := database.DB.Unscoped().First(&video, videoID).Error; err != nil {
		return false, false, err
	}
	var entry models.VideoTrashEntry
	if err := database.DB.First(&entry, entryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, false, nil
		}
		return false, false, err
	}
	if video.DeletedAt.IsValid() && entry.State == trashStateDeleted {
		return true, false, nil
	}
	if !video.DeletedAt.IsValid() && entry.State == trashStatePendingMove {
		return false, true, nil
	}
	return false, false, nil
}

func confirmRestoreTransactionOutcome(videoID uint, entryID uint) (bool, bool, error) {
	var video models.Video
	if err := database.DB.Unscoped().First(&video, videoID).Error; err != nil {
		return false, false, err
	}
	var entry models.VideoTrashEntry
	err := database.DB.First(&entry, entryID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if !video.DeletedAt.IsValid() {
			return true, false, nil
		}
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if video.DeletedAt.IsValid() && entry.State == trashStateRestoring {
		return false, true, nil
	}
	return false, false, nil
}

func recordTrashEntryError(entryID uint, cause error) error {
	if cause == nil {
		return nil
	}
	return database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", entryID).Update("last_error", cause.Error()).Error
}

func markTrashEntryRecoverable(entryID uint, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return database.DB.Model(&models.VideoTrashEntry{}).
		Where("id = ?", entryID).
		Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": message}).Error
}

func (s *VideoService) cancelInterruptedDeletion(entry *models.VideoTrashEntry) (*models.Video, error) {
	var video models.Video
	if err := database.DB.Preload("Tags").First(&video, entry.VideoID).Error; err != nil {
		return nil, fmt.Errorf("读取活动视频失败: %w", err)
	}
	originalInfo, originalExists, err := regularFileState(entry.OriginalPath)
	if err != nil {
		_ = recordTrashEntryError(entry.ID, err)
		return nil, err
	}
	trashInfo, trashExists, err := regularFileState(entry.TrashPath)
	if err != nil {
		_ = recordTrashEntryError(entry.ID, err)
		return nil, err
	}
	if originalExists {
		if !trashEntryFileMatches(entry.OriginalPath, originalInfo, *entry) {
			err := fmt.Errorf("原路径已被其他文件占用: %s", entry.OriginalPath)
			_ = recordTrashEntryError(entry.ID, err)
			return nil, err
		}
		if trashExists && os.SameFile(originalInfo, trashInfo) {
			if err := os.Remove(entry.TrashPath); err != nil {
				_ = recordTrashEntryError(entry.ID, err)
				return nil, err
			}
		}
	} else {
		if !trashExists {
			err := fmt.Errorf("原路径与回收站路径均不存在文件")
			_ = recordTrashEntryError(entry.ID, err)
			return nil, err
		}
		if !trashEntryFileMatches(entry.TrashPath, trashInfo, *entry) {
			err := fmt.Errorf("回收站文件与删除记录不一致: %s", entry.TrashPath)
			_ = recordTrashEntryError(entry.ID, err)
			return nil, err
		}
		if err := NewTrashService().RestoreFromTrash(entry.TrashPath, entry.OriginalPath); err != nil {
			_ = recordTrashEntryError(entry.ID, err)
			return nil, err
		}
	}
	if err := database.DB.Delete(entry).Error; err != nil {
		return nil, fmt.Errorf("清理中断删除日志失败: %w", err)
	}
	return &video, nil
}

// ReconcileTrashEntries 恢复上次进程中断时尚未完成的文件与数据库操作。
func (s *VideoService) ReconcileTrashEntries() error {
	libraryPathMutationMu.Lock()
	defer libraryPathMutationMu.Unlock()

	var entries []models.VideoTrashEntry
	if err := database.DB.Where("state IN ?", []string{trashStatePendingMove, trashStateRestoring, trashStateRollback}).Find(&entries).Error; err != nil {
		return err
	}
	var reconcileErrors []error
	for idx := range entries {
		entry := &entries[idx]
		var err error
		switch entry.State {
		case trashStatePendingMove:
			err = s.reconcilePendingDelete(entry)
		case trashStateRestoring:
			_, err = s.restoreTrashEntry(entry)
		case trashStateRollback:
			err = reconcileTrashRollback(entry)
		}
		if err != nil {
			_ = recordTrashEntryError(entry.ID, err)
			reconcileErrors = append(reconcileErrors, fmt.Errorf("回收站条目 %d 对账失败: %w", entry.ID, err))
		}
	}
	return errors.Join(reconcileErrors...)
}

func (s *VideoService) reconcilePendingDelete(entry *models.VideoTrashEntry) error {
	var video models.Video
	if err := database.DB.Unscoped().First(&video, entry.VideoID).Error; err != nil {
		return err
	}
	originalInfo, originalExists, err := regularFileState(entry.OriginalPath)
	if err != nil {
		return err
	}
	trashInfo, trashExists, err := regularFileState(entry.TrashPath)
	if err != nil {
		return err
	}
	fileReady := false
	if originalExists && trashExists {
		if os.SameFile(originalInfo, trashInfo) {
			if err := os.Remove(entry.OriginalPath); err != nil {
				return fmt.Errorf("清理已移入回收站的原路径副本失败: %w", err)
			}
			originalExists = false
		} else if trashEntryFileMatches(entry.OriginalPath, originalInfo, *entry) {
			if err := movePendingTrashEntryFile(entry, NewTrashService()); err != nil {
				return err
			}
			fileReady = true
		} else {
			return fmt.Errorf("原文件与待删除记录不一致")
		}
	}
	if fileReady {
		// 文件移动函数已使用排他目标完成移动。
	} else if originalExists {
		if !trashEntryFileMatches(entry.OriginalPath, originalInfo, *entry) {
			return fmt.Errorf("原文件与待删除记录不一致")
		}
		if err := NewTrashService().MoveToTrashAt(entry.OriginalPath, entry.TrashPath); err != nil {
			return err
		}
	} else if !trashExists {
		// 两边都没有文件。删除本来就是用户发起的，只要文件所在的扫描根现在可访问，
		// 就说明文件确实已经不在了，直接把库记录落成删除；卷没挂载或读不到时不动数据。
		reachable, root, err := scanRootReachableForPath(entry.OriginalPath)
		if err != nil {
			return err
		}
		if !reachable {
			return fmt.Errorf("原路径与回收站路径均不存在文件，且扫描根 %q 当前不可访问，库记录保持原样", root)
		}
		log.Printf("原文件已不在磁盘上，直接清理库记录 entry=%d path=%s", entry.ID, entry.OriginalPath)
	} else if !trashEntryFileMatches(entry.TrashPath, trashInfo, *entry) {
		return fmt.Errorf("回收站文件与待删除记录不一致")
	}

	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(entry).Updates(map[string]interface{}{"state": trashStateDeleted, "file_moved": true, "last_error": ""}).Error; err != nil {
			return err
		}
		return finalizeVideoDeletionTx(tx, &video)
	})
}

func reconcileTrashRollback(entry *models.VideoTrashEntry) error {
	trashService := NewTrashService()
	originalInfo, originalExists, err := regularFileState(entry.OriginalPath)
	if err != nil {
		return err
	}
	trashInfo, trashExists, err := regularFileState(entry.TrashPath)
	if err != nil {
		return err
	}
	if originalExists && trashExists {
		if os.SameFile(originalInfo, trashInfo) {
			if err := os.Remove(entry.TrashPath); err != nil {
				return fmt.Errorf("清理回滚后的回收站副本失败: %w", err)
			}
			trashExists = false
		} else {
			return fmt.Errorf("回滚时原路径已被其他文件占用")
		}
	}
	if !originalExists {
		if !trashExists {
			return fmt.Errorf("回滚时原路径与回收站路径均不存在文件")
		}
		if err := trashService.RestoreFromTrash(entry.TrashPath, entry.OriginalPath); err != nil {
			return err
		}
	} else if !trashEntryFileMatches(entry.OriginalPath, originalInfo, *entry) {
		return fmt.Errorf("回滚后的原文件与删除记录不一致")
	}
	return database.DB.Delete(entry).Error
}

func newBatchVideoOperationResult(ids []uint) *BatchVideoOperationResult {
	return &BatchVideoOperationResult{
		Requested: len(ids),
		Errors:    make([]BatchVideoOperationError, 0),
		Warnings:  make([]BatchVideoOperationWarning, 0),
	}
}

func (r *BatchVideoOperationResult) record(videoID uint, err error) {
	if err == nil {
		r.Succeeded++
		return
	}
	r.Failed++
	r.Errors = append(r.Errors, BatchVideoOperationError{
		VideoID: videoID,
		Error:   err.Error(),
	})
}

func (s *VideoService) BatchDeleteVideos(videoIDs []uint, deleteFile bool) *BatchVideoOperationResult {
	result := newBatchVideoOperationResult(videoIDs)
	for _, videoID := range videoIDs {
		result.record(videoID, s.DeleteVideo(videoID, deleteFile))
	}
	return result
}

func (s *VideoService) BatchAddTagToVideos(videoIDs []uint, tagID uint) *BatchVideoOperationResult {
	result := newBatchVideoOperationResult(videoIDs)
	for _, videoID := range videoIDs {
		result.record(videoID, s.AddTagToVideo(videoID, tagID))
	}
	return result
}

func (s *VideoService) BatchRemoveTagFromVideos(videoIDs []uint, tagID uint) *BatchVideoOperationResult {
	result := newBatchVideoOperationResult(videoIDs)
	for _, videoID := range videoIDs {
		result.record(videoID, s.RemoveTagFromVideo(videoID, tagID))
	}
	return result
}

func (s *VideoService) BatchRefreshVideoMetadata(videoIDs []uint) *BatchVideoOperationResult {
	result := newBatchVideoOperationResult(videoIDs)
	for _, videoID := range videoIDs {
		result.record(videoID, s.RefreshVideoMetadata(videoID))
	}
	return result
}

// AddTagToVideo 为视频添加标签
func (s *VideoService) AddTagToVideo(videoID uint, tagID uint) error {
	var video models.Video
	var tag models.Tag

	if err := database.DB.First(&video, videoID).Error; err != nil {
		return err
	}
	if err := database.DB.First(&tag, tagID).Error; err != nil {
		return err
	}
	if tag.AutomaticKind != "" {
		return fmt.Errorf("自动标签由应用维护，不能手动添加")
	}

	return database.DB.Model(&video).Association("Tags").Append(&tag)
}

// RemoveTagFromVideo 移除视频的标签
func (s *VideoService) RemoveTagFromVideo(videoID uint, tagID uint) error {
	var video models.Video
	var tag models.Tag

	if err := database.DB.First(&video, videoID).Error; err != nil {
		return err
	}
	if err := database.DB.First(&tag, tagID).Error; err != nil {
		return err
	}
	if tag.AutomaticKind != "" {
		return fmt.Errorf("自动标签由应用维护，不能手动移除")
	}

	return database.DB.Model(&video).Association("Tags").Delete(&tag)
}

// RefreshVideoMetadata 刷新并修复视频的元数据
func (s *VideoService) RefreshVideoMetadata(id uint) error {
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return err
	}

	if err := s.technicalProbe().Refresh(context.Background(), video.ID); err != nil {
		return err
	}
	return database.Transaction(func(tx *gorm.DB) error { return syncShortVideoTagForVideo(tx, video.ID) })
}
