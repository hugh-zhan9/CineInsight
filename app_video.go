package main

import (
	"log"
	"os"
	"video-master/models"
	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func homeDirForIINA() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// SyncIINAProgress 把 IINA 的播放断点同步进片库。外部播放器只记得播放次数，
// 进度这一环靠它补上。
func (a *App) SyncIINAProgress() (services.IINAProgressSyncResult, error) {
	result, err := a.iinaProgress.Sync()
	log.Printf("API SyncIINAProgress scanned=%d updated=%d err=%v", result.Scanned, result.Updated, err)
	return result, err
}

// GetIINAProgressAvailable 表示这台机器上有没有 IINA 的断点记录可读。
func (a *App) GetIINAProgressAvailable() bool {
	return a.iinaProgress.Available()
}

func (a *App) SelectMigrationSourceDirectory() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择要迁移的文件夹",
	})
}

func (a *App) SelectMigrationDestinationDirectory() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择迁移目标文件夹",
	})
}

func (a *App) SelectFolderToRename() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择要重命名的文件夹",
	})
}

// ScanDirectory 扫描目录
func (a *App) ScanDirectory(dir string) ([]string, error) {
	files, err := a.videoService.ScanDirectory(dir)
	log.Printf("API ScanDirectory dir=%s result=%d err=%v", dir, len(files), err)
	return files, err
}

// ScanDirectoryWithProgress scopes events to this invocation, so late events
// or other scans cannot overwrite the dialog's current discovery state.
func (a *App) ScanDirectoryWithProgress(dir, requestID string) ([]string, error) {
	log.Printf("API ScanDirectoryWithProgress begin dir=%s", dir)
	files, err := a.videoService.ScanDirectoryWithProgress(dir, func(progress services.DirectoryScanProgress) {
		runtime.EventsEmit(a.ctx, "directory-scan-progress", struct {
			services.DirectoryScanProgress
			RequestID string `json:"request_id"`
		}{progress, requestID})
	})
	log.Printf("API ScanDirectoryWithProgress end dir=%s result=%d err=%v", dir, len(files), err)
	return files, err
}

// SyncDirectoryWithProgress runs manual discovery and reconciliation in the backend.
func (a *App) SyncDirectoryWithProgress(dir, requestID string) *services.ScanSyncResult {
	result := a.videoService.SyncDirectoryWithProgress(dir, func(progress services.DirectoryScanProgress) {
		runtime.EventsEmit(a.ctx, "directory-scan-progress", struct {
			services.DirectoryScanProgress
			RequestID string `json:"request_id"`
		}{progress, requestID})
	})
	log.Printf("API SyncDirectoryWithProgress dir=%s added=%d restored=%d deleted=%d stale=%d errors=%d", dir, result.Added, result.Restored, result.Deleted, result.Stale, len(result.Errors))
	if a.cleanupService != nil {
		a.cleanupService.InvalidateAnalysis()
	}
	if result.Added > 0 && a.aiTaggingService != nil {
		go a.triggerAITaggingAuto("scan")
	}
	a.runPostScanAutomation(result)
	return result
}

// ScanDirectoryWithInfo 扫描目录（附带文件大小，用于迁移检测）
func (a *App) ScanDirectoryWithInfo(dir string) ([]services.ScannedFile, error) {
	files, err := a.videoService.ScanDirectoryWithInfo(dir)
	log.Printf("API ScanDirectoryWithInfo dir=%s result=%d err=%v", dir, len(files), err)
	return files, err
}

// RelocateVideo 更新视频路径（文件迁移，保留标签等元数据）
func (a *App) RelocateVideo(id uint, newPath string) error {
	err := a.videoService.RelocateVideo(id, newPath)
	log.Printf("API RelocateVideo id=%d newPath=%s err=%v", id, newPath, err)
	return err
}

func (a *App) MoveVideo(id uint, destinationDirectory string) (*services.FileMigrationResult, error) {
	result, err := a.videoService.MoveVideo(id, destinationDirectory)
	log.Printf("API MoveVideo id=%d destination=%s err=%v", id, destinationDirectory, err)
	return result, err
}

func (a *App) BatchMoveVideos(videoIDs []uint, destinationDirectory string) *services.BatchVideoOperationResult {
	result := a.videoService.BatchMoveVideos(videoIDs, destinationDirectory)
	log.Printf("API BatchMoveVideos requested=%d succeeded=%d failed=%d destination=%s", result.Requested, result.Succeeded, result.Failed, destinationDirectory)
	return result
}

