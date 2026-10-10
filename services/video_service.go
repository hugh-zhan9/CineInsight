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
	"sync/atomic"
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
	// watchState 保存已看状态观察者（D-PC52），见 library_service.go 的 SetWatchStateObserver。
	watchState watchStateNotifier
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
//
// 这是恢复备份 / 切换后端进入维护模式（App 的 enterDatabaseRestoreMode）拿路径写锁的入口：它在立维护围栏**之前**
// 调用，所以不查围栏、照常等锁。其他取路径写锁的地方一律经 lockLibraryPaths（修复 K）。
func BeginLibraryMaintenance() func() {
	libraryPathMutationMu.Lock()
	return libraryPathMutationMu.Unlock
}

// lockLibraryPaths 是全局路径锁 libraryPathMutationMu 写锁的获取入口（维护入口 BeginLibraryMaintenance 除外，修复 K）。
// 返回释放函数。获取方式见 acquireLibraryPath（修复 L m3）：围栏生效时立即返回 database.ErrMaintenance；
// 否则阻塞等写锁，等待期间维护开始也返回 ErrMaintenance；拿到写锁后复查围栏。
//
// 「待重启」终态下 enterDatabaseRestoreMode 拿的写锁与维护围栏一直保持到进程退出，这里保证回收站恢复、清除与移除记录、
// 永久删除、移动文件、文件夹改名 / 迁移 / 重映射……这些写锁入口都不会永久阻塞，写者之间的排队与「写者优先于新读者」不变。
func lockLibraryPaths() (func(), error) {
	return acquireLibraryPath(libraryPathMutationMu.TryLock, libraryPathMutationMu.Lock, libraryPathMutationMu.Unlock)
}

// rLockLibraryPaths 是路径读锁的默认入口；与可取消版本共用 rLockLibraryPathsContext 核心。返回释放函数。
//
// 恢复备份与切换后端时，enterDatabaseRestoreMode 先拿路径写锁（BeginLibraryMaintenance）、再立维护围栏；切换成功与
// 「只改配置」之后进入「待重启」终态，写锁与围栏一直保持到进程退出。监听触发的窄对账、Jellyfin 与手机端的删除、
// NFO 导出……这些读锁入口经 acquireLibraryPath 获取，不会在那之后永久阻塞。
//
// 读锁用阻塞的 RLock 等（不轮询）：写者放锁时，排着的读者先于下一个写者拿到锁，批量写者逐项取写锁期间读者不会被饿死；
// 有写者在等时新读者照常让位（写者优先）。
func rLockLibraryPaths() (func(), error) {
	return rLockLibraryPathsContext(context.Background())
}

// libraryMaintenanceStartedFn 是「维护开始」通知的替身入口，只在单测里设置（让通知不到达，以便钉住拿锁之后的围栏复查）；
// 为 nil 时用 database.MaintenanceStarted。用原子指针是因为后台 goroutine（播放重定位等）会并发读它。
var libraryMaintenanceStartedFn atomic.Pointer[func() <-chan struct{}]

func libraryMaintenanceStarted() <-chan struct{} {
	if fn := libraryMaintenanceStartedFn.Load(); fn != nil {
		return (*fn)()
	}
	return database.MaintenanceStarted()
}

// acquireLibraryPath 是路径锁（读或写）的获取实现（修复 L m3，主代理裁决：不轮询）：
//   - 维护围栏生效（database.MaintenanceActive）时立即返回 database.ErrMaintenance，不等锁；
//   - 否则先 tryLock，拿不到就在辅助 goroutine 里阻塞 lock()，调用方同时等「拿到锁」与「维护开始」
//     （database.MaintenanceStarted）两者之一。维护先到则返回 ErrMaintenance；那个辅助 goroutine 之后一旦拿到锁
//     立即释放（不持有、不泄漏锁）。阻塞的 lock() 保留读写锁本身的语义：写者之间排队、有写者在等时新读者让位、
//     写者放锁时排着的读者先于下一个写者；
//   - 拿到锁之后复查一次围栏：生效就放锁并返回 ErrMaintenance。之后的数据库读写反正都会被围栏拒绝，不要先动了文件再失败。
//
// 围栏生效期间绝不无限等待。围栏只是判断那一刻的快照，真正的写入拒绝仍由数据库回调里的 enter 负责。
// 恢复 / 切换在立围栏之前先拿路径写锁（BeginLibraryMaintenance），所以等在它后面的读者与写者都会收到这次通知。
func acquireLibraryPath(tryLock func() bool, lock, unlock func()) (func(), error) {
	return acquireLibraryPathContext(context.Background(), tryLock, lock, unlock)
}
func rLockLibraryPathsContext(ctx context.Context) (func(), error) {
	return acquireLibraryPathContext(ctx, libraryPathMutationMu.TryRLock, libraryPathMutationMu.RLock, libraryPathMutationMu.RUnlock)
}
func acquireLibraryPathContext(ctx context.Context, tryLock func() bool, lock, unlock func()) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if database.MaintenanceActive() {
		return nil, database.ErrMaintenance
	}
	// 在检查围栏之后取通知：两者之间维护开始了，取到的就是已关闭的通道。
	started := libraryMaintenanceStarted()
	if !tryLock() {
		acquired := make(chan struct{})
		abandoned := make(chan struct{})
		go func() {
			lock()
			select {
			case acquired <- struct{}{}:
			case <-abandoned:
				unlock()
			}
		}()
		select {
		case <-acquired:
		case <-ctx.Done():
			close(abandoned)
			return nil, ctx.Err()
		case <-started:
			close(abandoned)
			return nil, database.ErrMaintenance
		}
	}
	if err := ctx.Err(); err != nil {
		unlock()
		return nil, err
	}
	if database.MaintenanceActive() {
		unlock()
		return nil, database.ErrMaintenance
	}
	return unlock, nil
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
	unlock, err := rLockLibraryPaths()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return s.addVideo(path)
}

func (s *VideoService) addVideo(path string) (*models.Video, error) {
	video, _, err := s.addVideoOrRestorePutBack(path)
	return video, err
}

