package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// GetDatabaseBackendStatus 返回当前数据库后端、库位置与语义检索可用性。
func (a *App) GetDatabaseBackendStatus() services.DatabaseBackendStatus {
	return a.databaseSwitchService.Status()
}

// PreflightDatabaseSwitch 检查目标后端能否连接、是否为空。不写任何数据。
func (a *App) PreflightDatabaseSwitch(target string) (*services.DatabaseSwitchPreflight, error) {
	return a.databaseSwitchService.Preflight(target)
}

// StartDatabaseSwitch 迁移数据并写入后端配置；成功后需要重启才生效。
// 迁移在后台跑，进度走 database-switch-state 事件，GetDatabaseSwitchStatus 兜底。
func (a *App) StartDatabaseSwitch(target string) error {
	preflight, err := a.databaseSwitchService.Preflight(target)
	if err != nil {
		return err
	}
	if !preflight.Reachable || !preflight.Empty {
		return fmt.Errorf("%s", preflight.Message)
	}
	go func() {
		if err := a.databaseSwitchService.Switch(context.Background(), target); err != nil {
			log.Printf("API StartDatabaseSwitch target=%s err=%v", target, err)
		}
	}()
	return nil
}

// GetDatabaseSwitchStatus 返回迁移进度，供前端轮询兜底。
func (a *App) GetDatabaseSwitchStatus() services.DatabaseSwitchStatus {
	return a.databaseSwitchService.SwitchStatus()
}

// SelectDirectory 选择目录对话框
func (a *App) SelectDirectory() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择视频目录",
	})
	return dir, err
}

// ===== Settings Methods =====

// GetSettings 获取设置
func (a *App) GetSettings() (*models.Settings, error) {
	settings, err := a.settingsService.GetSettings()
	if err == nil {
		a.setLogEnabled(settings.LogEnabled)
	}
	log.Printf("API GetSettings err=%v value=%s", err, summarizeSettings(settings))
	return settings, err
}

// UpdateSettings 更新设置
func (a *App) UpdateSettings(input models.Settings) error {
	// 桥接只在它自己的字段变了的时候才重开。每次保存都重启的话，端口会在
	// 18110..18130 之间来回换，已经配好的插件每次都要重新探测；服务本身也要
	// 重新监听一次，纯属白折腾。读不出旧值时按"变了"处理，宁可多重启一次。
	bridgeBefore, bridgeErr := a.settingsService.GetSettings()

	err := a.settingsService.UpdateSettings(input)
	if err == nil {
		// 空闲调度改了要立刻生效：门自己 30 秒才刷一次快照，用户关掉开关后
		// 不该还对着一个"仍在等空闲"的任务干等。
		a.idleGate.InvalidateSettings()
		a.setLogEnabled(input.LogEnabled)
		a.configureLocalMetadata(input.LocalMetadataEnabled)
		if watchErr := a.configureLibraryWatcher(input.LibraryWatchEnabled); watchErr != nil {
			log.Printf("Library watcher settings apply failed err=%v", watchErr)
		}
		// 设置已保存；回流同步失败只记录（下次交互/启动同步自愈），
		// 不把已成功的设置保存报成失败。
		if _, syncErr := a.shortFeedService.SyncFeedback(); syncErr != nil {
			log.Printf("Short-feed feedback settings apply failed err=%v", syncErr)
		}
		// 桥接开关或下载目录改了要立刻生效：关掉之后端口必须马上不再监听，
		// 开启之后不该等到下次重启应用才能配对。
		if bridgeErr != nil || bridgeBefore == nil ||
			bridgeBefore.BrowserBridgeEnabled != input.BrowserBridgeEnabled {
			a.restartBrowserBridge()
		}
	}
	log.Printf("API UpdateSettings err=%v", err)
	return err
}

func (a *App) GetBackupStatus() services.BackupStatus {
	return a.backupService.GetStatus()
}