func (a *App) MoveDirectory(sourceDirectory, destinationParent string) (*services.FolderMigrationResult, error) {
	result, err := a.videoService.MoveDirectory(sourceDirectory, destinationParent)
	log.Printf("API MoveDirectory source=%s destinationParent=%s result=%+v err=%v", sourceDirectory, destinationParent, result, err)
	return result, err
}

func (a *App) RenameDirectory(sourceDirectory, newName string) (*services.FolderMigrationResult, error) {
	result, err := a.videoService.RenameDirectory(sourceDirectory, newName)
	if err == nil {
		a.reconfigureLibraryWatcher()
	}
	log.Printf("API RenameDirectory source=%s newName=%s result=%+v err=%v", sourceDirectory, newName, result, err)
	return result, err
}

// RefreshVideoMetadata 刷新并补全视频元数据 (时长/分辨率)
func (a *App) RefreshVideoMetadata(id uint) error {
	return a.videoService.RefreshVideoMetadata(id)
}

func (a *App) BatchRefreshVideoMetadata(videoIDs []uint) *services.BatchVideoOperationResult {
	result := a.videoService.BatchRefreshVideoMetadata(videoIDs)
	log.Printf("API BatchRefreshVideoMetadata requested=%d succeeded=%d failed=%d", result.Requested, result.Succeeded, result.Failed)
	return result
}

// RenameVideo 重命名视频文件及数据库记录
func (a *App) RenameVideo(id uint, newName string) error {
	err := a.videoService.RenameVideo(id, newName)
	log.Printf("API RenameVideo id=%d newName=%s err=%v", id, newName, err)
	return err
}

// AddVideo 添加视频
func (a *App) AddVideo(path string) (*models.Video, error) {
	video, err := a.videoService.AddVideo(path)
	if err == nil && video != nil && a.cleanupService != nil {
		a.cleanupService.InvalidateAnalysis()
	}
	if video != nil {
		log.Printf("API AddVideo path=%s id=%d err=%v", path, video.ID, err)
	} else {
		log.Printf("API AddVideo path=%s id=0 err=%v", path, err)
	}
	return video, err
}

// GetVideosByDirectory 按目录获取视频记录
func (a *App) GetVideosByDirectory(dir string) ([]models.Video, error) {
	videos, err := a.videoService.GetVideosByDirectory(dir)
	log.Printf("API GetVideosByDirectory dir=%s result=%d err=%v sample=%s", dir, len(videos), err, summarizeVideos(videos, 3))
	return videos, err
}

// DeleteVideo 删除视频
func (a *App) DeleteVideo(id uint, deleteFile bool) error {
	err := a.videoService.DeleteVideo(id, deleteFile)
	if err == nil {
		a.cleanupService.InvalidateAnalysis()
	}
	log.Printf("API DeleteVideo id=%d deleteFile=%v err=%v", id, deleteFile, err)
	return err
}

func (a *App) BatchDeleteVideos(videoIDs []uint, deleteFile bool) *services.BatchVideoOperationResult {
	result := a.videoService.BatchDeleteVideos(videoIDs, deleteFile)
	if result.Succeeded > 0 {
		a.cleanupService.InvalidateAnalysis()
	}
	log.Printf("API BatchDeleteVideos requested=%d succeeded=%d failed=%d deleteFile=%v", result.Requested, result.Succeeded, result.Failed, deleteFile)
	return result
}

// ListTrashEntries 返回当前可恢复的视频删除记录。
func (a *App) ListTrashEntries() ([]models.VideoTrashEntry, error) {
	entries, err := a.videoService.ListTrashEntries()
	log.Printf("API ListTrashEntries result=%d err=%v", len(entries), err)
	return entries, err
}

// RestoreTrashEntry 将一个视频恢复到删除前的路径。
func (a *App) RestoreTrashEntry(entryID uint) (*models.Video, error) {
	video, err := a.videoService.RestoreTrashEntry(entryID)
	if err == nil {
		a.cleanupService.InvalidateAnalysis()
	}
	log.Printf("API RestoreTrashEntry entryID=%d err=%v", entryID, err)
	return video, err
}

// OpenDirectory 打开文件所在目录
func (a *App) OpenDirectory(videoID uint) error {
	return a.videoService.OpenDirectory(videoID)
}

// GetPreviewSession 获取视频预览 session
func (a *App) GetPreviewSession(videoID uint) (*services.PreviewSession, error) {
	session, err := a.videoService.GetPreviewSession(videoID)
	if err != nil {
		log.Printf("API GetPreviewSession id=%d err=%v", videoID, err)
		return nil, err
	}
	log.Printf("API GetPreviewSession id=%d mode=%s", videoID, session.Mode)
	return session, nil
}

