package services

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
)

type VideoService struct {
	scanSyncMu sync.Mutex
}

var libraryPathMutationMu sync.RWMutex

const (
	recentActiveFileThreshold = 5 * time.Minute
	trashStatePendingMove     = "pending_move"
	trashStateDeleted         = "deleted"
	trashStateRestoring       = "restoring"
	trashStateRollback        = "rollback"
)

var tempVideoStemSuffixes = []string{
	".temp", "_temp", "-temp",
	".tmp", "_tmp", "-tmp",
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

type ScanSyncError struct {
	Operation string `json:"operation"`
	Directory string `json:"directory,omitempty"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error"`
}

type ScanSyncResult struct {
	Directories       int             `json:"directories"`
	Scanned           int             `json:"scanned"`
	Added             int             `json:"added"`
	Deleted           int             `json:"deleted"`
	Relocated         int             `json:"relocated"`
	MetadataRefreshed int             `json:"metadata_refreshed"`
	Skipped           int             `json:"skipped"`
	Errors            []ScanSyncError `json:"errors"`
}

const (
	RandomPlayModeBalanced  = "balanced"
	RandomPlayModeUnwatched = "unwatched"
	RandomPlayModeFavorites = "favorites"
)

// RandomPlayRequest 描述筛选内随机播放的边界和模式。
type RandomPlayRequest struct {
	Filter     LibraryFilter `json:"filter"`
	Mode       string        `json:"mode"`
	ExcludeIDs []uint        `json:"exclude_ids"`
}

type videoScoreRow struct {
	ID              uint
	PlayCount       int
	RandomPlayCount int
}

func (r *ScanSyncResult) recordError(operation, directory, path string, err error) {
	r.Skipped++
	r.Errors = append(r.Errors, ScanSyncError{
		Operation: operation,
		Directory: directory,
		Path:      path,
		Error:     err.Error(),
	})
}

// GetAllVideos 获取所有视频（已废弃，使用分页方式）
func (s *VideoService) GetAllVideos() ([]models.Video, error) {
	var videos []models.Video
	err := database.DB.Preload("Tags").Order("created_at desc").Limit(50).Find(&videos).Error
	return videos, err
}

// getPlayWeight 获取播放权重配置
func (s *VideoService) getPlayWeight() (float64, error) {
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return 0, fmt.Errorf("获取设置失败: %w", err)
	}
	w := settings.PlayWeight
	if w < 0.1 {
		w = 0.1
	}
	return w, nil
}

// scoreExprForTable 返回播放分数的 SQL 表达式片段，使用 fmt.Sprintf 将权重直接嵌入 SQL，
// 避免在复合 WHERE 条件中反复传递 ? 占位符导致参数计数出错。
func scoreExprForTable(tablePrefix string, playWeight float64) string {
	return fmt.Sprintf("(%splay_count * %g + %srandom_play_count)", tablePrefix, playWeight, tablePrefix)
}

// applyCursorCondition 为查询添加游标分页的 WHERE 条件
// 排序规则：score ASC, size DESC, id DESC
func applyCursorCondition(query *gorm.DB, scoreSql string, cursorScore float64, cursorSize int64, cursorID uint, tablePrefix string) *gorm.DB {
	if cursorID == 0 {
		return query
	}
	sizeCol := tablePrefix + "size"
	idCol := tablePrefix + "id"
	// 三元组游标条件：(score > ?) OR (score = ? AND size < ?) OR (score = ? AND size = ? AND id < ?)
	cond := fmt.Sprintf(
		"(%s > ?) OR (%s = ? AND %s < ?) OR (%s = ? AND %s = ? AND %s < ?)",
		scoreSql, scoreSql, sizeCol, scoreSql, sizeCol, idCol,
	)
	return query.Where(cond, cursorScore, cursorScore, cursorSize, cursorScore, cursorSize, cursorID)
}

// GetVideosPaginated 游标分页获取视频（按概率优先排序）
func (s *VideoService) GetVideosPaginated(cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	playWeight, err := s.getPlayWeight()
	if err != nil {
		return nil, err
	}

	var videos []models.Video
	scoreSql := scoreExprForTable("videos.", playWeight)
	query := database.DB.Model(&models.Video{}).Preload("Tags").
		Order(scoreSql + " ASC").
		Order("videos.size desc").
		Order("videos.id desc")

	query = applyCursorCondition(query, scoreSql, cursorScore, cursorSize, cursorID, "videos.")

	err = query.Limit(limit).Find(&videos).Error
	return videos, err
}