func (a *App) ListDatabaseBackups() ([]services.BackupFile, error) {
	return a.backupService.ListBackups()
}

func (a *App) CreateDatabaseBackup() (*services.BackupFile, error) {
	if err := a.beginBackupOperation(); err != nil {
		return nil, err
	}
	defer a.backupWG.Done()
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.backupService.CreateBackup(ctx)
}

func (a *App) RestoreDatabaseBackup(request services.BackupRestoreRequest) error {
	if err := a.beginBackupOperation(); err != nil {
		return err
	}
	defer a.backupWG.Done()
	a.restoreMu.Lock()
	defer a.restoreMu.Unlock()
	if a.restoreTerminal {
		return fmt.Errorf("数据库恢复已完成或进入不可恢复状态，请等待应用退出后重新打开")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	err := a.backupService.RestoreBackupWithLifecycle(ctx, request, a.enterDatabaseRestoreMode, database.Init)
	if err == nil || services.DatabaseRestoreRequiresRestart(err) {
		a.restoreTerminal = true
		_ = database.Close()
		if a.ctx != nil {
			go func(runtimeCtx context.Context) {
				time.Sleep(150 * time.Millisecond)
				runtime.Quit(runtimeCtx)
			}(a.ctx)
		} else {
			// 运行时上下文不可用（理论上只在启动完成前触发）：仍然退出进程，
			// 避免恢复完成后应用停留在数据库已关闭的僵死状态。
			go func() {
				time.Sleep(150 * time.Millisecond)
				log.Printf("Database restore finished without runtime context; exiting process")
				os.Exit(0)
			}()
		}
		return err
	}
	a.resumeAfterDatabaseRestoreFailure()
	return err
}

func (a *App) enterDatabaseRestoreMode() error {
	if a.aiTaggingService != nil {
		a.aiTaggingService.StopAndWait()
	}
	if a.localMetadata != nil {
		a.localMetadata.StopExport()
		a.localMetadata.StopBackfill()
	}
	if a.libraryWatcher != nil {
		if err := a.libraryWatcher.Close(); err != nil {
			return err
		}
	}
	if a.technicalBackfill != nil {
		a.technicalBackfill.StopAndWait()
	}
	if a.enhancement != nil {
		a.enhancement.StopAndWait()
	}
	if a.perceptualHash != nil {
		a.perceptualHash.StopAndWait()
	}
	if a.imageEXIFBackfill != nil {
		a.imageEXIFBackfill.StopAndWait()
	}
	if svc := a.semanticIndexService(); svc != nil {
		svc.StopAndWait()
	}
	if svc := a.imageAITaggingService(); svc != nil {
		svc.StopAndWait()
	}
	if svc := a.imageSemanticIndexService(); svc != nil {
		svc.StopAndWait()
	}
	if a.subtitleService != nil {
		a.subtitleService.QuiesceGeneration()
	}
	if a.shortFeedServer != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.shortFeedServer.Stop(stopCtx); err != nil {
			return err
		}
	}
	pathRelease := services.BeginLibraryMaintenance()
	databaseRelease := database.BeginMaintenance()
	a.restoreRelease = func() {
		databaseRelease()
		pathRelease()
	}
	if err := database.Close(); err != nil {
		return &services.DatabaseRestoreError{
			Fatal: true,
			Err:   fmt.Errorf("关闭数据库连接失败，应用必须重启: %w", err),
		}
	}
	return nil
}

func (a *App) resumeAfterDatabaseRestoreFailure() {
	a.releaseDatabaseRestoreMode()
	if a.ctx == nil {
		return
	}
	a.aiTaggingService.Start(a.ctx)
	a.resetSemanticIndexService()
	a.resetImageSemanticIndexService()
	a.resetImageAITaggingService()
	a.startShortFeedServer(a.ctx)
	if settings, err := a.settingsService.GetSettings(); err == nil {
		_ = a.configureLibraryWatcher(settings.LibraryWatchEnabled)
		a.configureLocalMetadata(settings.LocalMetadataEnabled)
	}
}

