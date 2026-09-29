package services

import (
	"context"
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
		"stale_reason":   "",
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
	result.Reason = playbackFailureReason(reasonCode, false)
	if shouldReconcile {
		result.ReconcileResult = s.reconcileAfterPlaybackFailure(video, reasonCode)
		result.Reason = result.ReconcileResult.Reason
		if result.Reason == playbackReasonOfflineRoot {
			result.UserMessage = fmt.Sprintf("播放失败: %s\n原因: 所在磁盘未连接，请连接后重试。", video.Name)
		}
	}
	return result
}

// 播放失败原因（PlaybackAttemptResult.reason 的取值，D-PC11）。
const (
	playbackReasonOfflineRoot = "offline_root"
	playbackReasonMissingFile = "missing_file"
	playbackReasonError       = "error"
)

// VideoRelocatedEvent 是后台重定位成功后发出的 video-relocated 事件载荷（D-PC11）。
type VideoRelocatedEvent struct {
	VideoID uint   `json:"video_id"`
	NewPath string `json:"new_path"`
}

var (
	// playbackRelocateSem 让后台重定位全局串行；playbackRelocateWG 让测试与关闭流程能等它们结束。
	playbackRelocateSem = make(chan struct{}, 1)
	playbackRelocateWG  sync.WaitGroup
	// playbackRelocateMu 保护下面三项，并让 WaitGroup 的 Add 与 StopPlaybackRelocation 的 Wait 不并发：
	// stopping 期间不再登记新的重定位（Minor 5）。
	playbackRelocateMu       sync.Mutex
	playbackRelocateCtx      context.Context
	playbackRelocateCancel   context.CancelFunc
	playbackRelocateStopping bool
)

// beginPlaybackRelocation 登记一次后台重定位：返回它要用的取消上下文。StopPlaybackRelocation 正在
// 等待时返回 false，调用方不启动 goroutine（Add 与 Wait 在同一把锁下互斥）。
func beginPlaybackRelocation() (context.Context, bool) {
	playbackRelocateMu.Lock()
	defer playbackRelocateMu.Unlock()
	if playbackRelocateStopping {
		return nil, false
	}
	if playbackRelocateCtx == nil {
		playbackRelocateCtx, playbackRelocateCancel = context.WithCancel(context.Background())
	}
	playbackRelocateWG.Add(1)
	return playbackRelocateCtx, true
}

// StopPlaybackRelocation 取消排队与进行中的后台重定位并等它们退出（应用退出时调用；接线项：
// App.shutdown）。先在锁内置「停止中」并取消上下文，之后不再有新的 Add，再 Wait；Wait 返回后
// 清除标志，此后再次发生的播放失败仍会启动新的重定位。
func StopPlaybackRelocation() {
	playbackRelocateMu.Lock()
	playbackRelocateStopping = true
	if playbackRelocateCancel != nil {
		playbackRelocateCancel()
	}
	playbackRelocateCtx, playbackRelocateCancel = nil, nil
	playbackRelocateMu.Unlock()
	playbackRelocateWG.Wait()
	playbackRelocateMu.Lock()
	playbackRelocateStopping = false
	playbackRelocateMu.Unlock()
}

var (
	videoRelocatedNotifierMu sync.RWMutex
	videoRelocatedNotifier   func(VideoRelocatedEvent)
	// playbackRelocating 记录正在后台重定位的视频，同一条不并发起多个遍历。
	playbackRelocating sync.Map
)

// SetVideoRelocatedNotifier 注入 video-relocated 事件的发送方。App 启动时设置为对
// runtime.EventsEmit 的调用；未设置时后台重定位照常完成，只是不发事件。
func SetVideoRelocatedNotifier(notifier func(VideoRelocatedEvent)) {
	videoRelocatedNotifierMu.Lock()
	videoRelocatedNotifier = notifier
	videoRelocatedNotifierMu.Unlock()
}

func notifyVideoRelocated(event VideoRelocatedEvent) {
	videoRelocatedNotifierMu.RLock()
	notifier := videoRelocatedNotifier
	videoRelocatedNotifierMu.RUnlock()
	if notifier != nil {
		notifier(event)
	}
}

// playbackVideoRootOffline 判断视频所在的扫描根现在是否离线：卷未挂载，或包含该路径的
// 扫描根都无法 Stat。找不到归属的根时按在线处理——那是「文件缺失」而不是「盘没插」。
func playbackVideoRootOffline(path string) bool {
	if mediaVolumeAvailable(path) != nil {
		return true
	}
	var dirs []models.ScanDirectory
	if err := database.DB.Find(&dirs).Error; err != nil {
		return false
	}
	containing := 0
	for _, root := range cleanScanRoots(dirs) {
		if !pathBelongsToAny(path, []string{root}) {
			continue
		}
		containing++
		if scanRootOnline(root) {
			return false
		}
	}
	return containing > 0
}