// SearchVideos 搜索视频（按名称）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideos(keyword string, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	return s.SearchVideosWithFilters(keyword, nil, 0, 0, 0, 0, cursorScore, cursorSize, cursorID, limit)
}

// SearchVideosByTags 按标签搜索（多选 AND）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideosByTags(tagIDs []uint, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	return s.SearchVideosWithFilters("", tagIDs, 0, 0, 0, 0, cursorScore, cursorSize, cursorID, limit)
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

// SearchVideosWithFilters 组合搜索（关键词 + 标签 + 体积 + 分辨率 AND）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideosWithFilters(keyword string, tagIDs []uint, minSize, maxSize int64, minHeight, maxHeight int, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	var videos []models.Video
	playWeight, err := s.getPlayWeight()
	if err != nil {
		return nil, err
	}

	scoreSql := scoreExprForTable("videos.", playWeight)
	query := database.DB.Model(&models.Video{}).Preload("Tags").
		Order(scoreSql + " ASC").
		Order("videos.size desc").
		Order("videos.id desc")

	if strings.TrimSpace(keyword) != "" {
		kw := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("(videos.name LIKE ? OR videos.path LIKE ?)", kw, kw)
	}

	if minSize > 0 {
		query = query.Where("videos.size >= ?", minSize)
	}
	if maxSize > 0 {
		query = query.Where("videos.size < ?", maxSize)
	}
	if minHeight > 0 {
		query = query.Where("videos.height >= ?", minHeight)
	}
	if maxHeight > 0 {
		query = query.Where("videos.height <= ?", maxHeight)
	}

	if len(tagIDs) > 0 {
		query = query.Joins("JOIN video_tags ON video_tags.video_id = videos.id").
			Where("video_tags.tag_id IN ?", tagIDs)
		query = query.Group("videos.id").
			Having("COUNT(DISTINCT video_tags.tag_id) = ?", len(tagIDs))
	}

	query = applyCursorCondition(query, scoreSql, cursorScore, cursorSize, cursorID, "videos.")

	err = query.Limit(limit).Find(&videos).Error
	return videos, err
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

	duration, resolution, width, height := s.getVideoMetadata(path)

	video := &models.Video{
		Name:       filepath.Base(path),
		Path:       path,
		Directory:  filepath.Dir(path),
		Size:       info.Size(),
		Duration:   duration,
		Resolution: resolution,
		Width:      width,
		Height:     height,
	}

	err = database.DB.Transaction(func(tx *gorm.DB) error {
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
	err := database.DB.Transaction(func(tx *gorm.DB) error {
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

func (s *VideoService) deleteVideo(id uint, deleteFile bool) error {
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
		return database.DB.Transaction(func(tx *gorm.DB) error {
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

	err = database.DB.Transaction(func(tx *gorm.DB) error {
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
		return fmt.Errorf("原路径与回收站路径均不存在文件")
	} else if !trashEntryFileMatches(entry.TrashPath, trashInfo, *entry) {
		return fmt.Errorf("回收站文件与待删除记录不一致")
	}

	return database.DB.Transaction(func(tx *gorm.DB) error {
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

// ScanDirectory 扫描目录获取视频文件
func (s *VideoService) ScanDirectory(dir string) ([]string, error) {
	files, err := s.ScanDirectoryWithInfo(dir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return paths, nil
}

// ScannedFile 扫描结果（附带文件大小，用于迁移检测）
type ScannedFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ScanDirectoryWithInfo 扫描目录获取视频文件（附带文件大小）
func (s *VideoService) ScanDirectoryWithInfo(dir string) ([]ScannedFile, error) {
	var videoFiles []ScannedFile
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return nil, fmt.Errorf("扫描根目录为空")
	}
	rootInfo, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("扫描根目录不可用: %w", err)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("扫描根路径不是目录: %s", dir)
	}

	// 从设置中获取支持的视频格式
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}

	// 解析视频格式
	videoExts := strings.Split(settings.VideoExtensions, ",")
	if len(videoExts) == 1 && strings.TrimSpace(videoExts[0]) == "" {
		videoExts = strings.Split(".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt", ",")
	}
	for i := range videoExts {
		videoExts[i] = strings.TrimSpace(videoExts[i])
		if videoExts[i] == "" {
			continue
		}
		if !strings.HasPrefix(videoExts[i], ".") {
			videoExts[i] = "." + videoExts[i]
		}
	}

	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过错误的文件
		}

		if shouldSkipHiddenPath(info) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			if isTrashDirName(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		if isTrashPath(path) || hasTempVideoSuffix(path) || isRecentlyActiveFile(info) || isKnownNonVideoSourcePath(path) {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		for _, videoExt := range videoExts {
			if ext == strings.ToLower(videoExt) {
				videoFiles = append(videoFiles, ScannedFile{Path: path, Size: info.Size()})
				break
			}
		}

		return nil
	})
	log.Printf("扫描目录完成 dir=%s files=%d", dir, len(videoFiles))

	return videoFiles, err
}

type scanFileFingerprint struct {
	Name string
	Size int64
}

func fingerprintScannedFile(file ScannedFile) scanFileFingerprint {
	return scanFileFingerprint{Name: filepath.Base(file.Path), Size: file.Size}
}

func fingerprintVideo(video models.Video) scanFileFingerprint {
	return scanFileFingerprint{Name: video.Name, Size: video.Size}
}

// SyncScanDirectories performs an incremental database sync for configured scan directories.
func (s *VideoService) SyncScanDirectories(dirs []models.ScanDirectory) *ScanSyncResult {
	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()
	s.scanSyncMu.Lock()
	defer s.scanSyncMu.Unlock()

	result := &ScanSyncResult{Errors: make([]ScanSyncError, 0)}
	scannedByPath := make(map[string]ScannedFile)
	existingByPath := make(map[string]models.Video)
	roots := make([]string, 0, len(dirs))
	allExisting := make([]models.Video, 0)
	duplicateVideos := make([]models.Video, 0)

	for _, dir := range dirs {
		root := filepath.Clean(strings.TrimSpace(dir.Path))
		if root == "" || root == "." {
			result.recordError("scan", dir.Path, "", fmt.Errorf("扫描目录为空"))
			continue
		}
		result.Directories++

		scannedFiles, err := s.ScanDirectoryWithInfo(root)
		if err != nil {
			result.recordError("scan", root, "", err)
			continue
		}
		roots = append(roots, root)
		result.Scanned += len(scannedFiles)
		for _, file := range scannedFiles {
			scannedByPath[file.Path] = file
		}
	}

	loadedExisting, err := s.getActiveVideosUnderRoots(roots)
	if err != nil {
		result.recordError("load_existing", "", "", err)
	} else {
		for _, video := range loadedExisting {
			if !videoBelongsToRoots(video, roots) {
				continue
			}
			if kept, exists := existingByPath[video.Path]; exists {
				if video.ID != kept.ID {
					duplicateVideos = append(duplicateVideos, video)
				}
				continue
			}
			existingByPath[video.Path] = video
			allExisting = append(allExisting, video)
		}
	}

	missingVideos := make([]models.Video, 0)
	for _, video := range allExisting {
		if _, exists := scannedByPath[video.Path]; !exists {
			missingVideos = append(missingVideos, video)
			continue
		}
		if video.Duration == 0 || video.Resolution == "" || video.Height == 0 {
			if err := s.RefreshVideoMetadata(video.ID); err != nil {
				result.recordError("refresh_metadata", video.Directory, video.Path, err)
			} else {
				result.MetadataRefreshed++
			}
		}
	}

	newFiles := make([]ScannedFile, 0)
	for _, file := range scannedByPath {
		if _, exists := existingByPath[file.Path]; !exists {
			newFiles = append(newFiles, file)
		}
	}

	sortScannedFiles(newFiles)
	relocatedVideoIDs := make(map[uint]struct{})
	consumedNewPaths := make(map[string]struct{})
	missingByFingerprint := make(map[scanFileFingerprint][]models.Video)
	newFileCounts := make(map[scanFileFingerprint]int)
	for _, video := range missingVideos {
		missingByFingerprint[fingerprintVideo(video)] = append(missingByFingerprint[fingerprintVideo(video)], video)
	}
	for _, file := range newFiles {
		newFileCounts[fingerprintScannedFile(file)]++
	}

	for _, file := range newFiles {
		key := fingerprintScannedFile(file)
		candidates := missingByFingerprint[key]
		if len(candidates) != 1 || newFileCounts[key] != 1 {
			continue
		}
		video := candidates[0]
		if _, used := relocatedVideoIDs[video.ID]; used {
			continue
		}
		if err := s.relocateVideo(video.ID, file.Path); err != nil {
			result.recordError("relocate", video.Directory, file.Path, err)
			continue
		}
		result.Relocated++
		relocatedVideoIDs[video.ID] = struct{}{}
		consumedNewPaths[file.Path] = struct{}{}
	}

	for _, file := range newFiles {
		if _, consumed := consumedNewPaths[file.Path]; consumed {
			continue
		}
		if _, err := s.addVideo(file.Path); err != nil {
			if errors.Is(err, ErrVideoExists) {
				result.Skipped++
				continue
			}
			result.recordError("add", filepath.Dir(file.Path), file.Path, err)
			continue
		}
		result.Added++
	}

	for _, video := range append(duplicateVideos, missingVideos...) {
		if _, relocated := relocatedVideoIDs[video.ID]; relocated {
			continue
		}
		if err := s.deleteVideo(video.ID, false); err != nil {
			result.recordError("delete", video.Directory, video.Path, err)
			continue
		}
		result.Deleted++
	}
	if err := database.DB.Transaction(func(tx *gorm.DB) error { return syncShortVideoTags(tx) }); err != nil {
		result.recordError("short-video-tag", "", "", err)
	}

	log.Printf("增量扫描同步完成 dirs=%d scanned=%d added=%d relocated=%d deleted=%d refreshed=%d skipped=%d errors=%d",
		result.Directories, result.Scanned, result.Added, result.Relocated, result.Deleted, result.MetadataRefreshed, result.Skipped, len(result.Errors))
	return result
}

func (s *VideoService) getActiveVideosUnderRoots(roots []string) ([]models.Video, error) {
	if len(roots) == 0 {
		return []models.Video{}, nil
	}
	var videos []models.Video
	if err := database.DB.Preload("Tags").Find(&videos).Error; err != nil {
		return nil, err
	}
	filtered := videos[:0]
	for _, video := range videos {
		if videoBelongsToRoots(video, roots) {
			filtered = append(filtered, video)
		}
	}
	return filtered, nil
}

func videoBelongsToRoots(video models.Video, roots []string) bool {
	for _, root := range roots {
		prefix := root + string(os.PathSeparator)
		if video.Directory == root || strings.HasPrefix(video.Directory, prefix) || strings.HasPrefix(video.Path, prefix) {
			return true
		}
	}
	return false
}

func sortScannedFiles(files []ScannedFile) {
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
}

func shouldSkipHiddenPath(info os.FileInfo) bool {
	return info.Name() != "." && strings.HasPrefix(info.Name(), ".")
}

func isTrashDirName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), DefaultTrashDirName)
}

func isTrashPath(path string) bool {
	cleanPath := filepath.Clean(path)
	volume := filepath.VolumeName(cleanPath)
	trimmed := strings.TrimPrefix(cleanPath, volume)
	for _, part := range strings.Split(trimmed, string(os.PathSeparator)) {
		if isTrashDirName(part) {
			return true
		}
	}
	return false
}

func hasTempVideoSuffix(path string) bool {
	baseName := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(baseName))
	stem := strings.TrimSuffix(baseName, ext)
	for _, suffix := range tempVideoStemSuffixes {
		if stem == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(stem, suffix) {
			return true
		}
	}
	return false
}

func isKnownNonVideoSourcePath(path string) bool {
	baseName := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(baseName, ".d.ts") || strings.HasSuffix(baseName, ".d.tsx") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(baseName))
	if ext != ".ts" && ext != ".tsx" {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "node_modules" {
			return true
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	sample := strings.ToLower(string(bytes.TrimSpace(data)))
	if sample == "" {
		return false
	}
	sourceMarkers := []string{
		"export ", "import ", "interface ", "type ", "declare ", "namespace ", "const ", "let ", "var ", "function ", "class ",
	}
	for _, marker := range sourceMarkers {
		if strings.Contains(sample, marker) {
			return true
		}
	}
	return false
}

func isRecentlyActiveFile(info os.FileInfo) bool {
	return time.Since(info.ModTime()) < recentActiveFileThreshold
}

// RelocateVideo 更新视频路径（文件迁移场景，保留标签等元数据）
func (s *VideoService) RelocateVideo(id uint, newPath string) error {
	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()
	return s.relocateVideo(id, newPath)
}

func (s *VideoService) relocateVideo(id uint, newPath string) error {
	newPath = filepath.Clean(strings.TrimSpace(newPath))

	// 验证新路径文件存在
	if _, err := os.Stat(newPath); err != nil {
		return fmt.Errorf("目标文件不存在: %w", err)
	}

	// 检查新路径是否已被其他记录占用
	var existing models.Video
	if err := database.DB.Where("path = ? AND id != ?", newPath, id).First(&existing).Error; err == nil {
		return fmt.Errorf("目标路径已被其他记录占用: %s", newPath)
	}

	// 迁移时也尝试重新提取元数据（可能之前的元数据是空的）
	duration, resolution, width, height := s.getVideoMetadata(newPath)

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.Video{}).Where("id = ?", id).Updates(map[string]interface{}{
			"path":       newPath,
			"directory":  filepath.Dir(newPath),
			"name":       filepath.Base(newPath),
			"duration":   duration,
			"resolution": resolution,
			"width":      width,
			"height":     height,
		})
		if result.Error != nil {
			return result.Error
		}
		return syncShortVideoTagForVideo(tx, id)
	}); err != nil {
		return err
	}
	log.Printf("视频迁移并更新元数据 id=%d newPath=%s duration=%.1f res=%s", id, newPath, duration, resolution)
	return nil
}

// RefreshVideoMetadata 刷新并修复视频的元数据
func (s *VideoService) RefreshVideoMetadata(id uint) error {
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return err
	}

	duration, resolution, width, height := s.getVideoMetadata(video.Path)
	if duration == 0 && resolution == "" {
		return fmt.Errorf("未能从文件中提取有效元数据: %s", video.Path)
	}

	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&video).Updates(map[string]interface{}{
			"duration":   duration,
			"resolution": resolution,
			"width":      width,
			"height":     height,
		}).Error; err != nil {
			return err
		}
		return syncShortVideoTagForVideo(tx, video.ID)
	})
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