func (a *App) releaseDatabaseRestoreMode() {
	if a.restoreRelease != nil {
		a.restoreRelease()
		a.restoreRelease = nil
	}
}

func (a *App) configureLocalMetadata(enabled bool) {
	if a.videoService == nil || a.localMetadata == nil {
		return
	}
	if enabled {
		a.videoService.SetLocalMetadataObserver(a.localMetadata.ObserveVideo)
		return
	}
	if a.localMetadata.BackfillStatus().Running {
		_ = a.localMetadata.CancelBackfill()
	}
	a.videoService.SetLocalMetadataObserver(nil)
}

// ===== Directory Methods =====

// GetAllDirectories 获取所有扫描目录
func (a *App) GetAllDirectories() ([]models.ScanDirectory, error) {
	dirs, err := a.directoryService.GetAllDirectories()
	log.Printf("API GetAllDirectories result=%d err=%v sample=%s", len(dirs), err, summarizeDirectories(dirs, 5))
	return dirs, err
}

// AddDirectory 添加扫描目录。
//
// 加完立刻对这一个目录跑一次窄扫描（D-S03）：之前删掉这个目录时被标失效的记录，
// 会在扫描发现文件还在时自动清掉标记回到列表，标签、评分、观看进度原样保留。
// 只有真的扫得到的文件才恢复——盘没插、路径写错时不会有任何记录被复活。
//
// 扫描放后台跑：添加目录的对话框不该被一次可能几分钟的扫描卡住，完成后复用既有的
// library-watcher-reconciled 事件让片库页自己刷新。
func (a *App) AddDirectory(path, alias string) (*models.ScanDirectory, error) {
	dir, err := a.directoryService.AddDirectory(path, alias)
	if err == nil {
		a.reconfigureLibraryWatcher()
		if dir != nil {
			go a.rescanAddedDirectory(*dir)
		}
	}
	return dir, err
}

// rescanAddedDirectory 对刚加入的目录做一次窄对账，把之前标失效的记录接回来。
func (a *App) rescanAddedDirectory(added models.ScanDirectory) {
	dirs, err := a.directoryService.GetAllDirectories()
	if err != nil {
		log.Printf("加入扫描目录后的恢复扫描读取目录失败 path=%s err=%v", added.Path, err)
		return
	}
	result := a.videoService.SyncAffectedDirectories(dirs, []string{added.Path})
	if result == nil {
		return
	}
	log.Printf("加入扫描目录后的恢复扫描完成 path=%s scanned=%d added=%d restored=%d errors=%d",
		added.Path, result.Scanned, result.Added, result.Restored, len(result.Errors))

	if a.cleanupService != nil && (result.Added > 0 || result.Restored > 0 || result.Relocated > 0) {
		a.cleanupService.InvalidateAnalysis()
	}
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "library-watcher-reconciled", services.LibraryReconcileEvent{
		DirectoryID: added.ID,
		Affected:    1,
		Result: &services.LibraryReconcileSummary{
			Scanned:           result.Scanned,
			Added:             result.Added,
			Relocated:         result.Relocated,
			Stale:             result.Stale,
			Restored:          result.Restored,
			MetadataRefreshed: result.MetadataRefreshed,
			Skipped:           result.Skipped,
			ErrorCount:        len(result.Errors),
		},
		CompletedAt: time.Now(),
	})
}

// UpdateDirectory 更新目录
func (a *App) UpdateDirectory(id uint, path, alias string) error {
	err := a.directoryService.UpdateDirectory(id, path, alias)
	if err == nil {
		a.reconfigureLibraryWatcher()
	}
	return err
}

