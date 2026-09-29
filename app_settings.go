package main

import (
	"context"
	"errors"
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

// errDatabaseMaintenanceBusy：恢复备份与切换后端互斥，后到者直接拒绝、不排队（§1.2b / §11）。
var errDatabaseMaintenanceBusy = errors.New("数据库恢复或切换正在进行，请稍后再试")

// StartDatabaseSwitch 迁移数据并写入后端配置；成功后需要重启才生效（设计中的 MigrateAndSwitch）。
// 迁移在后台跑，进度走 database-switch-state 事件，GetDatabaseSwitchStatus 兜底；
// 成功的终态带 relaunch_required=true，前端据此提供「立即重启」（RelaunchApp）。
//
// 迁移全程处在与恢复备份相同的维护模式里（D-PC55 / APP-02）：后台服务停掉、写入被围栏
// 拒绝，迁移器只经维护通道读源库。失败（含被取消）时撤围栏、恢复服务；成功时围栏保持、
// 服务不恢复，进入「待重启」终态（与恢复成功的 restoreTerminal 同等处理），之后的恢复、
// 再次切换返回 relaunch_pending，唯一出口是「立即重启」。与恢复共用 restoreMu，两者不会
// 同时进维护模式；恢复已进入终态（应用即将退出）时不再允许切换。
func (a *App) StartDatabaseSwitch(target string) error {
	if a.databaseSwitchService.RelaunchPending() {
		return services.ErrDatabaseRelaunchPending
	}
	if !a.restoreMu.TryLock() {
		return errDatabaseMaintenanceBusy
	}
	if a.databaseSwitchService.RelaunchPending() {
		a.restoreMu.Unlock()
		return services.ErrDatabaseRelaunchPending
	}
	if a.restoreTerminal {
		a.restoreMu.Unlock()
		return fmt.Errorf("数据库恢复已完成或进入不可恢复状态，请等待应用退出后重新打开")
	}
	preflight, err := a.databaseSwitchService.Preflight(target)
	if err != nil {
		a.restoreMu.Unlock()
		return err
	}
	if !preflight.Reachable || !preflight.Empty {
		a.restoreMu.Unlock()
		return fmt.Errorf("%s", preflight.Message)
	}
	go func() {
		defer a.restoreMu.Unlock()
		err := a.databaseSwitchService.SwitchWithLifecycle(context.Background(), target,
			func() error { return a.enterDatabaseRestoreMode(false) },
			a.resumeAfterDatabaseRestoreFailure,
		)
		if err == nil {
			// 持 restoreMu 置位（本 goroutine 一直持有它）：围栏不撤，应用停在「待重启」。
			a.restoreTerminal = true
		}
		log.Printf("API StartDatabaseSwitch target=%s err=%v", target, err)
	}()
	return nil
}

// cancelDatabaseSwitchForShutdown 取消正在进行的迁移（M-3）。接线项：app.go 的 shutdown 必须在
// 拿 restoreMu 之前调用它——迁移 goroutine 一直持有 restoreMu，不先取消，退出会等整个迁移跑完。
// 取消按失败处理：撤围栏，配置不改，目标库留为半迁移，下次可清空重试。
func (a *App) cancelDatabaseSwitchForShutdown() {
	if a.databaseSwitchService == nil {
		return
	}
	if a.databaseSwitchService.CancelRunningSwitch() {
		log.Printf("App shutdown cancelled running database switch")
	}
}

// SwitchBackendConfigOnly 切回上一个后端：只改配置、不迁移（D-PC55）。成功时
// relaunch_required=true；切换之后在当前库里的改动不会带回，结果的 message 写明。
//
// 与迁移并切换同一口径（APP-02）：写配置前进入与恢复相同的维护模式（不关连接），成功后
// 围栏保持、进入「待重启」终态，唯一出口是「立即重启」；与恢复、迁移共用 restoreMu。
func (a *App) SwitchBackendConfigOnly(target string) (*services.DatabaseSwitchConfigResult, error) {
	if !a.restoreMu.TryLock() {
		return nil, errDatabaseMaintenanceBusy
	}
	defer a.restoreMu.Unlock()
	if a.restoreTerminal && !a.databaseSwitchService.RelaunchPending() {
		return nil, fmt.Errorf("数据库恢复已完成或进入不可恢复状态，请等待应用退出后重新打开")
	}
	result, err := a.databaseSwitchService.SwitchBackendConfigOnlyWithLifecycle(target,
		func() error { return a.enterDatabaseRestoreMode(false) },
		a.resumeAfterDatabaseRestoreFailure,
	)
	if err == nil && result != nil && result.Switched {
		// 持 restoreMu 置位：围栏不撤，应用停在「待重启」。
		a.restoreTerminal = true
	}
	if result != nil {
		log.Printf("API SwitchBackendConfigOnly target=%s switched=%v reason=%s err=%v", target, result.Switched, result.ReasonCode, err)
	} else {
		log.Printf("API SwitchBackendConfigOnly target=%s err=%v", target, err)
	}
	return result, err
}

// ClearMigrationTarget 清空迁移失败的目标库，以便重试（D-PC55）。confirmText 必须是「清空」；
// 当前正在使用的库永远不会被清空。
func (a *App) ClearMigrationTarget(target, confirmText string) (*services.DatabaseTargetClearResult, error) {
	result, err := a.databaseSwitchService.ClearMigrationTarget(target, confirmText)
	if result != nil {
		log.Printf("API ClearMigrationTarget target=%s cleared=%v removed=%d reason=%s err=%v", target, result.Cleared, len(result.Removed), result.ReasonCode, err)
	} else {
		log.Printf("API ClearMigrationTarget target=%s err=%v", target, err)
	}
	return result, err
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

// TestWatchlistMetadataConnection 用设置页当前填写的代理与凭证向一个在线资料源发一次
// 真实请求，分清「凭证缺失 / 凭证无效 / 代理不通 / 网络不可达」。留空的字段回退到已保存
// 或环境变量配置，所以不必先保存再测。
//
// 日志里**只记源名、分类码与耗时**：凭证不能落地，代理地址也不能——它常带口令。
func (a *App) TestWatchlistMetadataConnection(input services.WatchlistMetadataProbeInput) services.WatchlistMetadataProbeResult {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	result := services.ProbeWatchlistMetadataSource(ctx, input)
	log.Printf("API TestWatchlistMetadataConnection source=%q ok=%v failure=%q status=%d latency_ms=%d",
		result.Source, result.OK, result.Failure, result.HTTPStatus, result.LatencyMS)
	return result
}

func (a *App) GetBackupStatus() services.BackupStatus {
	return a.backupService.GetStatus()
}

func (a *App) ListDatabaseBackups() ([]services.BackupFile, error) {
	return a.backupService.ListBackups()
}

// RevealBackupDirectory 在访达中打开实际的备份目录（与 GetBackupStatus 的 backup_directory 一致）。
func (a *App) RevealBackupDirectory() error {
	err := a.backupService.RevealBackupDirectory()
	log.Printf("API RevealBackupDirectory err=%v", err)
	return err
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

// RestoreDatabaseBackup 恢复备份。与切换后端互斥：另一方正在进行时立即拒绝，不排队等它结束；
// 切换成功后的「待重启」终态下返回 relaunch_pending。
func (a *App) RestoreDatabaseBackup(request services.BackupRestoreRequest) error {
	if err := a.beginBackupOperation(); err != nil {
		return err
	}
	defer a.backupWG.Done()
	if !a.restoreMu.TryLock() {
		return errDatabaseMaintenanceBusy
	}
	defer a.restoreMu.Unlock()
	if a.databaseSwitchService != nil && a.databaseSwitchService.RelaunchPending() {
		return services.ErrDatabaseRelaunchPending
	}
	if a.restoreTerminal {
		return fmt.Errorf("数据库恢复已完成或进入不可恢复状态，请等待应用退出后重新打开")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	enterRestoreMode := func() error { return a.enterDatabaseRestoreMode(true) }
	err := a.backupService.RestoreBackupWithLifecycle(ctx, request, enterRestoreMode, database.Init)
	if err == nil || services.DatabaseRestoreRequiresRestart(err) {
		a.restoreTerminal = true
		_ = database.Close()
		if a.ctx != nil {
			// 应用内部发起的退出：数据库已关，退出确认（beforeClose）必须放行，
			// 否则会在一个已经不能读库的窗口上弹「有任务在跑」（D-PC21 内部放行）。
			allowInternalQuit()
			go func(runtimeCtx context.Context) {
				time.Sleep(internalQuitDelay)
				quitRuntime(runtimeCtx)
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

// enterDatabaseRestoreMode 是数据库维护模式的唯一入口，恢复备份与切换后端共用（D-PC55）：
// 停掉会读写数据库的后台服务与对外服务，再立路径与数据库两道围栏。之后普通的 GORM 读写
// 一律得到 ErrMaintenance，只有经 database.WithMaintenanceAccess 的恢复/迁移流程能访问。
//
// closeConnection：恢复要换掉库（SQLite 换文件、PG 灌回同一个库后重连），围栏后必须关闭
// 连接；切换后端只读源库，连接保持打开供迁移器读取。失败后离开维护模式统一走
// resumeAfterDatabaseRestoreFailure；切换成功后不离开（「待重启」终态）。
func (a *App) enterDatabaseRestoreMode(closeConnection bool) error {
	if a.jellyfinServer != nil {
		a.jellyfinServer.Stop()
	}
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
	// 帧哈希回填同样逐项写 video_frame_hash_sequences，此前漏在清单外（清理检测遗留 Q5）。
	if a.frameHash != nil {
		a.frameHash.StopAndWait()
	}
	if a.imageEXIFBackfill != nil {
		a.imageEXIFBackfill.StopAndWait()
	}
	// 与上面几个后台任务同理：恢复备份会 Close 掉旧连接再把 database.DB 换成新库。
	// 这个任务还跑着的话，它会拿旧行的路径去 stat、拿新库的行去判新鲜度，记出一堆
	// 与两边都对不上的成功/失败，同时并发读写包级 *gorm.DB。
	if a.imagePHashBackfill != nil {
		a.imagePHashBackfill.StopAndWait()
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
	// 年度榜单抓取与上面几个同理，而且更急：它握着的是**构造时注入**的那个
	// *gorm.DB，恢复会 Close 掉它再把 database.DB 换成新库。不等它收摊，一轮
	// 正在跑的抓取就会拿着已经关掉的连接继续 upsert 与写回。详情阶段还是
	// 1 次/秒的限速循环，一年能跑约 25 分钟，靠「大概跑完了」赌不过去。
	if svc := a.movieChartService(); svc != nil {
		svc.StopRefreshAndWait()
	}
	if a.subtitleService != nil {
		a.subtitleService.QuiesceGeneration()
	}
	// 手机端服务的启停一律经生命周期锁，与设置页开关互斥（P-021）。
	if err := a.withShortFeedLifecycle(a.stopShortFeedForSetting); err != nil {
		return err
	}
	pathRelease := services.BeginLibraryMaintenance()
	databaseRelease := database.BeginMaintenance()
	a.restoreRelease = func() {
		databaseRelease()
		pathRelease()
	}
	if !closeConnection {
		return nil
	}
	if err := database.Close(); err != nil {
		return &services.DatabaseRestoreError{
			Fatal: true,
			Err:   fmt.Errorf("关闭数据库连接失败，应用必须重启: %w", err),
		}
	}
	return nil
}

// resumeAfterDatabaseRestoreFailure 离开维护模式：撤围栏并把 enterDatabaseRestoreMode
// 停掉的服务恢复起来。恢复备份失败、切换后端失败（含被取消）共用；切换成功不走这里，
// 围栏保持到重启。
func (a *App) resumeAfterDatabaseRestoreFailure() {
	a.releaseDatabaseRestoreMode()
	if a.ctx == nil {
		return
	}
	a.aiTaggingService.Start(a.ctx)
	a.resetSemanticIndexService()
	a.resetImageSemanticIndexService()
	a.resetImageAITaggingService()
	// 恢复失败续跑：database.Init 已经把 database.DB 换成了新的连接，而榜单服务
	// 握的是进入恢复模式之前那一个（已 Close）。不重建的话榜单页从此每次读都报
	// 「sql: database is closed」，而且只在这条失败续跑的路径上出现。
	a.resetMovieChartService()
	// 手机端服务按开关决定是否重新监听：restartShortFeedServerLocked 先停旧实例，再看
	// ShouldStart()。此前这里无条件 startShortFeedServer，用户关掉的手机端访问会在一次
	// 失败的恢复之后被重新打开（PLAY-01）。
	if err := a.withShortFeedLifecycle(func() error { return a.restartShortFeedServerLocked(a.ctx) }); err != nil {
		log.Printf("Short feed server resume after database maintenance failed err=%v", err)
	}
	if a.jellyfinServer != nil {
		a.jellyfinServer.Start()
	}
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
		Result:      services.SummarizeScanResult(result),
		CompletedAt: time.Now(),
	})
	a.emitLibraryScanSummary(services.ScanTriggerDirectoryChange, result)
}

// UpdateDirectory 更新目录。旧绑定的薄包装：路径变化按「替换为新目录」处理。
// 前端全部改用 UpdateDirectoryWithMode 之后由收尾切片删除。
func (a *App) UpdateDirectory(id uint, path, alias string) error {
	_, err := a.UpdateDirectoryWithMode(id, path, alias, services.DirectoryUpdateModeReplace)
	return err
}

// UpdateDirectoryWithMode 更新扫描目录的路径与别名（D-PC07）。
// mode=remap：目录只是换了位置，保留记录 ID、标签与进度，路径整体改写到新目录；
// mode=replace：用新目录替换旧目录，旧目录下的记录标为 removed_root。路径没变时 mode 被忽略。
//
// 成功且路径变化后重配监听，并在后台对新路径做一次窄对账——remap 后确认新位置的文件都在，
// replace 后把之前标失效、现在又扫得到的记录接回来。
func (a *App) UpdateDirectoryWithMode(id uint, path, alias, mode string) (*services.DirectoryUpdateResult, error) {
	result, err := a.directoryService.UpdateDirectory(id, path, alias, mode)
	if err != nil {
		return nil, err
	}
	if result.PathChanged {
		a.reconfigureLibraryWatcher()
		go a.rescanAddedDirectory(models.ScanDirectory{ID: id, Path: result.NewPath})
	}
	return result, nil
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
	settings, err := a.settingsService.GetSettings()
	if err != nil {
		return err
	}
	if a.libraryWatcher.Snapshot().Running {
		err = a.libraryWatcher.Reconfigure(dirs, settings.ScanExcludePaths)
	} else {
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		err = a.libraryWatcher.Start(ctx, dirs, settings.ScanExcludePaths)
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