// GetVideosByDirectory 按目录获取视频记录
func (s *VideoService) GetVideosByDirectory(dir string) ([]models.Video, error) {
	var videos []models.Video
	cleanDir := filepath.Clean(strings.TrimSpace(dir))
	childPrefix := escapeSQLLike(cleanDir+string(os.PathSeparator)) + "%"
	err := database.DB.Preload("Tags").
		Where("directory = ? OR directory LIKE ? ESCAPE '\\'", cleanDir, childPrefix).
		Order("id desc").
		Find(&videos).Error
	return videos, err
}

func escapeSQLLike(input string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	)
	return replacer.Replace(input)
}

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

// PlayRandomVideo 智能加权随机发起播放
func (s *VideoService) PlayRandomVideo() (*PlaybackAttemptResult, error) {
	// 获取播放权重配置
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}
	playWeight := settings.PlayWeight
	if playWeight < 0.1 {
		playWeight = 0.1
	}

	// 仅查询计算权重所需的最少字段，避免全量加载
	var rows []videoScoreRow
	if err := database.DB.Model(&models.Video{}).
		Select("id, play_count, random_play_count").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return &PlaybackAttemptResult{
			DispatchSucceeded: false,
			ReasonCode:        "no_videos",
			UserMessage:       "随机播放失败：当前没有可播放的视频记录。",
		}, nil
	}

	return s.playRandomFromRows(rows, playWeight, "按全库均衡权重选择")
}