// addVideoOrRestorePutBack 是新增视频的唯一实现。restored=true 表示同路径的文件是被用户在访达里
// 放回原处的那个已删除视频，这次恢复了原记录而没有新建（Minor 2：手动 AddVideo 与扫描同一判定）。
// 调用方持有路径读锁。
func (s *VideoService) addVideoOrRestorePutBack(path string) (*models.Video, bool, error) {
	path = filepath.Clean(strings.TrimSpace(path))

	// 检查文件是否存在
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, fmt.Errorf("文件不存在: %w", err)
	}
	if isKnownNonVideoSourcePath(path) {
		return nil, false, fmt.Errorf("不是视频文件: %s", path)
	}

	// 检查是否已存在：活跃记录一律算已存在；同路径只剩软删行时按身份判定（D-PC03）。
	var existingVideo models.Video
	if result := database.DB.Where("path = ?", path).Limit(1).Find(&existingVideo); result.Error != nil {
		return nil, false, result.Error
	} else if result.RowsAffected == 1 {
		log.Printf("跳过已存在视频 path=%s", path)
		return &existingVideo, false, ErrVideoExists
	}
	if restored, ok, err := s.restorePutBackVideo(path); err != nil {
		return nil, false, err
	} else if ok {
		log.Printf("文件已被放回原处，恢复原记录 video_id=%d", restored.ID)
		return restored, true, nil
	}
	if row, skipErr, err := softDeletedVideoPathSkip(path, info); err != nil {
		return nil, false, err
	} else if skipErr != nil {
		log.Printf("跳过同路径的已删除视频 path=%s", path)
		return row, false, skipErr
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
				return &existingVideo, false, ErrVideoExists
			}
		}
		return nil, false, err
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
	return video, false, nil
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
	unlock, err := rLockLibraryPaths()
	if err != nil {
		return err
	}
	defer unlock()
	return s.deleteVideo(id, deleteFile)
}

// ListTrashEntries 按最新删除优先返回可恢复条目。墓碑（trashStateRemoved，修复 I I-A）不列出。
func (s *VideoService) ListTrashEntries() ([]models.VideoTrashEntry, error) {
	var entries []models.VideoTrashEntry
	if err := database.DB.
		Where("state <> ?", trashStateRemoved).
		Order("created_at DESC, id DESC").
		Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("列出回收站条目失败: %w", err)
	}
	return entries, nil
}

// RestoreTrashEntry 将一个软删除视频恢复到原路径。
func (s *VideoService) RestoreTrashEntry(entryID uint) (*models.Video, error) {
	unlock, err := lockLibraryPaths()
	if err != nil {
		return nil, err
	}
	defer unlock()

	var entry models.VideoTrashEntry
	if err := database.DB.First(&entry, entryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errTrashEntryNotFound(entryID)
		}
		return nil, fmt.Errorf("读取回收站条目失败: %w", err)
	}
	// 墓碑与不存在的条目同一个错误（修复 L m6）。
	if entry.State == trashStateRemoved {
		return nil, errTrashEntryNotFound(entry.ID)
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
	if entry.State == models.TrashStateFileGone {
		return nil, ErrTrashFileGone
	}
	// 墓碑（修复 I I-A）：记录已被「移除记录」移除，对回收站接口一律按不存在处理（修复 L m6）。
	if entry.State == trashStateRemoved {
		return nil, errTrashEntryNotFound(entry.ID)
	}
	// 原路径已被新的活跃记录占用（例如「只删记录」后同路径的新文件被收录）时拒绝恢复：
	// 部分唯一索引会在事务里报一个看不懂的约束错误，这里前置成明确的中文错误。
	var occupant models.Video
	occupantResult := database.DB.Where("path = ? AND id <> ?", entry.OriginalPath, video.ID).Limit(1).Find(&occupant)
	if occupantResult.Error != nil {
		return nil, fmt.Errorf("检查原路径活跃记录失败: %w", occupantResult.Error)
	}
	if occupantResult.RowsAffected == 1 {
		// 恢复中断的行：文件不在原处就退回 deleted，不让它卡在 restoring（修复 L m2）。
		if entry.State == trashStateRestoring {
			if err := releaseOccupiedRestoringEntry(videoTrashKind, entry.ID, videoEntryFacts(*entry), entry.OriginalPath); err != nil {
				return nil, err
			}
		}
		return nil, ErrTrashPathOccupied
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

	// movedFromTrash：这一次恢复把文件从废纸篓移回了原处。只有这种情形在恢复事务失败时才把文件
	// 移回废纸篓作补偿；文件本来就在原路径（访达放回、上次中断已移回）时一律不动文件（I-1）。
	movedFromTrash := false
	trashService := NewTrashService()
	if entry.Mode == models.TrashModeRecordOnly {
		// 只删记录：文件一直在原地没动过，恢复只还原数据库（原 ID、标签、人物），不碰文件。
	} else if entry.FileMoved {
		var err error
		movedFromTrash, err = ensureTrashEntryFileRestored(trashService, *entry)
		if err != nil {
			_ = markTrashEntryRecoverable(entry.ID, err)
			return nil, err
		}
	} else if info, err := os.Stat(entry.OriginalPath); err != nil {
		restoreErr := fmt.Errorf("原文件不可用，无法恢复记录: %w", err)
		if os.IsNotExist(err) {
			if unavailable := mediaPathUnavailable(entry.OriginalPath); unavailable != nil {
				restoreErr = unavailable
			}
		}
		_ = markTrashEntryRecoverable(entry.ID, restoreErr)
		return nil, restoreErr
	} else if info.IsDir() {
		restoreErr := fmt.Errorf("原路径不是视频文件: %s", entry.OriginalPath)
		_ = markTrashEntryRecoverable(entry.ID, restoreErr)
		return nil, restoreErr
	} else if !trashEntryFileMatches(entry.OriginalPath, info, *entry) &&
		!(entry.DeletedBy == "scanner" && entry.FileIdentity == "" && entry.FileSHA256 == "" && info.Size() == video.Size) {
		restoreErr := fmt.Errorf("原路径文件与删除记录不一致: %s", entry.OriginalPath)
		_ = markTrashEntryRecoverable(entry.ID, restoreErr)
		return nil, restoreErr
	}

	// 旧版 trash/ 里还留着与原路径同一个文件的另一个名字时，事务里条目改为墓碑而不是硬删，提交之后清理成功才删掉墓碑
	// （修复 L m5）：清理失败（或进程在两者之间退出）时目录继续登记，残留名字不会被扫描当成新文件收录。
	residue := legacyRestoreResidue(entry.Mode, entry.FileMoved, entry.OriginalPath, entry.TrashPath)
	var restored models.Video
	err := database.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.Video{}).
			Unscoped().
			Where("id = ? AND deleted_at IS NOT NULL", video.ID).
			Updates(map[string]interface{}{"deleted_at": nil, "is_stale": false, "stale_reason": ""})
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
		if residue {
			return retireRestoredEntryAsTombstoneTx(tx, videoTrashKind, entry.ID)
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
			finishRestoredLegacyResidue(videoTrashKind, residue, entry.ID, entry.Mode, entry.FileMoved, entry.OriginalPath, entry.TrashPath)
			if loadErr := database.DB.Preload("Tags").First(&restored, video.ID).Error; loadErr != nil {
				return nil, fmt.Errorf("恢复已提交，但读取结果失败: %w", loadErr)
			}
			return &restored, nil
		}
		if !rolledBack {
			_ = recordTrashEntryError(entry.ID, fmt.Errorf("恢复状态不一致: %w", err))
			return nil, fmt.Errorf("恢复状态不一致，未执行文件补偿: %w", err)
		}
		if movedFromTrash {
			if rollbackErr := trashService.RestoreFromTrash(entry.OriginalPath, entry.TrashPath); rollbackErr != nil {
				_ = recordTrashEntryError(entry.ID, rollbackErr)
				return nil, fmt.Errorf("恢复数据库记录失败: %w；文件回滚失败: %v", err, rollbackErr)
			}
		}
		_ = database.DB.Model(entry).Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": err.Error()}).Error
		return nil, fmt.Errorf("恢复数据库记录失败: %w", err)
	}
	finishRestoredLegacyResidue(videoTrashKind, residue, entry.ID, entry.Mode, entry.FileMoved, entry.OriginalPath, entry.TrashPath)
	log.Printf("视频恢复 video_id=%d original_deleted_by=%s", restored.ID, entry.DeletedBy)
	return &restored, nil
}

