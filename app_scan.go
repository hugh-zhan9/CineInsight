package main

import (
	"log"
	"video-master/models"
	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ===== 扫描、可见性与迁移残留（产品完善度 P-011） =====

// emitLibraryScanSummary 在扫描完成后发 library-scan-summary 事件（D-PC09）。
// trigger 取 services.ScanTriggerStartup / ScanTriggerDirectoryChange / ScanTriggerManual。
// 前端在 added + removed + restored + stale_marked > 0 时重载列表与头部计数。
func (a *App) emitLibraryScanSummary(trigger string, result *services.ScanSyncResult) {
	if result == nil || a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "library-scan-summary", services.LibraryScanSummaryEvent{Trigger: trigger, Result: result})
}

// ValidateScanDirectory 在把目录加入扫描根之前预检：是否存在、是否重复、是否与已有根嵌套。
func (a *App) ValidateScanDirectory(path string) (*services.ScanDirectoryValidation, error) {
	return a.directoryService.ValidateScanDirectory(path)
}

// CheckMoveTarget 预检迁移目标是否落在扫描根之内。
func (a *App) CheckMoveTarget(targetDir string) (*services.MoveTargetCheck, error) {
	return a.videoService.CheckMoveTarget(targetDir)
}

// RecheckVideos 对失效视频所在目录做窄对账，文件回来的会自动恢复。
func (a *App) RecheckVideos(ids []uint) (services.LibraryReconcileSummary, error) {
	summary, err := a.videoService.RecheckVideos(ids)
	log.Printf("API RecheckVideos ids=%d restored=%d relocated=%d stale=%d errors=%d err=%v",
		len(ids), summary.Restored, summary.Relocated, summary.Stale, summary.ErrorCount, err)
	if err == nil && a.cleanupService != nil && (summary.Added > 0 || summary.Restored > 0 || summary.Relocated > 0) {
		a.cleanupService.InvalidateAnalysis()
	}
	return summary, err
}

// ReaddRemovedRoot 返回视频原来所属、后来被移除的扫描根，供前端预填「添加扫描目录」。
func (a *App) ReaddRemovedRoot(videoID uint) (string, error) {
	return a.videoService.ReaddRemovedRoot(videoID)
}

// ListStagedSources 列出跨盘迁移留下、仍待处理的暂存源文件。
func (a *App) ListStagedSources() ([]models.MigrationStagedSource, error) {
	return a.videoService.ListStagedSources()
}

// DeleteStagedSources 永久删除指定的迁移残留。
func (a *App) DeleteStagedSources(ids []uint) (*services.StagedSourceDeleteResult, error) {
	return a.videoService.DeleteStagedSources(ids)
}