// PlayRandomVideoWithFilter 在当前筛选范围内执行加权随机播放。
func (s *VideoService) PlayRandomVideoWithFilter(request RandomPlayRequest) (*PlaybackAttemptResult, error) {
	mode := strings.TrimSpace(request.Mode)
	if mode == "" {
		mode = RandomPlayModeBalanced
	}
	if mode != RandomPlayModeBalanced && mode != RandomPlayModeUnwatched && mode != RandomPlayModeFavorites {
		return nil, fmt.Errorf("不支持的随机播放模式: %s", mode)
	}
	if libraryFilterNeedsSubtitleSync(request.Filter) {
		if err := syncSubtitleIndexesFromFilesystem(); err != nil {
			return nil, err
		}
	}
	playWeight, err := s.getPlayWeight()
	if err != nil {
		return nil, err
	}
	query := database.DB.Model(&models.Video{}).
		Select("videos.id, videos.play_count, videos.random_play_count").
		Where("videos.is_stale = ?", false)
	query, err = applyLibraryFilter(query, request.Filter, time.Now())
	if err != nil {
		return nil, err
	}
	switch mode {
	case RandomPlayModeUnwatched:
		query = query.Where("videos.is_watched = ?", false)
	case RandomPlayModeFavorites:
		query = query.Where("videos.is_favorite = ?", true)
	}
	excludeIDs := uniqueUintIDs(request.ExcludeIDs)
	if len(excludeIDs) > 100 {
		excludeIDs = excludeIDs[len(excludeIDs)-100:]
	}
	if len(excludeIDs) > 0 {
		query = query.Where("videos.id NOT IN ?", excludeIDs)
	}
	var rows []videoScoreRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &PlaybackAttemptResult{
			DispatchSucceeded: false,
			ReasonCode:        "no_filtered_videos",
			UserMessage:       "随机播放失败：当前筛选范围没有可播放的视频。",
			SelectionReason:   randomModeReason(mode),
		}, nil
	}
	return s.playRandomFromRows(rows, playWeight, randomModeReason(mode))
}

