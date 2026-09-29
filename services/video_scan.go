package services

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

const (
	recentActiveFileThreshold = 5 * time.Minute
	trashStatePendingMove     = "pending_move"
	trashStateDeleted         = "deleted"
	trashStateRestoring       = "restoring"
	trashStateRollback        = "rollback"
)

// defaultVideoExtensions 是「视频扩展名」设置为空时使用的默认集合。
const defaultVideoExtensions = ".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt"

var tempVideoStemSuffixes = []string{
	".temp", "_temp", "-temp",
	".tmp", "_tmp", "-tmp",
}

type ScanSyncError struct {
	Operation string `json:"operation"`
	Directory string `json:"directory,omitempty"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error"`
}

type ScanSyncResult struct {
	Directories int `json:"directories"`
	Scanned     int `json:"scanned"`
	Added       int `json:"added"`
	Deleted     int `json:"deleted"`
	Stale       int `json:"stale"`
	// Restored 是这一轮把 is_stale 清掉的条数（文件重新出现）。删掉扫描目录后
	// 记录会被标失效，把同一路径加回来时就靠这个计数让界面知道"数据回来了"，
	// 否则一轮只做恢复的扫描在计数上全是 0，前端不会刷新列表。
	Restored          int `json:"restored"`
	Relocated         int `json:"relocated"`
	MetadataRefreshed int `json:"metadata_refreshed"`
	// Skipped 是 SkipBreakdown 各项之和，保留给只认总数的旧调用方。
	Skipped int `json:"skipped"`
	// SkipBreakdown 把「跳过」拆成用户能据此行动的原因（D-PC09）。
	SkipBreakdown SkipBreakdown   `json:"skip_breakdown"`
	Errors        []ScanSyncError `json:"errors"`
	// AddedVideoIDs 是本次新增的视频 ID。扫描后自动化要按"本次新增"下手
	// （D-006 的代理候选就只取这一批），光有计数说不出是哪几条。
	AddedVideoIDs []uint `json:"added_video_ids"`
}

// SkipBreakdown 是扫描跳过原因的分项计数（D-PC09）。JSON 键固定，前端按键取值。
type SkipBreakdown struct {
	// Existing：记录已存在（含扫得到但不属于本轮候选的在库文件）。
	Existing int `json:"existing"`
	// BlockedUserDelete：用户只删过记录、文件身份未变，扫描不重新收录。
	BlockedUserDelete int `json:"blocked_user_delete"`
	// RecentlyModified：5 分钟内修改过，可能仍在写入。
	RecentlyModified int `json:"recently_modified"`
	// TempFile：文件名带 -temp / -tmp 等临时后缀。
	TempFile int `json:"temp_file"`
	// NotVideo：扩展名与视频相同但内容是源码等非视频文件。
	NotVideo int `json:"not_video"`
	// ReadError：读取失败，已同时写入 Errors。
	ReadError int `json:"read_error"`
	// LegacyTrash：旧版应用留在 <媒体目录>/trash/ 里的、没有回收站条目的文件，扫描跳过不收录。
	LegacyTrash int `json:"legacy_trash"`
}

// Total 返回各项之和。
func (b SkipBreakdown) Total() int {
	return b.Existing + b.BlockedUserDelete + b.RecentlyModified + b.TempFile + b.NotVideo + b.ReadError + b.LegacyTrash
}

func (b *SkipBreakdown) merge(other SkipBreakdown) {
	b.Existing += other.Existing
	b.BlockedUserDelete += other.BlockedUserDelete
	b.RecentlyModified += other.RecentlyModified
	b.TempFile += other.TempFile
	b.NotVideo += other.NotVideo
	b.ReadError += other.ReadError
	b.LegacyTrash += other.LegacyTrash
}

// 跳过原因，与 SkipBreakdown 的字段一一对应。
const (
	skipReasonExisting          = "existing"
	skipReasonBlockedUserDelete = "blocked_user_delete"
	skipReasonRecentlyModified  = "recently_modified"
	skipReasonTempFile          = "temp_file"
	skipReasonNotVideo          = "not_video"
	skipReasonReadError         = "read_error"
	skipReasonLegacyTrash       = "legacy_trash"
)

// recordSkip 同时累加分项与总数，保证 Skipped == SkipBreakdown.Total()。
func (r *ScanSyncResult) recordSkip(reason string) {
	r.Skipped++
	switch reason {
	case skipReasonExisting:
		r.SkipBreakdown.Existing++
	case skipReasonBlockedUserDelete:
		r.SkipBreakdown.BlockedUserDelete++
	case skipReasonRecentlyModified:
		r.SkipBreakdown.RecentlyModified++
	case skipReasonTempFile:
		r.SkipBreakdown.TempFile++
	case skipReasonNotVideo:
		r.SkipBreakdown.NotVideo++
	case skipReasonLegacyTrash:
		r.SkipBreakdown.LegacyTrash++
	default:
		r.SkipBreakdown.ReadError++
	}
}

// addSkipBreakdown 并入遍历阶段收集的过滤计数。
func (r *ScanSyncResult) addSkipBreakdown(filtered SkipBreakdown) {
	r.SkipBreakdown.merge(filtered)
	r.Skipped += filtered.Total()
}

func (r *ScanSyncResult) recordError(operation, directory, path string, err error) {
	r.recordSkip(skipReasonReadError)
	r.Errors = append(r.Errors, ScanSyncError{
		Operation: operation,
		Directory: directory,
		Path:      path,
		Error:     err.Error(),
	})
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
	return s.scanDirectoryWithInfo(dir, true)
}

func (s *VideoService) scanDirectoryWithInfo(dir string, skipRecentlyActive bool) ([]ScannedFile, error) {
	return s.scanDirectoryWithProgress(dir, skipRecentlyActive, nil)
}

func (s *VideoService) scanDirectoryWithProgress(dir string, skipRecentlyActive bool, progress func(DirectoryScanProgress)) ([]ScannedFile, error) {
	return s.scanDirectoryCollect(dir, skipRecentlyActive, progress, nil)
}