// deleteVideo 删一条视频记录，成功后连带删掉它的播放代理（D-002）。
//
// 级联放在这一层而不是 DeleteVideo：扫描对账里消失的文件也走 deleteVideo，
// 那些视频的代理同样该跟着走——软删除与永久删除在这里是同一条路，回收站里的
// 视频不需要代理，恢复之后要用再重新触发一次。
func (s *VideoService) deleteVideo(id uint, deleteFile bool) error {
	return s.deleteVideoBy(id, deleteFile, "user")
}

func (s *VideoService) deleteVideoBy(id uint, deleteFile bool, deletedBy string) error {
	_, err := s.deleteVideoBatchItem(id, deleteFile, deletedBy, newDeleteBatchID())
	return err
}

// deleteVideoBatchItem 是带批次标识与结果码的单项删除。返回的 code 只在 err==nil 时有意义
// （ok 或 file_missing），失败时的结果码由 trashResultCodeForError 从 err 推出。
func (s *VideoService) deleteVideoBatchItem(id uint, deleteFile bool, deletedBy, batchID string) (string, error) {
	code, err := s.deleteVideoRecordBatch(id, deleteFile, deletedBy, batchID)
	if err != nil {
		return "", err
	}
	s.playbackProxies().DeleteForVideo(id)
	log.Printf("视频软删除 video_id=%d deleted_by=%s delete_file=%t code=%s", id, deletedBy, deleteFile, code)
	return code, nil
}

func (s *VideoService) deleteVideoRecord(id uint, deleteFile bool) error {
	return s.deleteVideoRecordBy(id, deleteFile, "user")
}

func (s *VideoService) deleteVideoRecordBy(id uint, deleteFile bool, deletedBy string) error {
	_, err := s.deleteVideoRecordBatch(id, deleteFile, deletedBy, newDeleteBatchID())
	return err
}