// PreviewExternally 使用系统播放器执行统计中立的外部预览
func (a *App) PreviewExternally(videoID uint) error {
	err := a.videoService.PreviewExternally(videoID)
	log.Printf("API PreviewExternally id=%d err=%v", videoID, err)
	return err
}

// PlayVideo 发起正式播放
func (a *App) PlayVideo(videoID uint) (*services.PlaybackAttemptResult, error) {
	result, err := a.videoService.PlayVideo(videoID)
	if result != nil {
		log.Printf("API PlayVideo id=%d dispatch=%v reason=%s err=%v", videoID, result.DispatchSucceeded, result.ReasonCode, err)
	} else {
		log.Printf("API PlayVideo id=%d dispatch=false reason=<nil> err=%v", videoID, err)
	}
	return result, err
}

// PlayRandomVideo 随机发起正式播放
func (a *App) PlayRandomVideo() (*services.PlaybackAttemptResult, error) {
	result, err := a.videoService.PlayRandomVideo()
	if result != nil && result.Video != nil {
		log.Printf("API PlayRandomVideo id=%d dispatch=%v reason=%s err=%v", result.Video.ID, result.DispatchSucceeded, result.ReasonCode, err)
	} else {
		log.Printf("API PlayRandomVideo id=0 dispatch=false err=%v", err)
	}
	return result, err
}

// PlayRandomVideoWithFilter 在当前片库筛选范围内发起随机播放。
func (a *App) PlayRandomVideoWithFilter(request services.RandomPlayRequest) (*services.PlaybackAttemptResult, error) {
	result, err := a.videoService.PlayRandomVideoWithFilter(request)
	if result != nil {
		log.Printf("API PlayRandomVideoWithFilter mode=%s dispatch=%v reason=%s err=%v", request.Mode, result.DispatchSucceeded, result.ReasonCode, err)
	} else {
		log.Printf("API PlayRandomVideoWithFilter mode=%s result=nil err=%v", request.Mode, err)
	}
	return result, err
}

// PickRandomVideos 在当前片库筛选范围内随机抽取若干视频，只返回列表不发起播放。
func (a *App) PickRandomVideos(request services.RandomPlayRequest, count int) (*services.RandomPickResult, error) {
	result, err := a.videoService.PickRandomVideos(request, count)
	if result != nil {
		log.Printf("API PickRandomVideos mode=%s count=%d picked=%d reason=%s err=%v", request.Mode, count, len(result.Videos), result.ReasonCode, err)
	} else {
		log.Printf("API PickRandomVideos mode=%s count=%d result=nil err=%v", request.Mode, count, err)
	}
	return result, err
}

// GetVideosByIDs 按给定顺序刷新这些视频的最新记录，查不到的条目会被跳过。
func (a *App) GetVideosByIDs(ids []uint) ([]models.Video, error) {
	return a.videoService.GetVideosByIDs(ids)
}

// AddTagToVideo 为视频添加标签
func (a *App) AddTagToVideo(videoID uint, tagID uint) error {
	err := a.videoService.AddTagToVideo(videoID, tagID)
	log.Printf("API AddTagToVideo videoID=%d tagID=%d err=%v", videoID, tagID, err)
	return err
}

func (a *App) BatchAddTagToVideos(videoIDs []uint, tagID uint) *services.BatchVideoOperationResult {
	result := a.videoService.BatchAddTagToVideos(videoIDs, tagID)
	log.Printf("API BatchAddTagToVideos requested=%d succeeded=%d failed=%d tagID=%d", result.Requested, result.Succeeded, result.Failed, tagID)
	return result
}

// RemoveTagFromVideo 移除视频标签
func (a *App) RemoveTagFromVideo(videoID uint, tagID uint) error {
	err := a.videoService.RemoveTagFromVideo(videoID, tagID)
	log.Printf("API RemoveTagFromVideo videoID=%d tagID=%d err=%v", videoID, tagID, err)
	return err
}

// SetVideoFavorite 更新主片库收藏状态。
func (a *App) SetVideoFavorite(videoID uint, favorite bool) (*models.Video, error) {
	video, err := a.videoService.SetVideoFavorite(videoID, favorite)
	log.Printf("API SetVideoFavorite video_id=%d favorite=%v err=%v", videoID, favorite, err)
	return video, err
}

// SetVideoWatched 更新主片库已看状态。
func (a *App) SetVideoWatched(videoID uint, watched bool) (*models.Video, error) {
	video, err := a.videoService.SetVideoWatched(videoID, watched)
	log.Printf("API SetVideoWatched video_id=%d watched=%v err=%v", videoID, watched, err)
	return video, err
}