// scanDirectoryCollect 是遍历的唯一实现。filtered 非空时，被过滤规则挡掉的视频类文件
// （临时后缀、5 分钟内修改、非视频源码）按原因累加进去，供扫描回报使用（D-PC09、LIB-14）。
func (s *VideoService) scanDirectoryCollect(dir string, skipRecentlyActive bool, progress func(DirectoryScanProgress), filtered *SkipBreakdown) ([]ScannedFile, error) {
	// 每一轮遍历开始时刷新旧版回收站目录集合，随后 isTrashPath 只读缓存。
	refreshLegacyTrashDirs()
	var videoFiles []ScannedFile
	reporter := directoryScanReporter{callback: progress}
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return nil, fmt.Errorf("扫描根目录为空")
	}
	reporter.update("checking", dir, true)
	rootInfo, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("扫描根目录不可用: %w", err)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("扫描根路径不是目录: %s", dir)
	}

	// 从设置中获取支持的视频格式
	reporter.update("settings", dir, true)
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}
	excludedPaths := parseScanExcludePaths(settings.ScanExcludePaths)
	if isScanPathExcluded(dir, excludedPaths) {
		log.Printf("跳过黑名单扫描目录 dir=%s", dir)
		return videoFiles, nil
	}

	// 解析视频格式
	videoExts := strings.Split(settings.VideoExtensions, ",")
	if len(videoExts) == 1 && strings.TrimSpace(videoExts[0]) == "" {
		videoExts = strings.Split(defaultVideoExtensions, ",")
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

	walk := filepath.Walk
	if progress != nil {
		walk = walkDirectoryInBatches
	}
	err = walk(dir, func(path string, info os.FileInfo, err error) error {
		if isScanPathExcluded(path, excludedPaths) {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Walk 在回调之前读取目录，隐藏/排除目录的读取失败仍应按原规则跳过。
		if info != nil && shouldSkipHiddenPath(info) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info != nil && info.IsDir() && isTrashDir(path) {
			if filtered != nil && isUnrecordedLegacyTrashDir(path) {
				filtered.LegacyTrash += countLegacyTrashVideos(path, videoExts)
			}
			return filepath.SkipDir
		}
		if err != nil {
			// 扫描范围内的读取失败不能当成空目录，必须阻止后续缺失对账。
			return err
		}
		if info.IsDir() {
			reporter.update("reading", path, true)
			return nil
		}
		reporter.state.Visited++
		defer func() { reporter.update("reading", reporter.state.CurrentPath, false) }()

		if isTrashPath(path) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		matched := false
		for _, videoExt := range videoExts {
			if ext == strings.ToLower(videoExt) {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		// 只有扩展名是视频的文件才谈得上「被跳过」；其他文件与扫描无关，不计数。
		if hasTempVideoSuffix(path) {
			if filtered != nil {
				filtered.TempFile++
			}
			return nil
		}
		if skipRecentlyActive && isRecentlyActiveFile(info) {
			if filtered != nil {
				filtered.RecentlyModified++
			}
			return nil
		}
		if isKnownNonVideoSourcePath(path) {
			if filtered != nil {
				filtered.NotVideo++
			}
			return nil
		}
		videoFiles = append(videoFiles, ScannedFile{Path: path, Size: info.Size()})
		reporter.state.Found = len(videoFiles)
		if len(videoFiles) == 1 {
			reporter.update("reading", reporter.state.CurrentPath, true)
		}

		return nil
	})
	reporter.update("reading", reporter.state.CurrentPath, true)
	log.Printf("扫描目录完成 dir=%s files=%d", dir, len(videoFiles))

	return videoFiles, err
}

func (s *VideoService) scanDirectoryForReconciliation(dir string, filtered *SkipBreakdown) ([]ScannedFile, error) {
	if s != nil && s.scanDirectoryWithOptions != nil {
		return s.scanDirectoryWithOptions(dir, false)
	}
	return s.scanDirectoryCollect(dir, false, nil, filtered)
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
	return s.syncScanDirectories(dirs, true, nil)
}

// SyncDirectoryWithProgress gives the manual dialog the same guarded deletion
// and restoration rules as startup scans, without touching other scan roots.
func (s *VideoService) SyncDirectoryWithProgress(dir string, progress func(DirectoryScanProgress)) *ScanSyncResult {
	return s.syncScanDirectories([]models.ScanDirectory{{Path: dir}}, false, progress)
}

func (s *VideoService) syncScanDirectories(dirs []models.ScanDirectory, reconcileOrphans bool, progress func(DirectoryScanProgress)) *ScanSyncResult {
	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()
	s.scanSyncMu.Lock()
	defer s.scanSyncMu.Unlock()

	result := &ScanSyncResult{Errors: make([]ScanSyncError, 0)}
	scannedByPath := make(map[string]ScannedFile)
	existingByPath := make(map[string]models.Video)
	roots := make([]string, 0, len(dirs))
	removalGuard := make(scanRemovalGuard)
	allExisting := make([]models.Video, 0)
	duplicateVideos := make([]models.Video, 0)
	var settings models.Settings
	excludedPaths := make([]string, 0)
	if err := database.DB.Select("scan_exclude_paths").First(&settings).Error; err != nil {
		result.recordError("load_scan_blacklist", "", "", err)
		return result
	} else {
		excludedPaths = parseScanExcludePaths(settings.ScanExcludePaths)
	}

	// configuredRoots 是"当前配置了哪些目录"，与 roots（本轮成功扫到的根）有意分开：
	// 移动硬盘没插时那个根扫不了、进不了 roots，但它仍然配置着，底下的记录不能
	// 因此被当成孤儿处理；离线根单独标失效，不进入软删除。
	configuredRoots := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		root := filepath.Clean(strings.TrimSpace(dir.Path))
		if root == "" || root == "." {
			result.recordError("scan", dir.Path, "", fmt.Errorf("扫描目录为空"))
			continue
		}
		configuredRoots = append(configuredRoots, root)
		result.Directories++

		if isScanPathExcluded(root, excludedPaths) {
			continue
		}
		if err := removalGuard.capture(root); err != nil {
			result.recordError("scan", root, "", err)
			if errors.Is(err, os.ErrNotExist) {
				s.markUnavailableScanRoot(root, result)
			}
			continue
		}
		var filtered SkipBreakdown
		scannedFiles, err := s.scanDirectoryCollect(root, true, progress, &filtered)
		if err != nil {
			result.recordError("scan", root, "", err)
			delete(removalGuard, root)
			if errors.Is(err, os.ErrNotExist) {
				if _, rootErr := os.Stat(root); errors.Is(rootErr, os.ErrNotExist) {
					s.markUnavailableScanRoot(root, result)
				}
			}
			continue
		}
		roots = append(roots, root)
		result.addSkipBreakdown(filtered)
		result.Scanned += len(scannedFiles)
		for _, file := range scannedFiles {
			scannedByPath[file.Path] = file
		}
	}

	reporter := directoryScanReporter{callback: progress}
	reporter.state.Found = result.Scanned
	reporter.update("reconciling", "", true)
	loadedExisting, err := s.getActiveVideosUnderRoots(roots)
	if err != nil {
		result.recordError("load_existing", "", "", err)
	} else {
		for _, video := range loadedExisting {
			if isScanPathExcluded(video.Path, excludedPaths) {
				continue
			}
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
			if scanCandidateIsMissing(video, result) {
				if missing, err := removalGuard.missing(video.Path, excludedPaths); err != nil {
					result.recordError("check_missing_root", video.Directory, video.Path, err)
					update := markVideoStale(video.ID, staleReasonForGuardError(err))
					if update.Error != nil {
						result.recordError("mark_stale", video.Directory, video.Path, update.Error)
					} else {
						result.Stale += int(update.RowsAffected)
					}
				} else if missing {
					missingVideos = append(missingVideos, video)
				}
			}
			continue
		}
		if video.IsStale {
			if err := clearVideoStale(video.ID); err != nil {
				result.recordError("clear_stale", video.Directory, video.Path, err)
			} else {
				video.IsStale = false
				result.Restored++
			}
		}
		needsRefresh, refreshCheckErr := s.needsTechnicalRefreshDuringScan(video, scannedByPath[video.Path])
		if refreshCheckErr != nil {
			result.recordError("check_metadata", video.Directory, video.Path, refreshCheckErr)
		}
		if needsRefresh {
			if err := s.RefreshVideoMetadata(video.ID); err != nil {
				result.recordError("refresh_metadata", video.Directory, video.Path, err)
			} else {
				result.MetadataRefreshed++
			}
		}
		s.observeLocalMetadata(video.ID, false)
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

	reporter.state.Total = len(newFiles) + len(missingVideos) + len(duplicateVideos)
	reportProcessing := func(path string, force bool) {
		reporter.state.Added, reporter.state.Restored = result.Added, result.Restored
		reporter.state.Deleted, reporter.state.Skipped = result.Deleted, result.Skipped
		reporter.update("processing", path, force)
	}
	reportProcessing("", true)
	for index, file := range newFiles {
		reporter.state.Processed = index
		reportProcessing(file.Path, false)
		if _, consumed := consumedNewPaths[file.Path]; consumed {
			continue
		}
		added, restored, err := s.addScannedVideo(file.Path)
		if err != nil {
			if reason, skipped := scanAddSkipReason(err); skipped {
				result.recordSkip(reason)
				continue
			}
			result.recordError("add", filepath.Dir(file.Path), file.Path, err)
			continue
		}
		if restored {
			result.Restored++
			continue
		}
		result.Added++
		if added != nil {
			result.AddedVideoIDs = append(result.AddedVideoIDs, added.ID)
		}
	}

	// Settings may have changed while a slow network traversal was running.
	if err := database.DB.Select("scan_exclude_paths").First(&settings).Error; err != nil {
		result.recordError("reload_scan_blacklist", "", "", err)
		return result
	}
	excludedPaths = parseScanExcludePaths(settings.ScanExcludePaths)
	for index, video := range append(duplicateVideos, missingVideos...) {
		reporter.state.Processed = len(newFiles) + index
		reportProcessing(video.Path, false)
		if _, relocated := relocatedVideoIDs[video.ID]; relocated {
			continue
		}
		missing, guardErr := removalGuard.missing(video.Path, excludedPaths)
		if guardErr != nil {
			result.recordError("check_delete", video.Directory, video.Path, guardErr)
			if update := markVideoStale(video.ID, staleReasonForGuardError(guardErr)); update.Error != nil {
				result.recordError("mark_stale", video.Directory, video.Path, update.Error)
			} else {
				result.Stale += int(update.RowsAffected)
			}
			continue
		}
		if !missing {
			continue
		}
		if err := s.deleteVideoBy(video.ID, false, "scanner"); err != nil {
			result.recordError("delete", video.Directory, video.Path, err)
			continue
		}
		result.Deleted++
	}

	reporter.state.Processed = reporter.state.Total
	reportProcessing("", true)
	for root := range removalGuard {
		if err := removalGuard.verify(root); err != nil {
			result.recordError("verify_root", root, "", err)
			s.markUnavailableScanRoot(root, result)
		}
	}
	if reconcileOrphans {
		s.reconcileOrphanedVideos(configuredRoots, result)
	}
	if err := database.Transaction(func(tx *gorm.DB) error { return syncShortVideoTags(tx) }); err != nil {
		result.recordError("short-video-tag", "", "", err)
	}

	log.Printf("增量扫描同步完成 dirs=%d scanned=%d added=%d relocated=%d deleted=%d stale=%d restored=%d refreshed=%d skipped=%d errors=%d",
		result.Directories, result.Scanned, result.Added, result.Relocated, result.Deleted, result.Stale, result.Restored, result.MetadataRefreshed, result.Skipped, len(result.Errors))
	return result
}

// markUnavailableScanRoot hides an offline root without entering the trash flow.
func (s *VideoService) markUnavailableScanRoot(root string, result *ScanSyncResult) {
	videos, err := s.getActiveVideosUnderRoots([]string{root})
	if err != nil {
		result.recordError("load_offline", root, "", err)
		return
	}
	for _, video := range videos {
		update := markVideoStale(video.ID, models.StaleReasonOfflineRoot)
		if update.Error != nil {
			result.recordError("mark_stale", root, video.Path, update.Error)
		} else {
			result.Stale += int(update.RowsAffected)
		}
	}
}

// SyncAffectedDirectories reconciles only stable watcher-affected subtrees.
// It deliberately marks disappeared paths stale instead of invoking the user's
// explicit delete workflow; a later event can restore or relocate the record.
func (s *VideoService) SyncAffectedDirectories(dirs []models.ScanDirectory, affected []string) *ScanSyncResult {
	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()
	s.scanSyncMu.Lock()
	defer s.scanSyncMu.Unlock()

	result := &ScanSyncResult{Errors: make([]ScanSyncError, 0)}
	roots := cleanScanRoots(dirs)
	targets, err := normalizeAffectedScanDirectories(roots, affected)
	if err != nil {
		result.recordError("validate_affected", "", "", err)
		return result
	}
	result.Directories = len(targets)

	scannedByPath := make(map[string]ScannedFile)
	successfulTargets := make([]string, 0, len(targets))
	for _, target := range targets {
		var filtered SkipBreakdown
		files, scanErr := s.scanDirectoryForReconciliation(target, &filtered)
		if scanErr != nil {
			result.recordError("scan_affected", target, "", scanErr)
			continue
		}
		successfulTargets = append(successfulTargets, target)
		result.addSkipBreakdown(filtered)
		result.Scanned += len(files)
		for _, file := range files {
			file.Path = filepath.Clean(file.Path)
			scannedByPath[file.Path] = file
		}
	}

	allExisting, loadErr := s.getActiveVideosUnderRoots(roots)
	if loadErr != nil {
		result.recordError("load_existing", "", "", loadErr)
		return result
	}
	existingByPath := make(map[string]models.Video, len(allExisting))
	for _, video := range allExisting {
		existingByPath[filepath.Clean(video.Path)] = video
	}

	missingCandidates := make([]models.Video, 0)
	missingIDs := make(map[uint]struct{})
	for _, video := range allExisting {
		path := filepath.Clean(video.Path)
		if !pathBelongsToAny(path, successfulTargets) {
			if video.IsStale {
				missingCandidates = append(missingCandidates, video)
				missingIDs[video.ID] = struct{}{}
			}
			continue
		}
		file, exists := scannedByPath[path]
		if !exists {
			if scanCandidateIsMissing(video, result) {
				missingCandidates = append(missingCandidates, video)
				missingIDs[video.ID] = struct{}{}
			}
			continue
		}
		if video.IsStale {
			if err := clearVideoStale(video.ID); err != nil {
				result.recordError("clear_stale", video.Directory, video.Path, err)
			} else {
				video.IsStale = false
				result.Restored++
			}
		}
		needsRefresh, refreshErr := s.needsTechnicalRefreshDuringNarrowScan(video, file)
		if refreshErr != nil {
			result.recordError("check_metadata", video.Directory, video.Path, refreshErr)
		} else if needsRefresh {
			if err := s.RefreshVideoMetadata(video.ID); err != nil {
				result.recordError("refresh_metadata", video.Directory, video.Path, err)
			} else {
				result.MetadataRefreshed++
			}
		}
		if err := ensureSubtitleIndexForVideo(video); err != nil {
			result.recordError("refresh_subtitle_index", video.Directory, video.Path, err)
		}
		s.observeLocalMetadata(video.ID, false)
	}

	newFiles := make([]ScannedFile, 0)
	for path, file := range scannedByPath {
		if _, exists := existingByPath[path]; !exists {
			newFiles = append(newFiles, file)
		}
	}
	sortScannedFiles(newFiles)

	relocatedIDs := make(map[uint]struct{})
	consumedPaths := make(map[string]struct{})
	// Pass 1 uses the same (name, size) identity rule as the full scan, so it may draw on
	// every recoverable record. Pass 2 matches on size alone to catch renames; that rule
	// is loose enough to hijack an unrelated record, so it is restricted to candidates
	// that lived in the directories this batch actually reconciled.
	matchWatcherRelocations(s, result, newFiles, missingCandidates, relocatedIDs, consumedPaths, true)
	localCandidates := make([]models.Video, 0, len(missingCandidates))
	for _, candidate := range missingCandidates {
		if pathBelongsToAny(filepath.Clean(candidate.Path), successfulTargets) {
			localCandidates = append(localCandidates, candidate)
		}
	}
	matchWatcherRelocations(s, result, newFiles, localCandidates, relocatedIDs, consumedPaths, false)

	for _, file := range newFiles {
		if _, consumed := consumedPaths[file.Path]; consumed {
			continue
		}
		video, restored, addErr := s.addScannedVideo(file.Path)
		if addErr != nil {
			if reason, skipped := scanAddSkipReason(addErr); skipped {
				result.recordSkip(reason)
				continue
			}
			result.recordError("add", filepath.Dir(file.Path), file.Path, addErr)
			continue
		}
		if restored {
			result.Restored++
			continue
		}
		result.Added++
		if video != nil {
			result.AddedVideoIDs = append(result.AddedVideoIDs, video.ID)
			if err := ensureSubtitleIndexForVideo(*video); err != nil {
				result.recordError("refresh_subtitle_index", video.Directory, video.Path, err)
			}
		}
	}

	for _, video := range missingCandidates {
		if _, relocated := relocatedIDs[video.ID]; relocated {
			continue
		}
		if _, affectedMissing := missingIDs[video.ID]; !affectedMissing {
			continue
		}
		if video.IsStale {
			// 根曾离线、如今回来了：在窄对账成功覆盖的子树里文件仍然不存在，
			// 失效原因就从「磁盘离线」改成「文件缺失」，否则重连后永远显示离线。
			if video.StaleReason == models.StaleReasonOfflineRoot && pathBelongsToAny(filepath.Clean(video.Path), successfulTargets) {
				if err := refineOfflineStaleToMissing(video.ID); err != nil {
					result.recordError("mark_stale", video.Directory, video.Path, err)
				}
			}
			continue
		}
		if err := markVideoStale(video.ID, models.StaleReasonMissingFile).Error; err != nil {
			result.recordError("mark_stale", video.Directory, video.Path, err)
			continue
		}
		result.Stale++
	}
	if err := database.Transaction(func(tx *gorm.DB) error { return syncShortVideoTags(tx) }); err != nil {
		result.recordError("short-video-tag", "", "", err)
	}
	return result
}

// scanCandidateIsMissing 区分磁盘丢失和扫描过滤；活跃窗口、黑名单等只限制扫描收录。
// 只有明确不存在的文件才能进入迁移、删除或标失效路径，权限/IO 失败保留原记录并报错。
func scanCandidateIsMissing(video models.Video, result *ScanSyncResult) bool {
	info, err := os.Stat(video.Path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		result.recordError("check_missing", video.Directory, video.Path, err)
	} else if !skippedDuringWalk(video.Path, info) {
		// 遍历阶段已经按原因计过数的（临时后缀、刚修改、非视频源码）不再重复计；
		// 其余是在库、文件也在、但不属于本轮遍历结果的记录。
		result.recordSkip(skipReasonExisting)
	}
	return false
}

// skippedDuringWalk 判断文件是否会被遍历阶段的过滤规则挡掉（这些规则在遍历时已计数）。
func skippedDuringWalk(path string, info os.FileInfo) bool {
	return hasTempVideoSuffix(path) || isRecentlyActiveFile(info) || isKnownNonVideoSourcePath(path)
}

// reconcileOrphanedVideos 处理"不属于任何已配置扫描目录"的记录。
//
// 这类记录从哪来：用户删掉过某个扫描目录（旧版本只删配置行、不动记录），或者把
// 目录改成了别的路径。而两条对账都只在配置目录之下取候选（getActiveVideosUnderRoots），
// 所以它们过去永远碰不到，记录就停在"扫描看不见、列表看得见"的夹缝里。
// 只在删除目录那一刻做处理是不够的——那修不了已经掉进夹缝的历史记录。
//
// 与 D-S01 同口径：标失效，不软删、不进回收站，磁盘文件不动。目录加回来时由
// 窄对账的 clear_stale 分支恢复。
//
// 一个目录都没配置时所有记录都是孤儿、全部失效——"没有扫描目录就不该有内容"。
// 调用方必须先确认目录清单读成功：读失败时绝不能走到这里，否则会把整库藏起来。
func (s *VideoService) reconcileOrphanedVideos(configuredRoots []string, result *ScanSyncResult) {
	var videos []models.Video
	if err := database.DB.Select("id", "path", "directory", "is_stale").
		Where("is_stale = ?", false).Find(&videos).Error; err != nil {
		result.recordError("load_orphans", "", "", err)
		return
	}
	orphanIDs := make([]uint, 0)
	for _, video := range videos {
		if len(configuredRoots) > 0 && videoBelongsToRoots(video, configuredRoots) {
			continue
		}
		orphanIDs = append(orphanIDs, video.ID)
	}
	if len(orphanIDs) == 0 {
		return
	}
	const batchSize = 500
	for start := 0; start < len(orphanIDs); start += batchSize {
		end := start + batchSize
		if end > len(orphanIDs) {
			end = len(orphanIDs)
		}
		update := markVideosStale(orphanIDs[start:end], models.StaleReasonOutsideRoots)
		if update.Error != nil {
			result.recordError("orphan_stale", "", "", update.Error)
			return
		}
		result.Stale += int(update.RowsAffected)
	}
	log.Printf("对账发现不属于任何扫描目录的记录，已标失效 count=%d", len(orphanIDs))
}

// MarkVideosStaleUnderRemovedRoot 把只属于被移除扫描根的活跃视频标成失效（D-S01）。
//
// 为什么是标失效而不是删：视频的软删走 deleteVideoRecord，每条都会建一个回收站条目，
// 删一个目录就往回收站灌上千条。标失效之后这批记录从默认列表消失（见 library_service.go
// 里的 D-S02 那一条）、留在「路径失效」视图里，标签与评分一个不丢；路径加回来时由扫描
// 的 clear_stale 分支把标记清掉，数据自动回来。磁盘文件全程不动。
//
// 嵌套目录是这里唯一的要害：/media 与 /media/movies 同时配着、用户删掉 /media 时，
// /media/movies 下的视频仍归另一个根管，不能跟着标失效。所以判断的是"属于被移除的根，
// 且不属于任何剩余的根"。
//
// 返回实际标记的条数。
func (s *VideoService) MarkVideosStaleUnderRemovedRoot(removedRoot string, remainingRoots []string) (int64, error) {
	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()
	return markVideosStaleUnderRemovedRoot(removedRoot, remainingRoots)
}

// markVideosStaleUnderRemovedRoot 是不加锁的实现，调用方负责持有 libraryPathMutationMu。
func markVideosStaleUnderRemovedRoot(removedRoot string, remainingRoots []string) (int64, error) {
	root := filepath.Clean(strings.TrimSpace(removedRoot))
	if root == "" || root == "." {
		return 0, fmt.Errorf("扫描目录为空")
	}
	remaining := make([]string, 0, len(remainingRoots))
	for _, candidate := range remainingRoots {
		cleaned := filepath.Clean(strings.TrimSpace(candidate))
		if cleaned == "" || cleaned == "." || cleaned == root {
			continue
		}
		remaining = append(remaining, cleaned)
	}

	candidates, err := activeVideosUnderRoots([]string{root})
	if err != nil {
		return 0, fmt.Errorf("读取该目录下的视频失败: %w", err)
	}

	ids := make([]uint, 0, len(candidates))
	for _, video := range candidates {
		if video.IsStale {
			continue
		}
		if len(remaining) > 0 && videoBelongsToRoots(video, remaining) {
			// 还归另一个仍在清单里的根管，保持原样。
			continue
		}
		ids = append(ids, video.ID)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	// 分批更新：一次 IN 里塞几千个参数在 SQLite 上会撞占位符上限。
	const batchSize = 500
	var marked int64
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		result := markVideosStale(ids[start:end], models.StaleReasonRemovedRoot)
		if result.Error != nil {
			return marked, fmt.Errorf("标记失效失败: %w", result.Error)
		}
		marked += result.RowsAffected
	}
	log.Printf("扫描目录移除，标记失效 root=%s marked=%d", root, marked)
	return marked, nil
}

func cleanScanRoots(dirs []models.ScanDirectory) []string {
	roots := make([]string, 0, len(dirs))
	seen := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		root := filepath.Clean(strings.TrimSpace(dir.Path))
		if root == "" || root == "." {
			continue
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return roots
}

func normalizeAffectedScanDirectories(roots, affected []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("no configured scan roots")
	}
	unique := make(map[string]struct{}, len(affected))
	for _, raw := range affected {
		path := filepath.Clean(strings.TrimSpace(raw))
		if path == "" || path == "." {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			path = filepath.Dir(path)
		}
		if !pathBelongsToAny(path, roots) {
			return nil, fmt.Errorf("affected path is outside configured roots: %s", path)
		}
		unique[path] = struct{}{}
	}
	if len(unique) == 0 {
		return nil, fmt.Errorf("no affected directories")
	}
	targets := make([]string, 0, len(unique))
	for path := range unique {
		targets = append(targets, path)
	}
	sort.Slice(targets, func(i, j int) bool {
		if len(targets[i]) == len(targets[j]) {
			return targets[i] < targets[j]
		}
		return len(targets[i]) < len(targets[j])
	})
	reduced := make([]string, 0, len(targets))
	for _, target := range targets {
		if pathBelongsToAny(target, reduced) {
			continue
		}
		reduced = append(reduced, target)
	}
	return reduced, nil
}

func pathBelongsToAny(path string, parents []string) bool {
	path = filepath.Clean(path)
	for _, parent := range parents {
		relative, err := filepath.Rel(filepath.Clean(parent), path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func matchWatcherRelocations(service *VideoService, result *ScanSyncResult, newFiles []ScannedFile, candidates []models.Video, relocatedIDs map[uint]struct{}, consumedPaths map[string]struct{}, exactName bool) {
	type matchKey struct {
		name string
		size int64
	}
	candidatesByKey := make(map[matchKey][]models.Video)
	filesByKey := make(map[matchKey][]ScannedFile)
	for _, candidate := range candidates {
		if _, used := relocatedIDs[candidate.ID]; used {
			continue
		}
		key := matchKey{size: candidate.Size}
		if exactName {
			key.name = candidate.Name
		}
		candidatesByKey[key] = append(candidatesByKey[key], candidate)
	}
	for _, file := range newFiles {
		if _, used := consumedPaths[file.Path]; used {
			continue
		}
		key := matchKey{size: file.Size}
		if exactName {
			key.name = filepath.Base(file.Path)
		}
		filesByKey[key] = append(filesByKey[key], file)
	}
	for key, matchingFiles := range filesByKey {
		matchingCandidates := candidatesByKey[key]
		if len(matchingFiles) != 1 || len(matchingCandidates) != 1 {
			continue
		}
		file := matchingFiles[0]
		video := matchingCandidates[0]
		// 跨目录的失效候选只在真实匹配时核验，避免每个本地事件都访问无关离线磁盘。
		if !scanCandidateIsMissing(video, result) {
			continue
		}
		if err := service.relocateVideo(video.ID, file.Path); err != nil {
			result.recordError("relocate", video.Directory, file.Path, err)
			continue
		}
		result.Relocated++
		relocatedIDs[video.ID] = struct{}{}
		consumedPaths[file.Path] = struct{}{}
		var updated models.Video
		if err := database.DB.First(&updated, video.ID).Error; err == nil {
			_ = ensureSubtitleIndexForVideo(updated)
			service.observeLocalMetadata(updated.ID, false)
		}
	}
}

func (s *VideoService) needsTechnicalRefreshDuringNarrowScan(video models.Video, scanned ScannedFile) (bool, error) {
	if scanned.Size != video.Size {
		return true, nil
	}
	var metadata models.VideoTechnicalMetadata
	err := database.DB.Select("video_id", "successful_source_size", "successful_source_mod_time_ns", "probed_at").
		First(&metadata, "video_id = ?", video.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if metadata.ProbedAt == nil || metadata.SuccessfulSourceSize == nil || metadata.SuccessfulSourceModTimeNS == nil {
		return true, nil
	}
	fingerprint, err := mediaProbeStat(video.Path)
	if err != nil {
		return true, nil
	}
	return fingerprint.size != *metadata.SuccessfulSourceSize || fingerprint.modTimeNS != *metadata.SuccessfulSourceModTimeNS, nil
}

func (s *VideoService) needsTechnicalRefreshDuringScan(video models.Video, scanned ScannedFile) (bool, error) {
	if video.Duration == 0 || video.Resolution == "" || video.Height == 0 || scanned.Size != video.Size {
		return true, nil
	}
	var metadata models.VideoTechnicalMetadata
	err := database.DB.Select("video_id", "successful_source_size", "successful_source_mod_time_ns", "probed_at").
		First(&metadata, "video_id = ?", video.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// A complete legacy base record is intentionally left for explicit backfill.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if metadata.ProbedAt == nil || metadata.SuccessfulSourceSize == nil || metadata.SuccessfulSourceModTimeNS == nil {
		return true, nil
	}
	fingerprint, err := mediaProbeStat(video.Path)
	if err != nil {
		return true, nil
	}
	return fingerprint.size != *metadata.SuccessfulSourceSize || fingerprint.modTimeNS != *metadata.SuccessfulSourceModTimeNS, nil
}

func (s *VideoService) getActiveVideosUnderRoots(roots []string) ([]models.Video, error) {
	return activeVideosUnderRoots(roots)
}

// activeVideosUnderRoots 不依赖服务实例，供只持有数据库的调用方（目录更新、路径改写）使用。
func activeVideosUnderRoots(roots []string) ([]models.Video, error) {
	if len(roots) == 0 {
		return []models.Video{}, nil
	}
	// Narrow in SQL so a reconciliation batch does not load the whole library. LIKE can
	// over-match on paths containing wildcards, so videoBelongsToRoots stays authoritative.
	query := database.DB.Model(&models.Video{})
	conditions := database.DB.Session(&gorm.Session{NewDB: true})
	for index, root := range roots {
		prefix := escapeSQLLikePrefix(scanRootChildPrefix(root)) + "%"
		clause := database.DB.Session(&gorm.Session{NewDB: true}).
			Where("directory = ?", root).
			Or(`directory LIKE ? ESCAPE '\'`, prefix).
			Or(`path LIKE ? ESCAPE '\'`, prefix)
		if index == 0 {
			conditions = conditions.Where(clause)
			continue
		}
		conditions = conditions.Or(clause)
	}
	var videos []models.Video
	if err := query.Where(conditions).Find(&videos).Error; err != nil {
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

// scanRootChildPrefix 返回"根下子路径"的前缀。filepath.Clean 会给卷根（"/"、
// Windows 的 "D:\\"）保留尾部分隔符，无脑再拼一个会得到 "//"，让任何真实路径
// 都匹配不上，整块盘做扫描根时整库都会消失。
// scanRootReachableForPath 判断某个媒体路径所属的扫描根现在是否可访问。用于把
// "文件真的没了"和"卷没挂载/权限被拦"区分开：后者绝不能拿来当删除库记录的依据。
// 找不到归属的扫描根时直接报错，不猜。
func scanRootReachableForPath(path string) (bool, string, error) {
	var dirs []models.ScanDirectory
	if err := database.DB.Find(&dirs).Error; err != nil {
		return false, "", fmt.Errorf("加载扫描目录失败: %w", err)
	}
	for _, root := range cleanScanRoots(dirs) {
		if path != root && !strings.HasPrefix(path, scanRootChildPrefix(root)) {
			continue
		}
		// 带挂载点检查：macOS 上弹出磁盘后 /Volumes 下会留下空目录，只 Stat 根会误判在线。
		return scanRootOnline(root), root, nil
	}
	return false, "", fmt.Errorf("视频 %q 不在任何已配置的扫描目录下，无法确认其所在位置是否可访问", path)
}

func scanRootChildPrefix(root string) string {
	separator := string(os.PathSeparator)
	if strings.HasSuffix(root, separator) {
		return root
	}
	return root + separator
}

func escapeSQLLikePrefix(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func videoBelongsToRoots(video models.Video, roots []string) bool {
	for _, root := range roots {
		prefix := scanRootChildPrefix(root)
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

// isTrashDirName 只按名字判断，保留给还没改成按路径判断的图片扫描；
// 视频扫描与监听一律用 isTrashDir / isTrashPath（LIB-14：用户自己名为 Trash 的目录要能被扫描）。
func isTrashDirName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), DefaultTrashDirName)
}

// 系统废纸篓目录名（macOS 卷上的 .Trash / .Trashes）。
func isSystemTrashSegment(name string) bool {
	return strings.EqualFold(name, ".Trash") || strings.EqualFold(name, ".Trashes")
}

// legacyTrashDirs 缓存旧版回收站目录集合：mode='legacy_trash' 的回收站条目里
// filepath.Dir(trash_path) 去重。绑定到 database.DB 实例，换库时自动失效；
// 每轮扫描开始时 refreshLegacyTrashDirs 重新读取。
//
// 集合里有两类目录：有 legacy_trash 条目的目录（值为 false），以及旧版应用（2026-04-16 到
// 2026-07-30）删除时移进去、却没建回收站条目的 <目录>/trash/（值为 true，见 loadLegacyTrashDirs）。
var legacyTrashDirs struct {
	mu   sync.RWMutex
	db   *gorm.DB
	dirs map[string]bool
}

// refreshLegacyTrashDirs 从两张回收站表重建集合。读取失败时保留旧缓存（换库除外），
// 因为「多跳过一个目录」比「把回收站里的文件当新视频收录」安全得多。
func refreshLegacyTrashDirs() {
	db := database.DB
	if db == nil {
		return
	}
	dirs, err := loadLegacyTrashDirs(db)
	legacyTrashDirs.mu.Lock()
	defer legacyTrashDirs.mu.Unlock()
	if err != nil {
		log.Printf("读取旧版回收站目录失败 err=%v", err)
		if legacyTrashDirs.db != db {
			legacyTrashDirs.db, legacyTrashDirs.dirs = db, nil
		}
		return
	}
	legacyTrashDirs.db, legacyTrashDirs.dirs = db, dirs
}

func loadLegacyTrashDirs(db *gorm.DB) (map[string]bool, error) {
	dirs := make(map[string]bool)
	// mode 为空且 file_moved=true 的行是回填之前的旧版条目，同样属于旧版回收站。
	const condition = "trash_path <> '' AND (mode = ? OR (mode = '' AND file_moved = ?))"
	for _, model := range []interface{}{&models.VideoTrashEntry{}, &models.ImageTrashEntry{}} {
		var paths []string
		if err := db.Model(model).Where(condition, models.TrashModeLegacyTrash, true).Distinct().Pluck("trash_path", &paths).Error; err != nil {
			return nil, err
		}
		for _, trashPath := range paths {
			dirs[filepath.Dir(filepath.Clean(trashPath))] = false
		}
	}
	// 旧版应用在这段时间里删除会把文件移进 <媒体目录>/trash/ 却不建条目（fd06f54、81fa75d）。
	// 目录基名恰为 trash（区分大小写），且其父目录是至少一条已软删视频或图片的所在目录，就认作旧版
	// 回收站目录；用户自己的 Trash（大写）或父目录从无删除记录的 trash 仍正常扫描（C1）。
	for _, source := range []struct {
		model  interface{}
		column string
	}{{&models.Video{}, "directory"}, {&models.Image{}, "directory"}} {
		var parents []string
		if err := db.Unscoped().Model(source.model).Where("deleted_at IS NOT NULL AND "+source.column+" <> ''").
			Distinct().Pluck(source.column, &parents).Error; err != nil {
			return nil, err
		}
		for _, parent := range parents {
			trashDir := filepath.Join(filepath.Clean(parent), DefaultTrashDirName)
			if _, known := dirs[trashDir]; !known {
				dirs[trashDir] = true
			}
		}
	}
	return dirs, nil
}

func snapshotLegacyTrashDirs() map[string]bool {
	legacyTrashDirs.mu.RLock()
	current := legacyTrashDirs.db == database.DB
	dirs := legacyTrashDirs.dirs
	legacyTrashDirs.mu.RUnlock()
	if current {
		return dirs
	}
	refreshLegacyTrashDirs()
	legacyTrashDirs.mu.RLock()
	defer legacyTrashDirs.mu.RUnlock()
	return legacyTrashDirs.dirs
}

// isUnrecordedLegacyTrashDir 报告 path 是否是没有条目的旧版 trash/ 目录（C1）。
func isUnrecordedLegacyTrashDir(path string) bool {
	return snapshotLegacyTrashDirs()[filepath.Clean(path)]
}

// countLegacyTrashVideos 统计旧版 trash/ 目录里的视频类文件（扩展名命中），供扫描回报的 legacy_trash 分项。
func countLegacyTrashVideos(dir string, videoExts []string) int {
	count := 0
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		for _, videoExt := range videoExts {
			if videoExt != "" && ext == strings.ToLower(videoExt) {
				count++
				break
			}
		}
		return nil
	})
	return count
}

// isTrashDir 判断目录本身是不是回收站：旧版回收站目录，或系统废纸篓目录名。
func isTrashDir(path string) bool {
	clean := filepath.Clean(path)
	if isSystemTrashSegment(filepath.Base(clean)) {
		return true
	}
	_, legacy := snapshotLegacyTrashDirs()[clean]
	return legacy
}

// isTrashPath 判断路径是否位于回收站内。只认旧版回收站目录集合与路径中的
// .Trash / .Trashes 段，不再按任意层级名为 trash 的目录判断。
func isTrashPath(path string) bool {
	cleanPath := filepath.Clean(path)
	volume := filepath.VolumeName(cleanPath)
	trimmed := strings.TrimPrefix(cleanPath, volume)
	for _, part := range strings.Split(trimmed, string(os.PathSeparator)) {
		if isSystemTrashSegment(part) {
			return true
		}
	}
	dirs := snapshotLegacyTrashDirs()
	if len(dirs) == 0 {
		return false
	}
	for current := cleanPath; ; {
		if _, legacy := dirs[current]; legacy {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
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
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	return isTypeScriptSource(file)
}

const typeScriptSampleLimit = 64 * 1024

func isTypeScriptSource(reader io.Reader) bool {
	// .ts also means MPEG transport stream. Never read an entire multi-GB video
	// just to exclude source code: this runs during discovery, including on SMB.
	data, err := io.ReadAll(io.LimitReader(reader, typeScriptSampleLimit))
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
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

// markVideoStale 把一条活跃视频标为失效并写入原因。条件更新：已失效的行不受影响，
// RowsAffected 只统计这次新标的。所有写 is_stale=true 的地方都走这里或 markVideosStale。
func markVideoStale(id uint, reason string) *gorm.DB {
	return database.DB.Model(&models.Video{}).
		Where("id = ? AND is_stale = ?", id, false).
		Updates(map[string]interface{}{"is_stale": true, "stale_reason": reason})
}

func markVideosStale(ids []uint, reason string) *gorm.DB {
	return database.DB.Model(&models.Video{}).
		Where("id IN ? AND is_stale = ?", ids, false).
		Updates(map[string]interface{}{"is_stale": true, "stale_reason": reason})
}

// clearVideoStale 清除失效标记，同时清空原因（is_stale=false 时原因必须为空）。
func clearVideoStale(id uint) error {
	return database.DB.Model(&models.Video{}).Where("id = ?", id).
		Updates(map[string]interface{}{"is_stale": false, "stale_reason": ""}).Error
}

// refineOfflineStaleToMissing 把「磁盘离线」的失效改成「文件缺失」；条件更新，不动其他原因。
func refineOfflineStaleToMissing(id uint) error {
	return database.DB.Model(&models.Video{}).
		Where("id = ? AND is_stale = ? AND stale_reason = ?", id, true, models.StaleReasonOfflineRoot).
		Update("stale_reason", models.StaleReasonMissingFile).Error
}

// staleReasonForGuardError 区分「根不可用」与「读取失败」：removalGuard 报告的根离线、
// 卷未挂载、根身份变化都带 errScanRootUnavailable，其余（例如权限）是读取失败。
func staleReasonForGuardError(err error) string {
	if errors.Is(err, errScanRootUnavailable) {
		return models.StaleReasonOfflineRoot
	}
	return models.StaleReasonReadError
}

// scanAddSkipReason 把 addScannedVideo 的「不算失败」结果映射成跳过原因。
func scanAddSkipReason(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrVideoBlockedByUserDelete):
		return skipReasonBlockedUserDelete, true
	case errors.Is(err, ErrVideoExists):
		return skipReasonExisting, true
	}
	return "", false
}

// scanRootOnline 判断扫描根当前是否可用：目录存在且（macOS 上）卷确实挂载。
func scanRootOnline(root string) bool {
	if scanVolumeAvailable(root) != nil {
		return false
	}
	info, err := os.Stat(root)
	return err == nil && info.IsDir()
}

// MarkRootOffline 把已判定不可用的扫描根下的活跃视频标为 offline_root（D-PC08）。
// 仍属于其他可用根的视频（嵌套根）保持原样。返回实际标记的条数。
//
// 由监听在根变为 unavailable 时调用；启动与全量扫描的离线判定走 markUnavailableScanRoot，
// 写同一个原因。根恢复后由窄对账清除（文件存在即恢复）。
func (s *VideoService) MarkRootOffline(root string) (int64, error) {
	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()

	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return 0, fmt.Errorf("扫描目录为空")
	}
	// 监听的判定与这里之间隔着队列与写锁，根可能已经回来了：执行前再确认一次，在线就什么都不标（Minor 12）。
	if scanRootOnline(root) {
		return 0, nil
	}
	var dirs []models.ScanDirectory
	if err := database.DB.Find(&dirs).Error; err != nil {
		return 0, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	others := make([]string, 0, len(dirs))
	for _, other := range cleanScanRoots(dirs) {
		if other != root && scanRootOnline(other) {
			others = append(others, other)
		}
	}
	candidates, err := activeVideosUnderRoots([]string{root})
	if err != nil {
		return 0, fmt.Errorf("读取该目录下的视频失败: %w", err)
	}
	ids := make([]uint, 0, len(candidates))
	for _, video := range candidates {
		if video.IsStale || (len(others) > 0 && videoBelongsToRoots(video, others)) {
			continue
		}
		ids = append(ids, video.ID)
	}
	var marked int64
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		update := markVideosStale(ids[start:end], models.StaleReasonOfflineRoot)
		if update.Error != nil {
			return marked, fmt.Errorf("标记离线失败: %w", update.Error)
		}
		marked += update.RowsAffected
	}
	if marked > 0 {
		log.Printf("扫描根离线，标记 offline_root root=%s marked=%d", root, marked)
	}
	return marked, nil
}

// RecheckVideos 对指定视频所在目录做一次窄对账（D-PC06）：目录去重后交给
// SyncAffectedDirectories，文件回来的清失效，仍不存在的按失效原因更新。
// 所在目录已不存在时向上取最近的仍存在的目录，且必须落在某个扫描根之内；
// 不在任何扫描根下的视频（outside_roots）无法窄对账，会被忽略。
func (s *VideoService) RecheckVideos(ids []uint) (LibraryReconcileSummary, error) {
	if len(ids) == 0 {
		return LibraryReconcileSummary{}, nil
	}
	var dirs []models.ScanDirectory
	if err := database.DB.Find(&dirs).Error; err != nil {
		return LibraryReconcileSummary{}, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	roots := cleanScanRoots(dirs)
	var videos []models.Video
	if err := database.DB.Select("id", "directory", "path").Where("id IN ?", ids).Find(&videos).Error; err != nil {
		return LibraryReconcileSummary{}, fmt.Errorf("读取视频失败: %w", err)
	}
	unique := make(map[string]struct{})
	for _, video := range videos {
		target := filepath.Clean(video.Directory)
		if !pathBelongsToAny(target, roots) {
			continue
		}
		for {
			if _, err := os.Stat(target); err == nil || !pathBelongsToAny(filepath.Dir(target), roots) {
				break
			}
			target = filepath.Dir(target)
		}
		unique[target] = struct{}{}
	}
	if len(unique) == 0 {
		return LibraryReconcileSummary{}, nil
	}
	affected := make([]string, 0, len(unique))
	for target := range unique {
		affected = append(affected, target)
	}
	sort.Strings(affected)
	summary := summarizeLibraryReconciliation(s.SyncAffectedDirectories(dirs, affected))
	if summary == nil {
		return LibraryReconcileSummary{}, nil
	}
	return *summary, nil
}

// ReaddRemovedRoot 返回视频原来所属、后来被移除的扫描根，供前端预填「添加扫描目录」。
// 扫描目录是软删的，据此找回包含该视频路径的、最长的那个已移除根；找不到就报错，
// 不去猜一个目录。
func (s *VideoService) ReaddRemovedRoot(videoID uint) (string, error) {
	var video models.Video
	if err := database.DB.Select("id", "path").First(&video, videoID).Error; err != nil {
		return "", fmt.Errorf("视频不存在: %w", err)
	}
	var removed []models.ScanDirectory
	if err := database.DB.Unscoped().Where("deleted_at IS NOT NULL").Find(&removed).Error; err != nil {
		return "", fmt.Errorf("读取已移除的扫描目录失败: %w", err)
	}
	best := ""
	for _, dir := range removed {
		root := filepath.Clean(strings.TrimSpace(dir.Path))
		if root == "" || root == "." || !pathBelongsToAny(video.Path, []string{root}) {
			continue
		}
		if len(root) > len(best) {
			best = root
		}
	}
	if best == "" {
		return "", fmt.Errorf("找不到该视频原来的扫描目录")
	}
	return best, nil
}

// 扫描完成事件（D-PC09）。事件由 App 层沿用 runtime.EventsEmit 发出，载荷在这里定义。
const (
	ScanTriggerStartup         = "startup"
	ScanTriggerDirectoryChange = "directory_change"
	ScanTriggerManual          = "manual"
)

// LibraryScanSummaryEvent 是 library-scan-summary 事件的载荷。
type LibraryScanSummaryEvent struct {
	Trigger string          `json:"trigger"`
	Result  *ScanSyncResult `json:"result"`
}