func randomModeReason(mode string) string {
	switch mode {
	case RandomPlayModeUnwatched:
		return "在当前筛选范围内优先选择未看视频"
	case RandomPlayModeFavorites:
		return "在当前筛选范围内选择收藏视频"
	default:
		return "在当前筛选范围内按播放次数均衡选择"
	}
}

func (s *VideoService) playRandomFromRows(rows []videoScoreRow, playWeight float64, selectionReason string) (*PlaybackAttemptResult, error) {
	if len(rows) == 0 {
		return &PlaybackAttemptResult{
			DispatchSucceeded: false,
			ReasonCode:        "no_videos",
			UserMessage:       "随机播放失败：当前没有可播放的视频记录。",
		}, nil
	}
	// 计算每个视频的播放分数和最大分数
	scores := make([]float64, len(rows))
	maxScore := 0.0
	for i, r := range rows {
		scores[i] = float64(r.PlayCount)*playWeight + float64(r.RandomPlayCount)
		if scores[i] > maxScore {
			maxScore = scores[i]
		}
	}

	// 计算选择权重并做加权随机选择
	totalWeight := 0.0
	weights := make([]float64, len(rows))
	for i, score := range scores {
		weights[i] = maxScore - score + 1.0
		totalWeight += weights[i]
	}

	// 使用加权随机选择（Go 1.20+ 全局 rand 已自动 seed，无需手动调用）
	randomValue := rand.Float64() * totalWeight
	selectedIdx := len(rows) - 1 // 默认最后一个（防御浮点精度）
	cumulative := 0.0
	for i, w := range weights {
		cumulative += w
		if randomValue <= cumulative {
			selectedIdx = i
			break
		}
	}

	// 仅对选中的视频查询完整记录（含 Tags）
	var selectedVideo models.Video
	if err := database.DB.Preload("Tags").First(&selectedVideo, rows[selectedIdx].ID).Error; err != nil {
		return nil, fmt.Errorf("查询选中视频失败: %w", err)
	}

	// 使用数据库原子操作更新随机播放次数和最后播放时间
	result, err := s.dispatchFormalPlayback(&selectedVideo, true)
	if result != nil {
		result.SelectionReason = selectionReason
	}
	return result, err
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

	if err := openWithDefaultFn(video.Path, false); err != nil {
		return s.buildPlaybackFailureResult(video, "dispatch_failed", err.Error(), false), nil
	}

	now := time.Now()
	updates := map[string]interface{}{
		"last_played_at": now,
		"is_stale":       false,
	}
	if random {
		updates["random_play_count"] = gorm.Expr("random_play_count + 1")
		video.RandomPlayCount++
	} else {
		updates["play_count"] = gorm.Expr("play_count + 1")
		video.PlayCount++
	}
	if err := database.DB.Model(video).Updates(updates).Error; err != nil {
		log.Printf("更新播放统计失败 id=%d err=%v", video.ID, err)
	}
	video.LastPlayedAt = &now
	video.IsStale = false

	return &PlaybackAttemptResult{
		Video:             video,
		DispatchSucceeded: true,
	}, nil
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