// deleteVideoRecordBatch 删除一条视频记录并建回收站条目（详细设计 §2.1 结果契约）。
//
// 建条目的每条路径都写非空 mode 与 delete_batch_id：
//   - deleteFile=true 且文件在：mode=trash，走 pending_move → 系统废纸篓 → 事务软删；
//   - deleteFile=false，或文件在但位于旧版回收站目录：mode=record_only，记录身份；
//   - 扫描器删除、或用户删除时文件已不在：mode=missing。
//
// 不支持废纸篓、没权限、卷离线时记录与条目都保持原状（pending 条目被撤销），不降级。
func (s *VideoService) deleteVideoRecordBatch(id uint, deleteFile bool, deletedBy, batchID string) (string, error) {
	if batchID == "" {
		batchID = newDeleteBatchID()
	}
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return "", err
	}
	var existingEntry models.VideoTrashEntry
	existingResult := database.DB.Where("video_id = ?", video.ID).Limit(1).Find(&existingEntry)
	if existingResult.Error != nil {
		return "", fmt.Errorf("检查既有回收站条目失败: %w", existingResult.Error)
	}
	if existingResult.RowsAffected == 1 {
		switch existingEntry.State {
		case trashStatePendingMove:
			completed, reconcileErr := s.reconcilePendingDelete(&existingEntry)
			if reconcileErr != nil {
				_ = recordTrashEntryError(existingEntry.ID, reconcileErr)
				return "", fmt.Errorf("继续上次删除失败: %w", reconcileErr)
			}
			if completed {
				return TrashResultOK, nil
			}
			// 上次的删除没有移动过文件，已被取消；按本次请求重新删除。
		case trashStateRollback:
			if reconcileErr := reconcileTrashRollback(&existingEntry); reconcileErr != nil {
				_ = recordTrashEntryError(existingEntry.ID, reconcileErr)
				return "", fmt.Errorf("完成上次删除回滚失败: %w", reconcileErr)
			}
		case trashStateRemoved:
			if deletedBy == "scanner" {
				// 扫描器因文件缺失软删一条挂着「恢复后残留」墓碑的记录（修复 N I-1，主代理裁决）：不做残留收尾，照常软删
				// （deleted_by='scanner'），墓碑保留、继续登记旧版 trash/ 目录。video_id 唯一，建不进 missing 条目；
				// 文件回到原处时由 addScannedVideo 恢复原记录（restoreScannerDeletedVideoWithoutEntry）。
				// 软删实际写入 0 行（读取之后记录已被别处软删或硬删，修复 P m-4）：这次什么也没删，回滚并返回与
				// 「记录已不在库」相同的结果，扫描不计入 Deleted。
				if err := database.Transaction(func(tx *gorm.DB) error {
					rows, err := finalizeVideoDeletionRowsTx(tx, &video, deletedBy)
					if err != nil {
						return err
					}
					if rows == 0 {
						return gorm.ErrRecordNotFound
					}
					return nil
				}); err != nil {
					return "", err
				}
				return TrashResultOK, nil
			}
			// 上次恢复成功、旧版 trash/ 里的残留名字没清掉时留下的墓碑（修复 L m5）：先补做清理、删掉墓碑，再照常删除；
			// 清不掉就不删（墓碑要继续登记那个目录，而 video_id 唯一，新条目建不进去）。残留判定用记录的当前路径（修复 N m1）。
			if err := settleRestoredTrashTombstone(videoTrashKind, existingEntry.ID, existingEntry.Mode, existingEntry.FileMoved,
				existingEntry.OriginalPath, video.Path, existingEntry.TrashPath); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("视频已有回收站条目，不能重复删除: %d", existingEntry.ID)
		}
	}

	entry := models.VideoTrashEntry{
		DeletedBy:     deletedBy,
		FileSize:      video.Size,
		VideoID:       video.ID,
		VideoName:     video.Name,
		OriginalPath:  video.Path,
		State:         trashStateDeleted,
		DeleteBatchID: batchID,
	}
	sourceInfo, err := os.Stat(video.Path)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("检查待删除文件失败: %w", err)
	}
	if err == nil {
		if sourceInfo.IsDir() {
			return "", fmt.Errorf("视频路径不是文件: %s", video.Path)
		}
		// trash / record_only 只记录身份（大小、mtime、dev:inode），不读文件内容做 SHA-256。
		entry.FileSize = sourceInfo.Size()
		entry.FileModTime = sourceInfo.ModTime().UnixNano()
		entry.FileIdentity = stableFileIdentity(sourceInfo)
	} else {
		sourceInfo = nil
	}

	code := TrashResultOK
	switch {
	case deletedBy == "scanner":
		entry.Mode = models.TrashModeMissing
	case sourceInfo == nil && deleteFile:
		// 文件不在磁盘上。先确认它所在的扫描根现在可访问：卷没挂载不能被当成「文件没了」，
		// 也不能降级成只删记录。文件确实不在时只清库记录。
		if err := mediaPathUnavailable(video.Path); err != nil {
			return "", err
		}
		entry.Mode = models.TrashModeMissing
		code = TrashResultFileMissing
	case sourceInfo == nil, !deleteFile, isRecordedTrashPath(video.Path):
		// 只有确实登记过的回收站位置才降级为只删记录；启发式旧版 trash/ 目录不算（I-3）。
		entry.Mode = models.TrashModeRecordOnly
	default:
		entry.Mode = models.TrashModeTrash
	}

	if entry.Mode != models.TrashModeTrash {
		err := database.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
			return finalizeVideoDeletionTx(tx, &video, entry.DeletedBy)
		})
		return code, err
	}
	return s.deleteVideoToSystemTrash(&video, &entry)
}