// playbackFailureReason 把失败归入 offline_root / missing_file / error。
func playbackFailureReason(reasonCode string, offline bool) string {
	switch {
	case offline:
		return playbackReasonOfflineRoot
	case reasonCode == "file_missing" || reasonCode == "path_is_directory":
		return playbackReasonMissingFile
	default:
		return playbackReasonError
	}
}

// reconcileAfterPlaybackFailure 立即返回（LIB-13、PLAY-12）：先标记失效并写原因，
// 磁盘离线时到此为止且不做重定位；根在线时把「是否被移动到别处」的遍历放到后台，
// 找到后由 relocateInBackground 改路径并发 video-relocated 事件。
func (s *VideoService) reconcileAfterPlaybackFailure(video *models.Video, reasonCode string) *PlaybackReconcileResult {
	result := &PlaybackReconcileResult{
		VideoID:    video.ID,
		ReasonCode: reasonCode,
	}
	offline := playbackVideoRootOffline(video.Path)
	reason := playbackFailureReason(reasonCode, offline)
	result.Reason = reason
	staleReason := models.StaleReasonMissingFile
	if offline {
		staleReason = models.StaleReasonOfflineRoot
	}
	if err := markVideoStale(video.ID, staleReason).Error; err == nil {
		video.IsStale = true
		video.StaleReason = staleReason
		result.DidMarkStale = true
	} else {
		log.Printf("播放失败标记失效失败 id=%d err=%v", video.ID, err)
	}
	if updatedVideo, loadErr := s.GetVideo(video.ID); loadErr == nil {
		result.UpdatedVideo = updatedVideo
	} else {
		result.NeedsReload = true
	}
	log.Printf("播放失败 id=%d code=%s reason=%s", video.ID, reasonCode, reason)
	if !offline {
		snapshot := *video
		if _, running := playbackRelocating.LoadOrStore(video.ID, struct{}{}); !running {
			// 上下文在派生 goroutine 之前取：StopPlaybackRelocation 取消的就是这一个，
			// 避免排队的 goroutine 启动得太晚、拿到取消之后新建的上下文而永远等不到信号量。
			if ctx, ok := beginPlaybackRelocation(); ok {
				go s.relocateInBackground(ctx, snapshot)
			} else {
				// 正在停止（应用退出）：不再启动新的遍历，记录保持失效。
				playbackRelocating.Delete(video.ID)
			}
		}
	}
	return result
}

// relocateInBackground 在所有在线扫描根里找同名同大小的唯一候选；找到就 RelocateVideo
// （同时清失效）并通知前端。任何一步失败或结果不唯一都只记日志，记录保持失效。
func (s *VideoService) relocateInBackground(ctx context.Context, video models.Video) {
	defer playbackRelocateWG.Done()
	defer playbackRelocating.Delete(video.ID)
	// 全局串行：多个视频同时播放失败时，全根遍历一次只跑一个，其余排队（Minor 8）。
	select {
	case playbackRelocateSem <- struct{}{}:
		defer func() { <-playbackRelocateSem }()
	case <-ctx.Done():
		return
	}
	if ctx.Err() != nil {
		return
	}
	matchedPath, ambiguous, err := s.findRelocatedVideoCandidate(ctx, &video)
	if err != nil {
		log.Printf("自动纠偏扫描失败 id=%d err=%v", video.ID, err)
		return
	}
	if ambiguous || matchedPath == "" || matchedPath == video.Path {
		return
	}
	if err := s.RelocateVideo(video.ID, matchedPath); err != nil {
		log.Printf("自动纠偏失败 id=%d err=%v", video.ID, err)
		return
	}
	notifyVideoRelocated(VideoRelocatedEvent{VideoID: video.ID, NewPath: matchedPath})
}

func (s *VideoService) findRelocatedVideoCandidate(ctx context.Context, video *models.Video) (string, bool, error) {
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
		// 离线的根遍历只会失败并拖慢整个搜索；文件不可能在一个不在线的根里被找到。
		if !scanRootOnline(root) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		// 遍历中途每个目录项都检查取消（Minor 5）：大盘全根遍历可能很久，退出时不能等它走完。
		scannedFiles, err := s.scanDirectoryCollectContext(ctx, root, true, nil, nil)
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