// UpdateVideoWatchProgress 保存内嵌播放器观看位置。
func (a *App) UpdateVideoWatchProgress(videoID uint, positionSeconds float64, completed bool) (*models.Video, error) {
	video, err := a.videoService.UpdateVideoWatchProgress(videoID, positionSeconds, completed)
	log.Printf("API UpdateVideoWatchProgress video_id=%d completed=%v err=%v", videoID, completed, err)
	return video, err
}

func (a *App) BatchRemoveTagFromVideos(videoIDs []uint, tagID uint) *services.BatchVideoOperationResult {
	result := a.videoService.BatchRemoveTagFromVideos(videoIDs, tagID)
	log.Printf("API BatchRemoveTagFromVideos requested=%d succeeded=%d failed=%d tagID=%d", result.Requested, result.Succeeded, result.Failed, tagID)
	return result
}

// ===== 兼容性转封装代理（D-001..D-006）=====

// CreatePlaybackProxy 为一个视频生成播放代理（详情抽屉与行菜单入口）。
func (a *App) CreatePlaybackProxy(videoID uint) (services.PlaybackProxyStatus, error) {
	status, err := a.playbackProxies.CreatePlaybackProxy(a.backgroundContext(), videoID)
	log.Printf("API CreatePlaybackProxy video_id=%d queued=%d err=%v", videoID, status.Queued, err)
	return status, err
}

// BatchCreatePlaybackProxies 批量生成代理（批量操作栏入口）。单 worker FIFO，逐项出结果。
func (a *App) BatchCreatePlaybackProxies(videoIDs []uint) (services.PlaybackProxyStatus, error) {
	status, err := a.playbackProxies.BatchCreatePlaybackProxies(a.backgroundContext(), videoIDs)
	log.Printf("API BatchCreatePlaybackProxies requested=%d total=%d err=%v", len(videoIDs), status.Total, err)
	return status, err
}

// BatchCreatePlaybackProxiesForFilter 为当前筛选命中的视频生成代理（结果条管理菜单入口）。
// 与「当前筛选写出 NFO」同一套路：筛选在后端解析成 ID 列表，不把上万个 ID 搬过绑定。
func (a *App) BatchCreatePlaybackProxiesForFilter(filter services.LibraryFilter) (services.PlaybackProxyStatus, error) {
	status, err := a.playbackProxies.BatchCreatePlaybackProxiesForFilter(a.backgroundContext(), filter)
	log.Printf("API BatchCreatePlaybackProxiesForFilter total=%d err=%v", status.Total, err)
	return status, err
}

// CancelPlaybackProxyTask 取消当前这一轮代理任务。
func (a *App) CancelPlaybackProxyTask() error {
	err := a.playbackProxies.CancelPlaybackProxyTask()
	log.Printf("API CancelPlaybackProxyTask err=%v", err)
	return err
}

// GetPlaybackProxyStatus 返回代理任务状态（playback-proxy-state 事件推同一份）。
func (a *App) GetPlaybackProxyStatus() services.PlaybackProxyStatus {
	return a.playbackProxies.Status()
}

// GetPlaybackProxy 返回单个视频当前的代理元数据；没有则为 nil。
func (a *App) GetPlaybackProxy(videoID uint) (*services.PlaybackProxyView, error) {
	return a.playbackProxies.GetPlaybackProxy(videoID)
}

// DeletePlaybackProxy 删除一个视频的代理（详情抽屉「删除此代理」）。
func (a *App) DeletePlaybackProxy(videoID uint) error {
	err := a.playbackProxies.DeletePlaybackProxy(videoID)
	log.Printf("API DeletePlaybackProxy video_id=%d err=%v", videoID, err)
	return err
}

// ClearPlaybackProxies 清空全部代理（设置页「清空全部」）。
func (a *App) ClearPlaybackProxies() (services.PlaybackProxyUsage, error) {
	usage, err := a.playbackProxies.ClearPlaybackProxies()
	log.Printf("API ClearPlaybackProxies err=%v", err)
	return usage, err
}

// EnforcePlaybackProxyLimit 立即按上限做一次 LRU 整理（设置页「立即整理」）。
func (a *App) EnforcePlaybackProxyLimit() (services.PlaybackProxyUsage, error) {
	usage, err := a.playbackProxies.EnforcePlaybackProxyLimit()
	log.Printf("API EnforcePlaybackProxyLimit total_bytes=%d count=%d err=%v", usage.TotalBytes, usage.Count, err)
	return usage, err
}

// GetPlaybackProxyUsage 返回代理占用、数量与上限（上限 0 表示不限）。
func (a *App) GetPlaybackProxyUsage() (services.PlaybackProxyUsage, error) {
	return a.playbackProxies.GetPlaybackProxyUsage()
}