// deleteVideoToSystemTrash 是 mode=trash 的删除：pending_move 条目 → 移入系统废纸篓 →
// 记下实际路径 → 事务里置 deleted 并软删记录。
func (s *VideoService) deleteVideoToSystemTrash(video *models.Video, entry *models.VideoTrashEntry) (string, error) {
	trashService := NewTrashService()
	entry.State = trashStatePendingMove
	entry.TrashPath = ""
	if err := database.DB.Create(entry).Error; err != nil {
		return "", fmt.Errorf("记录待删除文件失败: %w", err)
	}
	cancelPending := func() {
		_ = database.DB.Where("id = ? AND state = ?", entry.ID, trashStatePendingMove).Delete(&models.VideoTrashEntry{}).Error
	}

	info, statErr := os.Stat(video.Path)
	if statErr != nil {
		cancelPending()
		return "", fmt.Errorf("检查待删除文件失败: %w", statErr)
	}
	want := trashFileID{Size: entry.FileSize, ModTimeNS: entry.FileModTime, Identity: entry.FileIdentity}
	if !want.strictMatch(info) {
		cancelPending()
		return "", fmt.Errorf("文件在删除过程中发生了变化，已取消删除")
	}

	trashedPath, moveErr := trashService.MoveToTrash(video.Path)
	if errors.Is(moveErr, errTrashLocationUnknown) {
		// 文件已进废纸篓，只是系统没告诉我们位置。条目必须保留：走崩溃恢复的「按身份在废纸篓查找」，
		// 找不到则置 deleted + 「文件位置未知」，保证记录与文件不脱节（Minor 1）。
		completed, recErr := s.reconcilePendingTrashDelete(entry)
		if recErr != nil {
			return "", fmt.Errorf("文件已移入废纸篓，但无法确认位置: %w", recErr)
		}
		if !completed {
			return "", fmt.Errorf("移动文件到回收站失败: %w", moveErr)
		}
		return TrashResultOK, nil
	}
	if moveErr != nil {
		cancelPending()
		if errors.Is(moveErr, os.ErrNotExist) {
			// 文件在检查与移动之间消失了：与「文件本来就不在」同一处理，只清库记录；
			// 但卷此刻离线时不是「文件没了」，不降级（I6）。
			if err := mediaPathUnavailable(video.Path); err != nil {
				return "", err
			}
			entry.ID = 0
			entry.CreatedAt = time.Time{}
			entry.UpdatedAt = time.Time{}
			entry.State = trashStateDeleted
			entry.Mode = models.TrashModeMissing
			err := database.Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(entry).Error; err != nil {
					return err
				}
				return finalizeVideoDeletionTx(tx, video, entry.DeletedBy)
			})
			return TrashResultFileMissing, err
		}
		if errors.Is(moveErr, ErrTrashUnsupportedVolume) || errors.Is(moveErr, ErrTrashPermissionDenied) {
			return "", moveErr
		}
		return "", fmt.Errorf("移动文件到回收站失败: %w", moveErr)
	}

	// 文件已在系统废纸篓里。先把实际路径写回条目：这一步之前进程退出，条目会停在 pending_move
	// 且 trash_path 为空，启动对账按文件身份去废纸篓里找（详细设计 §2.1 崩溃恢复）。
	entry.TrashPath = trashedPath
	if err := recordTrashedPath("video_trash_entries", entry.ID, trashedPath); err != nil {
		if errors.Is(err, errTrashEntryStateChanged) {
			// 条目状态已被他方改变（并发删除同一视频、启动对账）：先重读。他方已终结就不能回滚文件，
			// 否则会出现「记录已删、文件被我们放回原处」（Minor 2）。
			var current models.VideoTrashEntry
			reread := database.DB.Where("id = ?", entry.ID).Limit(1).Find(&current)
			if reread.Error == nil && reread.RowsAffected == 1 {
				if current.State == trashStateDeleted {
					return TrashResultOK, nil
				}
				// 文件确实已经在废纸篓里，只是条目被并发操作改成了别的状态：如实说明，不回滚文件，
				// 由启动对账（或下一次删除/恢复）按文件身份收尾（Minor 12）。
				return "", fmt.Errorf("文件已移入废纸篓，但删除记录的状态已被其他操作改变，数据库状态待对账: %w", err)
			}
			// 条目已不存在（他方取消了删除）：文件却在废纸篓里，落到下面的回滚逻辑把它放回原处。
		}
		if rollbackErr := trashService.RestoreFromTrashVerified(trashedPath, video.Path, want); rollbackErr != nil {
			return "", fmt.Errorf("记录废纸篓位置失败: %w；文件回滚失败，将在下次启动时按文件身份对账: %v", err, rollbackErr)
		}
		cancelPending()
		return "", fmt.Errorf("记录废纸篓位置失败，已撤销删除: %w", err)
	}
	trashInfo, err := os.Stat(trashedPath)
	if err != nil {
		return "", fmt.Errorf("读取回收站文件信息失败: %w", err)
	}
	if !want.strictMatch(trashInfo) {
		return "", ErrTrashIdentityMismatch
	}
	log.Printf("视频已移入系统废纸篓 video_id=%d", video.ID)

	err = database.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.VideoTrashEntry{}).
			Where("id = ? AND state = ?", entry.ID, trashStatePendingMove).
			Updates(map[string]interface{}{
				"state":      trashStateDeleted,
				"file_moved": true,
				"last_error": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("待删除条目状态已变化: %d", entry.ID)
		}
		return finalizeVideoDeletionTx(tx, video, entry.DeletedBy)
	})
	if err == nil {
		return TrashResultOK, nil
	}
	committed, rolledBack, confirmErr := confirmDeleteTransactionOutcome(video.ID, entry.ID)
	if confirmErr != nil {
		_ = recordTrashEntryError(entry.ID, fmt.Errorf("删除提交结果无法确认: %w", err))
		return "", fmt.Errorf("删除提交结果无法确认，文件和操作日志已保留供启动对账: %w", err)
	}
	if committed {
		return TrashResultOK, nil
	}
	if !rolledBack {
		_ = recordTrashEntryError(entry.ID, fmt.Errorf("删除状态不一致: %w", err))
		return "", fmt.Errorf("删除状态不一致，未执行文件补偿: %w", err)
	}

	_ = database.DB.Model(entry).Where("state = ?", trashStatePendingMove).Update("state", trashStateRollback).Error
	if rollbackErr := trashService.RestoreFromTrashVerified(entry.TrashPath, video.Path, want); rollbackErr != nil {
		_ = recordTrashEntryError(entry.ID, rollbackErr)
		return "", fmt.Errorf("删除数据库记录失败: %w；文件回滚失败: %v", err, rollbackErr)
	}
	if cleanupErr := database.DB.Delete(entry).Error; cleanupErr != nil {
		_ = recordTrashEntryError(entry.ID, cleanupErr)
		return "", fmt.Errorf("删除数据库记录失败: %w；清理待删除条目失败: %v", err, cleanupErr)
	}
	return "", fmt.Errorf("删除数据库记录失败: %w", err)
}

func finalizeVideoDeletionTx(tx *gorm.DB, video *models.Video, deletedBy string) error {
	_, err := finalizeVideoDeletionRowsTx(tx, video, deletedBy)
	return err
}