// DeleteDirectory 删除扫描目录。
//
// 删配置行之前先把该目录下的视频标成失效（D-S01）：记录留着、不软删、不进回收站，
// 它们从默认列表消失但留在「路径失效」视图里，把同一个路径加回来时由扫描自动恢复。
//
// 顺序不能反：标记失败就不删配置行并把错误交回给用户。先删了配置又没标上的话，
// 那批记录会掉进"扫描看不见、列表看得见"的夹缝——正是这次要修的毛病。
func (a *App) DeleteDirectory(id uint) error {
	dirs, err := a.directoryService.GetAllDirectories()
	if err != nil {
		log.Printf("API DeleteDirectory load dirs err=%v", err)
		return err
	}
	var removedPath string
	found := false
	remaining := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir.ID == id {
			removedPath = dir.Path
			found = true
			continue
		}
		remaining = append(remaining, dir.Path)
	}
	if !found {
		return fmt.Errorf("扫描目录不存在: %d", id)
	}

	marked, err := a.videoService.MarkVideosStaleUnderRemovedRoot(removedPath, remaining)
	if err != nil {
		log.Printf("API DeleteDirectory mark stale id=%d path=%s err=%v", id, removedPath, err)
		return err
	}

	err = a.directoryService.DeleteDirectory(id)
	if err == nil {
		a.reconfigureLibraryWatcher()
	}
	log.Printf("API DeleteDirectory id=%d path=%s marked_stale=%d err=%v", id, removedPath, marked, err)
	return err
}

func (a *App) GetLibraryWatcherStatus() services.LibraryWatcherStatus {
	if a.libraryWatcher == nil {
		return services.LibraryWatcherStatus{}
	}
	status := a.libraryWatcher.Snapshot()
	if status.Running || len(status.Roots) > 0 {
		return status
	}
	settings, settingsErr := a.settingsService.GetSettings()
	dirs, dirsErr := a.directoryService.GetAllDirectories()
	if settingsErr != nil || dirsErr != nil {
		return status
	}
	state := services.LibraryWatchStateDisabled
	reason := "disabled"
	message := "实时同步已关闭"
	if settings.LibraryWatchEnabled {
		state = services.LibraryWatchStateError
		reason = "watcher_not_running"
		message = "实时同步未运行"
	}
	for _, dir := range dirs {
		status.Roots = append(status.Roots, services.LibraryWatchRootStatus{
			DirectoryID: dir.ID,
			State:       state,
			ReasonCode:  reason,
			Message:     message,
			UpdatedAt:   time.Now(),
		})
	}
	return status
}

func (a *App) RetryLibraryWatcherRoot(directoryID uint) (services.LibraryWatchRootStatus, error) {
	if a.libraryWatcher == nil || !a.libraryWatcher.Snapshot().Running {
		return services.LibraryWatchRootStatus{}, fmt.Errorf("实时同步未运行")
	}
	return a.libraryWatcher.RetryRoot(directoryID)
}

func (a *App) configureLibraryWatcher(enabled bool) error {
	if a.libraryWatcher == nil {
		return nil
	}
	if !enabled {
		err := a.libraryWatcher.Close()
		a.emitLibraryWatcherStatus()
		return err
	}
	dirs, err := a.directoryService.GetAllDirectories()
	if err != nil {
		return err
	}
	if a.libraryWatcher.Snapshot().Running {
		err = a.libraryWatcher.Reconfigure(dirs)
	} else {
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		err = a.libraryWatcher.Start(ctx, dirs)
	}
	a.emitLibraryWatcherStatus()
	return err
}

func (a *App) reconfigureLibraryWatcher() {
	settings, err := a.settingsService.GetSettings()
	if err != nil || !settings.LibraryWatchEnabled {
		return
	}
	if err := a.configureLibraryWatcher(true); err != nil {
		log.Printf("Library watcher directory reconfiguration failed err=%v", err)
	}
}

func (a *App) emitLibraryWatcherStatus() {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "library-watcher-status", a.GetLibraryWatcherStatus())
	}
}