// finalizeVideoDeletionRowsTx 是 finalizeVideoDeletionTx 的本体，另返回软删实际写入的行数（修复 P m-4）：软删带
// deleted_at IS NULL 条件，记录在读取之后已被别处软删（或硬删）时为 0，调用方据此不把这次算作删除。
func finalizeVideoDeletionRowsTx(tx *gorm.DB, video *models.Video, deletedBy string) (int64, error) {
	if err := tx.Model(video).Update("deleted_by", deletedBy).Error; err != nil {
		return 0, err
	}
	if err := tx.Where("video_id = ?", video.ID).Delete(&models.SubtitleSegment{}).Error; err != nil {
		return 0, err
	}
	if err := tx.Where("video_id = ?", video.ID).Delete(&models.SubtitleIndexState{}).Error; err != nil {
		return 0, err
	}
	result := tx.Delete(video)
	return result.RowsAffected, result.Error
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

// ensureTrashEntryFileRestored 确保条目的文件回到原路径。返回的 movedFromTrash 只在「这一次把文件
// 从废纸篓移回了原处」时为 true；文件本来就在原路径（访达放回、硬链接、上次恢复中断前已移回）时为
// false，调用方据此决定事务失败后要不要补偿（I-1）。
//
//   - trash 模式：原路径上的文件与条目严格一致（inode + 大小 + mtime）就是当初删掉的那个文件，直接判定
//     「已在原处」，不读废纸篓一侧（I-A）：读不到废纸篓（EPERM 等）不得让恢复失败；废纸篓里同 inode 的
//     另一个名字（硬链接）也不删（M1）。
//   - legacy_trash 旧行：原路径上的文件大小与 inode 与条目一致（与放回判定同一口径，I-1）同样直接判定
//     「已在原处」，不读 trash/ 一侧；条目记录了 file_sha256 时先核对一次内容哈希，不一致就是另一个文件
//     （inode 被复用或放回后被改过），报「原路径文件与删除记录不一致」、不恢复（修复 I m-b）。
//   - 原路径与废纸篓是同一个文件（硬链接）：文件本来就在原处，返回 false、不删废纸篓那个名字（M1）；
//     legacy_trash 旧行在恢复提交之后才清理旧版 trash/ 里的残留名字（removeLegacyTrashLinkAfterRestore；清理失败时条目
//     保留为墓碑，修复 L m5）。
//   - 原路径用 Lstat 读取（m1）：是符号链接（可能正指向废纸篓里的文件）时返回 ErrTrashOriginalNotRegular，
//     不跟随、不覆盖、不删任何名字。
func ensureTrashEntryFileRestored(trashService *TrashService, entry models.VideoTrashEntry) (bool, error) {
	want := trashFileID{Size: entry.FileSize, ModTimeNS: entry.FileModTime, Identity: entry.FileIdentity}
	strict := !isLegacyTrashMode(entry.Mode)
	matches := func(path string, info os.FileInfo) bool {
		if strict {
			return want.strictMatch(info)
		}
		return trashEntryFileMatches(path, info, entry)
	}
	mismatch := func(format string) error {
		if strict {
			return ErrTrashIdentityMismatch
		}
		return fmt.Errorf(format, entry.OriginalPath)
	}

	originalInfo, originalExists, err := originalFileState(entry.OriginalPath)
	if err != nil {
		return false, err
	}
	if originalExists {
		if strict && want.strictMatch(originalInfo) {
			return false, nil
		}
		if !strict && entryFileAtOriginal(videoEntryFacts(entry), originalInfo.Size(), originalInfo.ModTime().UnixNano(), stableFileIdentity(originalInfo)) {
			switch content, contentErr := checkLegacyPutBackContent(videoEntryFacts(entry), entry.FileSHA256, entry.OriginalPath); content {
			case legacyContentMismatch:
				return false, mismatch("原路径文件与删除记录不一致: %s")
			case legacyContentUndetermined:
				// 读不出哈希、无法判定（修复 L m1）：显式恢复同样拒绝，什么都不动。
				return false, contentErr
			}
			return false, nil
		}
	}
	trashInfo, trashExists, err := regularFileState(entry.TrashPath)
	if err != nil {
		return false, err
	}
	if originalExists && trashExists {
		if !os.SameFile(originalInfo, trashInfo) {
			return false, ErrTrashPathOccupied
		}
		// 硬链接：原路径上本来就有这个文件。trash 模式走到这里说明它与条目不符（严格一致已在上面返回）。
		if strict {
			return false, ErrTrashIdentityMismatch
		}
		return false, nil
	}
	if originalExists {
		if !matches(entry.OriginalPath, originalInfo) {
			return false, mismatch("原路径文件与删除记录不一致: %s")
		}
		// 文件本来就在原路径：这次没有动它。
		return false, nil
	}
	// 原路径上没有文件时先看卷在不在（Minor 3）：离线时两边都读不到，那不是「废纸篓里的文件已不存在」；
	// 废纸篓那一份还在时也不能往未挂载卷留下的空挂载点里恢复。读不到（权限）同样不动（m3）。
	if err := trashEntryUnavailable(entry.OriginalPath, entry.TrashPath); err != nil {
		return false, err
	}
	if !trashExists {
		return false, ErrTrashFileGone
	}
	if !matches(entry.TrashPath, trashInfo) {
		return false, mismatch("回收站文件与删除记录不一致: %s")
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
	if entry.FileIdentity != "" && sameFileInode(entry.FileIdentity, info) {
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
	// 恢复事务在有旧版残留名字时把条目改为墓碑而不是删掉（修复 L m5）：记录已活跃、条目是墓碑同样是已提交。
	if !video.DeletedAt.IsValid() && entry.State == trashStateRemoved {
		return true, false, nil
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
	if !isLegacyTrashMode(entry.Mode) && entry.State == trashStatePendingMove {
		return s.cancelInterruptedTrashDeletion(entry)
	}
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
			// 只有原路径与 trash/ 里是同一个普通文件的两个硬链接时才删残留名字（m1）：原路径是指向它的
			// 符号链接时，跟随链接的 Stat 同样报「同一个文件」，删掉的就是真正的文件。
			if !hardLinkedRegularNames(entry.OriginalPath, entry.TrashPath) {
				_ = recordTrashEntryError(entry.ID, errLegacyResidueNotHardLink)
				return nil, errLegacyResidueNotHardLink
			}
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

// cancelInterruptedTrashDeletion 撤销一次 mode=trash 中断在 pending_move 的删除：文件没动就直接删条目，
// 已进废纸篓（trash_path 已记录或按身份找到）就核对身份后移回原路径；位置未知则拒绝。
func (s *VideoService) cancelInterruptedTrashDeletion(entry *models.VideoTrashEntry) (*models.Video, error) {
	var video models.Video
	if err := database.DB.Preload("Tags").First(&video, entry.VideoID).Error; err != nil {
		return nil, fmt.Errorf("读取活动视频失败: %w", err)
	}
	want := trashFileID{Size: entry.FileSize, ModTimeNS: entry.FileModTime, Identity: entry.FileIdentity}
	location, foundPath, err := resolvePendingTrashMove(entry.OriginalPath, entry.TrashPath, want)
	if err != nil {
		_ = recordTrashEntryError(entry.ID, err)
		return nil, err
	}
	if location == pendingFileUnknown {
		if err := mediaPathUnavailable(entry.OriginalPath); err != nil {
			return nil, err
		}
		_ = recordTrashEntryError(entry.ID, ErrTrashFileGone)
		return nil, ErrTrashFileGone
	}
	if location == pendingFileInTrash {
		if err := NewTrashService().RestoreFromTrashVerified(foundPath, entry.OriginalPath, want); err != nil {
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
	unlock, err := lockLibraryPaths()
	if err != nil {
		return err
	}
	defer unlock()

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
			_, err = s.reconcilePendingDelete(entry)
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
	// 旧版 trash/ 目录已不存在的墓碑不再需要登记，清理掉（修复 L m4）。
	if err := sweepGoneTrashTombstones(videoTrashKind); err != nil {
		reconcileErrors = append(reconcileErrors, err)
	}
	return errors.Join(reconcileErrors...)
}

// reconcilePendingDelete 对账一条 pending_move 条目。completed=true 表示删除已补提交（记录已软删）；
// false 表示这次删除没有移动过文件，已被取消，视频记录保持活跃。
func (s *VideoService) reconcilePendingDelete(entry *models.VideoTrashEntry) (bool, error) {
	if !isLegacyTrashMode(entry.Mode) {
		return s.reconcilePendingTrashDelete(entry)
	}
	err := s.reconcileLegacyPendingDelete(entry)
	return err == nil, err
}

// reconcilePendingTrashDelete 处理 mode=trash 的 pending_move（详细设计 §2.1 崩溃恢复三分支）：
//  1. 原路径文件仍在且身份一致 → 取消，回滚为活跃记录；
//  2. 原路径已不在，在废纸篓里按身份找到 → 补写 trash_path，置 deleted；
//  3. 都找不到 → 置 deleted 并写「文件位置未知」，回收站里只提供「移除记录」。
func (s *VideoService) reconcilePendingTrashDelete(entry *models.VideoTrashEntry) (bool, error) {
	var video models.Video
	if err := database.DB.Unscoped().First(&video, entry.VideoID).Error; err != nil {
		return false, err
	}
	want := trashFileID{Size: entry.FileSize, ModTimeNS: entry.FileModTime, Identity: entry.FileIdentity}
	location, foundPath, err := resolvePendingTrashMove(entry.OriginalPath, entry.TrashPath, want)
	if err != nil {
		return false, err
	}
	if location == pendingFileUnknown {
		// 卷离线（或因权限读不到）时废纸篓里也读不到：不能当成「位置未知」落库，保持 pending_move 等卷回来（I1、m3）。
		if err := mediaPathUnavailable(entry.OriginalPath); err != nil {
			return false, err
		}
	}
	if location == pendingFileAtOriginal {
		result := database.DB.Where("id = ? AND state = ?", entry.ID, trashStatePendingMove).Delete(&models.VideoTrashEntry{})
		if result.Error != nil {
			return false, fmt.Errorf("取消中断删除失败: %w", result.Error)
		}
		log.Printf("视频删除中断且文件未移动，已取消 video_id=%d", entry.VideoID)
		return false, nil
	}
	updates := map[string]interface{}{"state": trashStateDeleted, "file_moved": true, "last_error": ""}
	if location == pendingFileUnknown {
		updates = map[string]interface{}{"state": trashStateDeleted, "file_moved": false, "trash_path": "", "last_error": trashUnknownLocationMessage}
	}
	err = database.Transaction(func(tx *gorm.DB) error {
		if location == pendingFileInTrash && foundPath != entry.TrashPath {
			if err := claimTrashPathTx(tx, "video_trash_entries", entry.ID, foundPath); err != nil {
				return err
			}
			updates["trash_path"] = foundPath
		}
		result := tx.Model(&models.VideoTrashEntry{}).
			Where("id = ? AND state = ?", entry.ID, trashStatePendingMove).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("待删除条目状态已变化: %d", entry.ID)
		}
		return finalizeVideoDeletionTx(tx, &video, entry.DeletedBy)
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *VideoService) reconcileLegacyPendingDelete(entry *models.VideoTrashEntry) error {
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
			// 删原路径那个名字之前确认两侧是同一个普通文件的两个硬链接（m1）。
			if !hardLinkedRegularNames(entry.OriginalPath, entry.TrashPath) {
				return errLegacyResidueNotHardLink
			}
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
		return finalizeVideoDeletionTx(tx, &video, entry.DeletedBy)
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
			// 只有两侧是同一个普通文件的两个硬链接时才删 trash/ 里的名字（m1）。
			if !hardLinkedRegularNames(entry.OriginalPath, entry.TrashPath) {
				return errLegacyResidueNotHardLink
			}
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
	batchID := newDeleteBatchID()
	for _, videoID := range videoIDs {
		_, err := s.deleteVideoBatchItemLocked(videoID, deleteFile, batchID)
		result.record(videoID, err)
	}
	return result
}

// deleteVideoBatchItemLocked 在路径读锁下做一项用户删除；读锁拿不到（维护围栏生效）时返回 database.ErrMaintenance（修复 I m1）。
func (s *VideoService) deleteVideoBatchItemLocked(videoID uint, deleteFile bool, batchID string) (string, error) {
	unlock, err := rLockLibraryPaths()
	if err != nil {
		return "", err
	}
	defer unlock()
	return s.deleteVideoBatchItem(videoID, deleteFile, "user", batchID)
}

// DeleteVideosDetailed 是桌面端使用的批量删除：逐项返回结果码，整批共享一个 batch_id，
// 可带 requestID 上报进度并在两项之间被 CancelBatchDelete 取消（D-PC51）。
func (s *VideoService) DeleteVideosDetailed(videoIDs []uint, deleteFile bool, opts BatchDeleteOptions) *BatchResult {
	batchID := newDeleteBatchID()
	result := newBatchResult(len(videoIDs), batchID)
	cancelled, finish := opts.begin()
	defer finish()
	for index, videoID := range videoIDs {
		if cancelled() {
			result.addCode(videoID, TrashResultCancelled, "")
			continue
		}
		code, err := s.deleteVideoBatchItemLocked(videoID, deleteFile, batchID)
		result.addOutcome(videoID, code, err)
		opts.report(index+1, len(videoIDs))
	}
	return result
}

// PermanentlyDeleteVideos 永久删除视频文件与记录，只用于 trash_unsupported 之后用户明确选择
// 「永久删除」（二次确认在前端）。流程：身份核对 → 删文件 → 硬删记录（级联）。
func (s *VideoService) PermanentlyDeleteVideos(videoIDs []uint) *BatchResult {
	result := newBatchResult(len(videoIDs), "")
	for _, videoID := range videoIDs {
		code, err := s.permanentlyDeleteVideoLocked(videoID)
		result.addOutcome(videoID, code, err)
	}
	return result
}

// permanentlyDeleteVideoLocked 在路径写锁下永久删除一项；写锁拿不到（维护围栏生效）时返回 database.ErrMaintenance（修复 K）。
func (s *VideoService) permanentlyDeleteVideoLocked(id uint) (string, error) {
	unlock, err := lockLibraryPaths()
	if err != nil {
		return "", err
	}
	defer unlock()
	return s.permanentlyDeleteVideo(id)
}

func (s *VideoService) permanentlyDeleteVideo(id uint) (string, error) {
	// 只接受活跃记录（默认 scope）：已软删的视频归回收站管，永久删除会绕过条目与身份核对（I5）。
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrPermanentDeleteNotActive
		}
		return "", err
	}
	var existingEntry models.VideoTrashEntry
	existingResult := database.DB.Where("video_id = ?", video.ID).Limit(1).Find(&existingEntry)
	if existingResult.Error != nil {
		return "", fmt.Errorf("检查回收站条目失败: %w", existingResult.Error)
	}
	if existingResult.RowsAffected == 1 {
		if existingEntry.State != trashStateRemoved {
			return "", ErrPermanentDeleteHasTrashEntry
		}
		// 恢复成功后留下的墓碑（修复 L m5）：先补做残留清理、删掉墓碑；清不掉就不删文件——删掉原文件后，旧版 trash/ 里
		// 那个名字就是这份内容仅剩的一个名字，墓碑随记录删掉后它会被扫描当成新文件收录。残留判定用记录的当前路径（修复 N m1）。
		if err := settleRestoredTrashTombstone(videoTrashKind, existingEntry.ID, existingEntry.Mode, existingEntry.FileMoved,
			existingEntry.OriginalPath, video.Path, existingEntry.TrashPath); err != nil {
			return "", err
		}
	}
	if err := ensureNoActiveEnhancement(database.DB, video.ID); err != nil {
		return "", err
	}
	code := TrashResultOK
	info, err := os.Stat(video.Path)
	switch {
	case err == nil:
		if info.IsDir() {
			return "", fmt.Errorf("视频路径不是文件")
		}
		// 没有删除时记录的条目可核对（不支持废纸篓时删除已被撤销），只能核对入库时的大小。
		if video.Size != 0 && info.Size() != video.Size {
			return "", ErrTrashIdentityMismatch
		}
		if err := os.Remove(video.Path); err != nil {
			if errors.Is(err, os.ErrPermission) {
				return "", ErrTrashPermissionDenied
			}
			return "", fmt.Errorf("删除文件失败: %w", err)
		}
	case os.IsNotExist(err):
		if err := mediaPathUnavailable(video.Path); err != nil {
			return "", err
		}
		code = TrashResultFileMissing
	default:
		return "", fmt.Errorf("检查待删除文件失败: %w", err)
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("video_id = ?", video.ID).Delete(&models.VideoTrashEntry{}).Error; err != nil {
			return err
		}
		return hardDeleteVideoTx(tx, video.ID)
	}); err != nil {
		return "", fmt.Errorf("删除数据库记录失败: %w", err)
	}
	s.playbackProxies().DeleteForVideo(video.ID)
	log.Printf("视频永久删除 video_id=%d code=%s", video.ID, code)
	return code, nil
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
		return setManualAutomaticVideoTag(videoID, tag, true)
	}

	// 手动加上的标签若正是某条待审 AI 候选匹配的标签，候选在同一事务里作废为 superseded
	// （D-PC28 规则 2，META-06）；其他候选不受影响。前端按 (video_id, tag_id) 局部移除。
	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&video).Association("Tags").Append(&tag); err != nil {
			return err
		}
		superseded, err := SupersedeCandidatesForManualTag(tx, videoID, tagID)
		if err != nil {
			return fmt.Errorf("作废对应的 AI 候选失败: %w", err)
		}
		if len(superseded) > 0 {
			log.Printf("手动加标签作废 AI 候选 video_id=%d tag_id=%d candidates=%d", videoID, tagID, len(superseded))
		}
		return nil
	})
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
		return setManualAutomaticVideoTag(videoID, tag, false)
	}

	return database.DB.Model(&video).Association("Tags").Delete(&tag)
}

func setManualAutomaticVideoTag(videoID uint, tag models.Tag, present bool) error {
	if tag.AutomaticKind != shortVideoAutomaticTagKind && tag.AutomaticKind != lowResolutionAutomaticTagKind {
		return fmt.Errorf("该自动标签不能手动修改")
	}
	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&models.Video{}, videoID).Error; err != nil {
			return err
		}
		if err := tx.First(&models.Tag{}, tag.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO video_automatic_tag_overrides(video_id, automatic_kind, present)
			VALUES (?, ?, ?) ON CONFLICT(video_id, automatic_kind) DO UPDATE SET present = excluded.present`, videoID, tag.AutomaticKind, present).Error; err != nil {
			return err
		}
		if present {
			return tx.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING", videoID, tag.ID).Error
		}
		return tx.Exec("DELETE FROM video_tags WHERE video_id = ? AND tag_id = ?", videoID, tag.ID).Error
	})
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
