package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"video-master/internal/appdata"
	"video-master/models"
	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	maxAppLogSizeBytes = 20 * 1024 * 1024
	maxAppLogBackups   = 3
)

var sensitiveLogPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(deepl[_\-\s]*api[_\-\s]*key["'\s:=]+)([^"',}\s]+)`),
	regexp.MustCompile(`(?i)(api[_\-\s]*key["'\s:=]+)([^"',}\s]+)`),
	regexp.MustCompile(`(?i)(authorization["'\s:=]+bearer\s+)([^"',}\s]+)`),
}

// App struct
type App struct {
	ctx                   context.Context
	videoService          *services.VideoService
	thumbnailService      *services.ThumbnailService
	tagService            *services.TagService
	settingsService       *services.SettingsService
	backupService         *services.BackupService
	databaseSwitchService *services.DatabaseSwitchService
	directoryService      *services.DirectoryService
	subtitleService       *services.SubtitleService
	subtitleWorkbench     *services.SubtitleWorkbenchService
	cleanupService        *services.CleanupService
	subtitleSearchService *services.SubtitleSearchService
	aiTaggingService      *services.AITaggingService
	aiQualityService      *services.AIQualityService
	shortFeedService      *services.ShortFeedService
	personService         *services.PersonService
	collectionService     *services.CollectionService
	collectionSuggestions *services.CollectionSuggestionService
	videoDetailService    *services.VideoDetailService
	libraryStatsService   *services.LibraryStatsService
	localMetadata         *services.LocalMetadataService
	mediaProbeService     *services.MediaProbeService
	technicalBackfill     *services.TechnicalBackfillService
	perceptualHash        *services.PerceptualHashService
	// mediaWorkSlot 是转封装、帧哈希回填、人脸抽帧共享的重媒体处理槽（D-007）：
	// 容量 1，三条流水线在处理每一项之前各自取一次。新增重 ffmpeg 任务请复用它，
	// 不要各建一个——各建一个等于没有限制。
	mediaWorkSlot       *services.MediaWorkSlot
	frameHash           *services.FrameHashService
	playbackProxies     *services.PlaybackProxyService
	enhancement         *services.EnhancementService
	enhancementModels   *services.EnhancementModelInstaller
	iinaProgress        *services.IINAProgressService
	semanticIndex       *services.SemanticIndexService
	libraryWatcher      *services.LibraryWatcherService
	imageService        *services.ImageService
	imageEXIFBackfill   *services.ImageEXIFBackfillService
	imageThumbnail      *services.ImageThumbnailService
	imageLibraryService *services.ImageLibraryService
	imageStatsService   *services.ImageStatsService
	imageCleanupService *services.ImageCleanupService
	backgroundTasks     *services.BackgroundTaskRegistry
	idleGate            *services.IdleGate
	desktopNotify       *services.DesktopNotificationCenter
	// 人脸识别（D-016..D-020）：运行时管理与分析服务。运行时不可用时分析入口置灰，
	// 自动路径静默跳过——两者都读同一个 FaceRuntime.Status()。
	faceRuntime  *services.FaceRuntime
	faceAnalysis *services.FaceAnalysisService
	// faceReview 是人脸链路上唯一会写 video_people / image_people 的服务，
	// 且只由用户的审阅动作驱动（D-019）。
	faceReview            *services.FaceReviewService
	shortFeedServer       *services.ShortFeedHTTPServer
	shortFeedStartupError string
	startupError          string
	logFile               *os.File // 保持日志文件句柄引用，防止泄漏
	backupCancel          context.CancelFunc
	backupWG              sync.WaitGroup
	backupOpMu            sync.Mutex
	backupOpsClosed       bool
	semanticMu            sync.RWMutex
	imageAITagMu          sync.RWMutex
	imageAITagging        *services.ImageAITaggingService
	imageAITagAutoMu      sync.Mutex // 串行化自动触发，防止启动与扫描后触发并发
	imageSemanticMu       sync.RWMutex
	imageSemanticIndex    *services.ImageSemanticIndexService
	restoreMu             sync.Mutex
	restoreTerminal       bool
	restoreRelease        func()
}

// NewApp creates a new App application struct
func NewApp() *App {
	// 数据根目录：必要时把历史遗留的 ~/.video-master 一次性改名成 ~/.CineInsight。
	// 迁移失败不能静默继续——那会让用户对着一个空库以为数据全丢了。
	dataDir, dataDirErr := appdata.Resolve()
	mediaProbeService := services.NewMediaProbeService()
	videoService := services.NewVideoService(mediaProbeService)
	libraryWatcher := services.NewLibraryWatcherService(videoService)
	subtitleService := services.NewSubtitleService(dataDir)
	aiTaggingService := services.NewAITaggingService()
	aiTaggingService.SetTemporaryTranscriptProvider(subtitleService)

	personService := services.NewPersonService(dataDir)
	collectionService := services.NewCollectionService(dataDir)
	localMetadata := services.NewLocalMetadataService(dataDir, personService, collectionService)
	imageThumbnail := services.NewImageThumbnailService(dataDir)
	shortFeedService := services.NewShortFeedService(videoService)
	// 手机端要能刷到图片：图片能否在浏览器显示由解码矩阵裁定，复用同一套缓存。
	shortFeedService.SetImageThumbnailService(imageThumbnail)
	// 重媒体处理槽（D-007）：容量 1，几条 ffmpeg 重流水线共享同一个实例。
	mediaWorkSlot := services.NewMediaWorkSlot()
	// 播放代理（D-001..D-006）：预览与手机端在白名单不命中时经它换源，
	// 因此 videoService 也要拿到它——ResolvePreviewMedia 与删除级联都在那一侧。
	playbackProxies := services.NewPlaybackProxyService(dataDir, mediaProbeService)
	playbackProxies.SetMediaWorkSlot(mediaWorkSlot)
	videoService.SetPlaybackProxyService(playbackProxies)
	app := &App{
		videoService:          videoService,
		thumbnailService:      services.NewThumbnailService(videoService, dataDir),
		tagService:            &services.TagService{},
		settingsService:       &services.SettingsService{},
		backupService:         services.NewBackupService(dataDir),
		databaseSwitchService: services.NewDatabaseSwitchService(dataDir),
		directoryService:      &services.DirectoryService{},
		subtitleService:       subtitleService,
		subtitleWorkbench:     services.NewSubtitleWorkbenchService(subtitleService),
		cleanupService:        &services.CleanupService{},
		subtitleSearchService: &services.SubtitleSearchService{},
		aiTaggingService:      aiTaggingService,
		aiQualityService:      services.NewAIQualityService(),
		shortFeedService:      shortFeedService,
		personService:         personService,
		collectionService:     collectionService,
		collectionSuggestions: services.NewCollectionSuggestionService(collectionService),
		videoDetailService:    services.NewVideoDetailService(personService, collectionService),
		libraryStatsService:   services.NewLibraryStatsService(),
		localMetadata:         localMetadata,
		mediaProbeService:     mediaProbeService,
		technicalBackfill:     services.NewTechnicalBackfillService(mediaProbeService),
		perceptualHash:        services.NewPerceptualHashService(),
		mediaWorkSlot:         mediaWorkSlot,
		frameHash:             services.NewFrameHashService(mediaWorkSlot),
		playbackProxies:       playbackProxies,
		enhancement:           services.NewEnhancementService(videoService, mediaProbeService, aiTaggingService.SameSourceService(), dataDir),
		enhancementModels:     services.NewEnhancementModelInstaller(services.EnhancementModelDirFor(dataDir)),
		iinaProgress:          services.NewIINAProgressService(homeDirForIINA()),
		libraryWatcher:        libraryWatcher,
		imageService:          services.NewImageService(),
		imageEXIFBackfill:     services.NewImageEXIFBackfillService(),
		imageThumbnail:        imageThumbnail,
		imageLibraryService:   services.NewImageLibraryService(),
		imageStatsService:     services.NewImageStatsService(),
		imageCleanupService:   services.NewImageCleanupService(),
		backgroundTasks:       services.NewBackgroundTaskRegistry(),
		idleGate:              services.NewIdleGate(),
	}
	app.desktopNotify = services.NewDesktopNotificationCenter(services.NewDesktopNotifier(), app.desktopNotificationsEnabled)
	// 人脸识别（D-016、D-018）：运行时读设置里的镜像前缀，分析复用图片解码矩阵与
	// 重媒体处理槽。构造在 wire* 之前，两个 wire 才接得到它。
	app.faceRuntime = services.NewFaceRuntime(dataDir, app.faceModelMirrorURL)
	app.faceAnalysis = services.NewFaceAnalysisService(dataDir, app.faceRuntime, imageThumbnail)
	app.faceAnalysis.SetMediaWorkSlot(mediaWorkSlot)
	app.faceReview = &services.FaceReviewService{}
	app.wireBackgroundTaskRegistry()
	app.wireDesktopNotifier()
	if dataDirErr != nil {
		app.setStartupError(dataDirErr)
	}
	return app
}

// wireBackgroundTaskRegistry 把全部长任务服务接进登记表（D-014）。
// 在 NewApp 里做而不是 startup：数据库恢复后重建的服务各自在重建处补接，
// 这里保证"构造出来就已登记"，不依赖启动顺序。
func (a *App) wireBackgroundTaskRegistry() {
	a.technicalBackfill.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.perceptualHash.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.cleanupService.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.imageEXIFBackfill.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.localMetadata.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.aiTaggingService.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.subtitleService.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.enhancement.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.backupService.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.collectionSuggestions.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.frameHash.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.playbackProxies.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.faceAnalysis.SetBackgroundTaskRegistry(a.backgroundTasks)
}

// wireDesktopNotifier 把本切片接入的长任务终态接到桌面通知（D-013）。
// 两个语义索引服务在 resetSemanticIndexService / resetImageSemanticIndexService
// 里重建，各自在重建处补接。
func (a *App) wireDesktopNotifier() {
	a.subtitleService.SetDesktopNotifier(a.desktopNotify)
	a.enhancement.SetDesktopNotifier(a.desktopNotify)
	a.backupService.SetDesktopNotifier(a.desktopNotify)
	a.playbackProxies.SetDesktopNotifier(a.desktopNotify)
	a.faceAnalysis.SetDesktopNotifier(a.desktopNotify)
}

// desktopNotificationsEnabled 读通知开关，每次投递前读一次（设置即时生效）。
// 读不到就当没开：这时数据库本来就有问题，与其猜一个默认值不如安静。
func (a *App) desktopNotificationsEnabled() bool {
	settings, err := a.settingsService.GetSettings()
	if err != nil {
		return false
	}
	return settings.DesktopNotificationsEnabled
}

// faceModelMirrorURL 读人脸模型下载的镜像前缀（D-016），每次下载前读一次。
// 读不到设置就当没配镜像：官方地址通不通是另一回事，不该因为读不到设置就不许下载。
func (a *App) faceModelMirrorURL() string {
	settings, err := a.settingsService.GetSettings()
	if err != nil {
		return ""
	}
	return settings.FaceModelMirrorURL
}

// SetWindowForeground 由前端在 focus / blur / visibilitychange 时上报窗口前后台（D-013）。
// 后端只在标记为后台时发系统通知；前台时应用内 AppFeedback 已经提示过了。
func (a *App) SetWindowForeground(foreground bool) {
	a.desktopNotify.SetForeground(foreground)
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	log.Printf("App startup begin startupError=%q", a.startupError)
	if a.startupError != "" {
		return
	}
	if err := a.videoService.ReconcileTrashEntries(); err != nil {
		log.Printf("App startup trash reconciliation failed err=%v", err)
	}
	if err := a.imageService.ReconcileImageTrashEntries(); err != nil {
		log.Printf("App startup image trash reconciliation failed err=%v", err)
	}
	a.subtitleService.SetContext(ctx) // Inject context
	// Background workers can still emit while the app is tearing down; the frontend is gone by then.
	emit := func(event string, data any) {
		if ctx.Err() != nil {
			return
		}
		runtime.EventsEmit(ctx, event, data)
	}
	// 登记表变化驱动三件事：清掉已跑完任务的一次性 bypass、刷 Dock 角标（D-014），
	// 再广播给前端。角标不看开关也不看前后台，它就是"现在几个任务在跑"。
	a.backgroundTasks.SetOnChange(func(running []string) {
		a.idleGate.SyncRunningTasks(running)
		a.desktopNotify.SetBadge(services.BadgeLabelForCount(len(running)))
		emit("background-tasks", running)
	})
	a.idleGate.SetEventEmitter(func(status services.IdleSchedulerStatus) {
		emit("idle-scheduler-state", status)
	})
	a.resetSemanticIndexService()
	a.resetImageSemanticIndexService()
	a.resetImageAITaggingService()
	a.technicalBackfill.SetEventEmitter(func(status services.TechnicalBackfillStatus) {
		emit("technical-backfill-state", status)
	})
	a.databaseSwitchService.SetProgressSink(func(status services.DatabaseSwitchStatus) {
		emit("database-switch-state", status)
	})
	a.imageCleanupService.SetEventEmitter(func(progress services.ImageCleanupProgress) {
		emit("image-cleanup-progress", progress)
	})
	a.imageEXIFBackfill.SetEventEmitter(func(status services.ImageEXIFBackfillStatus) {
		emit("image-exif-backfill-progress", status)
	})
	a.perceptualHash.SetEventEmitter(func(status services.PerceptualHashStatus) {
		if status.Completed && status.Succeeded > 0 {
			a.cleanupService.InvalidateAnalysis()
		}
		emit("perceptual-hash-state", status)
	})
	a.frameHash.SetEventEmitter(func(status services.FrameHashStatus) {
		// 新算出来的序列会改变"截取片段"这一类候选，缓存的清理结果因此可能过期。
		// 与感知哈希同口径：只标记过期，不静默重跑。
		if status.Completed && status.Succeeded > 0 {
			a.cleanupService.InvalidateAnalysis()
		}
		emit("frame-hash-backfill-state", status)
	})
	a.libraryWatcher.SetEventEmitters(func(status services.LibraryWatcherStatus) {
		emit("library-watcher-status", status)
	}, func(event services.LibraryReconcileEvent) {
		if event.Result != nil && (event.Result.Added > 0 || event.Result.Relocated > 0 || event.Result.Stale > 0 || event.Result.MetadataRefreshed > 0) {
			a.cleanupService.InvalidateAnalysis()
		}
		if event.Result != nil && event.Result.Added > 0 {
			// 自动唤醒经空闲门（D-030）；用户显式的 TriggerAITagging 不走这里。
			go a.triggerAITaggingAuto("library-watch")
		}
		emit("library-watcher-reconciled", event)
	})
	a.localMetadata.SetBackfillEventEmitter(func(status services.LocalMetadataBackfillStatus) {
		emit("local-metadata-backfill", status)
	})
	a.localMetadata.SetExportEventEmitter(func(status services.LocalMetadataExportStatus) {
		emit("local-metadata-export", status)
	})
	a.collectionSuggestions.SetEventEmitter(func(status services.CollectionSuggestionStatus) {
		emit("collection-suggestion-state", status)
	})
	a.playbackProxies.SetEventEmitter(func(status services.PlaybackProxyStatus) {
		emit("playback-proxy-state", status)
	})
	a.faceRuntime.SetEventEmitter(func(status services.FaceRuntimeStatus) {
		emit("face-runtime-state", status)
	})
	a.faceAnalysis.SetEventEmitter(func(status services.FaceAnalysisStatus) {
		emit("face-analysis-state", status)
	})
	a.enhancement.SetEventEmitter(func(view services.EnhancementTaskView) {
		emit("video-enhancement-state", view)
	})
	a.enhancementModels.SetEventEmitter(func(status services.EnhancementModelStatus) {
		emit("enhancement-model-state", status)
	})
	// 模型装好后立刻重探能力并广播，界面不用重启就能看到"可用"。
	a.enhancementModels.SetOnInstalled(func() {
		emit("video-enhancement-capability", a.enhancement.RefreshCapability())
	})
	// 对账可能包含大文件 SHA-256，异步执行避免阻塞窗口可用。
	go a.enhancement.RecoverOnStartup(ctx)
	// 外部播放多半发生在应用没开着的时候，启动时先补一次 IINA 断点；
	// 之后靠监听断点目录实时跟进，看完切回来就已经更新好了，不用重启应用。
	a.iinaProgress.SetOnSynced(func(result services.IINAProgressSyncResult) {
		emit("iina-progress-synced", result)
	})
	go func() {
		if !a.iinaProgress.Available() {
			return
		}
		if _, err := a.iinaProgress.Sync(); err != nil {
			log.Printf("IINA 播放进度同步失败 err=%v", err)
		}
		if err := a.iinaProgress.StartWatching(); err != nil {
			log.Printf("IINA 断点目录监听启动失败 err=%v", err)
		}
	}()
	a.cleanupService.SetContext(ctx)
	if result, err := a.tagService.SyncShortVideoTags(); err != nil {
		log.Printf("App startup short-video tag sync failed err=%v", err)
	} else {
		log.Printf("App startup short-video tag sync tag=%d added=%d removed=%d", result.TagID, result.Added, result.Removed)
	}
	if result, err := a.shortFeedService.SyncFeedback(); err != nil {
		log.Printf("App startup short-feed feedback sync failed err=%v", err)
	} else if result.Enabled {
		log.Printf("App startup short-feed feedback sync video(likes +%d/-%d favorites +%d) image(likes +%d/-%d favorites +%d)",
			result.LikesAdded, result.LikesRemoved, result.FavoritesAdded,
			result.ImageLikesAdded, result.ImageLikesRemoved, result.ImageFavoritesAdded)
	}
	a.aiTaggingService.Start(ctx)
	// 启动时后台增量生成图片描述（仅处理尚无描述的图，配置缺失则静默跳过）。
	go a.triggerImageAITaggingAuto("startup")
	a.startShortFeedServer(ctx)
	if settings, err := a.settingsService.GetSettings(); err == nil {
		log.Printf("App startup settings loaded %s", summarizeSettings(settings))
		a.setLogEnabled(settings.LogEnabled)
		if err := a.configureLibraryWatcher(settings.LibraryWatchEnabled); err != nil {
			log.Printf("App startup library watcher configuration failed err=%v", err)
		}
		a.configureLocalMetadata(settings.LocalMetadataEnabled)
		backupCtx, backupCancel := context.WithCancel(ctx)
		a.backupCancel = backupCancel
		a.backupWG.Add(1)
		go func() {
			defer a.backupWG.Done()
			if _, err := a.backupService.MaybeBackup(backupCtx); err != nil && backupCtx.Err() == nil {
				log.Printf("App startup automatic database backup failed err=%v", err)
			}
		}()
	} else {
		log.Printf("App startup settings load failed err=%v", err)
	}
}

// beginBackupOperation 在 backupWG 上安全登记一次操作：关闭后拒绝新操作，
// 避免 Add 与 shutdown 的 Wait 并发（sync.WaitGroup 复用误用会 panic）。
func (a *App) beginBackupOperation() error {
	a.backupOpMu.Lock()
	defer a.backupOpMu.Unlock()
	if a.backupOpsClosed {
		return fmt.Errorf("应用正在退出，备份操作不可用")
	}
	a.backupWG.Add(1)
	return nil
}

func (a *App) shutdown(ctx context.Context) {
	if a.iinaProgress != nil {
		a.iinaProgress.StopWatching()
	}
	if a.backupCancel != nil {
		a.backupCancel()
	}
	a.backupOpMu.Lock()
	a.backupOpsClosed = true
	a.backupOpMu.Unlock()
	a.backupWG.Wait()
	if a.localMetadata != nil {
		a.localMetadata.StopExport()
		a.localMetadata.StopBackfill()
	}
	if a.libraryWatcher != nil {
		if err := a.libraryWatcher.Close(); err != nil {
			log.Printf("Library watcher shutdown failed: %v", err)
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
	if a.frameHash != nil {
		a.frameHash.StopAndWait()
	}
	if a.playbackProxies != nil {
		a.playbackProxies.StopAndWait()
	}
	if a.faceAnalysis != nil {
		a.faceAnalysis.StopAndWait()
	}
	if a.faceRuntime != nil {
		a.faceRuntime.StopAndWait()
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
	if a.collectionSuggestions != nil {
		a.collectionSuggestions.StopAndWait()
	}
	if a.aiTaggingService != nil {
		a.aiTaggingService.StopAndWait()
	}
	if a.shortFeedServer != nil {
		if err := a.shortFeedServer.Stop(ctx); err != nil {
			log.Printf("Short feed server shutdown failed: %v", err)
		}
	}
	a.closeLogFile()
}

func (a *App) startShortFeedServer(ctx context.Context) {
	distFS, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		a.shortFeedStartupError = fmt.Sprintf("短视频 Feed 前端资源不可用: %v", err)
		log.Printf("Short feed server not started: %s", a.shortFeedStartupError)
		return
	}
	a.shortFeedServer = services.NewShortFeedHTTPServer(a.shortFeedService, distFS, services.ShortFeedHTTPServerConfig{})
	a.shortFeedServer.Start(ctx)
	status := a.shortFeedServer.Status()
	if status.StartupError != "" {
		a.shortFeedStartupError = status.StartupError
		log.Printf("Short feed server startup failed: %s", status.StartupError)
		return
	}
	a.shortFeedStartupError = ""
	log.Printf("Short feed server running url=%s lan=%v", status.URL, status.LANURLs)
}

func (a *App) setStartupError(err error) {
	if err == nil {
		a.startupError = ""
		return
	}
	a.startupError = err.Error()
}

func (a *App) GetStartupError() string {
	log.Printf("API GetStartupError hasError=%v value=%q", a.startupError != "", a.startupError)
	return a.startupError
}

func (a *App) GetShortFeedServerStatus() services.ShortFeedServerStatus {
	if a.shortFeedServer == nil {
		return services.ShortFeedServerStatus{
			Running:       false,
			StartupError:  a.shortFeedStartupError,
			AllowedAccess: "loopback/private-lan/link-local only, no login",
		}
	}
	status := a.shortFeedServer.Status()
	if status.StartupError == "" && a.shortFeedStartupError != "" {
		status.StartupError = a.shortFeedStartupError
	}
	log.Printf("API GetShortFeedServerStatus running=%v port=%d err=%q", status.Running, status.Port, status.StartupError)
	return status
}

func (a *App) LogFrontend(level string, source string, message string) {
	level = strings.ToUpper(strings.TrimSpace(level))
	if level == "" {
		level = "INFO"
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "frontend"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	message = redactSensitiveLogMessage(message)
	log.Printf("[Frontend][%s][%s] %s", level, source, message)
}

// closeLogFile 关闭当前日志文件句柄（如果有）
func (a *App) closeLogFile() {
	if a.logFile != nil {
		a.logFile.Close()
		a.logFile = nil
	}
}

func (a *App) setLogEnabled(enabled bool) {
	if !enabled {
		log.SetOutput(io.Discard)
		a.closeLogFile()
		return
	}
	// dataDir 已经在 NewApp 中计算过，但这里再次获取也没问题
	if dataDir, err := appdata.Resolve(); err == nil {
		if _, err := os.Stat(dataDir); err != nil {
			_ = os.MkdirAll(dataDir, 0755)
		}
		logPath := filepath.Join(dataDir, "app.log")
		rotateLogIfNeeded(logPath)
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
			a.closeLogFile() // 先关闭旧句柄
			a.logFile = f
			log.SetOutput(f)
		}
	}
}

func redactSensitiveLogMessage(message string) string {
	redacted := message
	for _, pattern := range sensitiveLogPatterns {
		redacted = pattern.ReplaceAllString(redacted, "${1}[REDACTED]")
	}
	return redacted
}

func rotateLogIfNeeded(logPath string) {
	info, err := os.Stat(logPath)
	if err != nil || info.Size() < maxAppLogSizeBytes {
		return
	}
	oldest := fmt.Sprintf("%s.%d", logPath, maxAppLogBackups)
	_ = os.Remove(oldest)
	for index := maxAppLogBackups - 1; index >= 1; index-- {
		src := fmt.Sprintf("%s.%d", logPath, index)
		dst := fmt.Sprintf("%s.%d", logPath, index+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	_ = os.Rename(logPath, logPath+".1")
}

func (a *App) backgroundContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

func summarizeVideos(videos []models.Video, limit int) string {
	if len(videos) == 0 {
		return "[]"
	}
	if limit <= 0 || limit > len(videos) {
		limit = len(videos)
	}
	parts := make([]string, 0, limit+1)
	for index := 0; index < limit; index++ {
		video := videos[index]
		parts = append(parts, fmt.Sprintf("{id:%d name:%q path:%q tags:%d}", video.ID, video.Name, video.Path, len(video.Tags)))
	}
	if len(videos) > limit {
		parts = append(parts, fmt.Sprintf("...+%d more", len(videos)-limit))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func summarizeTags(tags []models.Tag, limit int) string {
	if len(tags) == 0 {
		return "[]"
	}
	if limit <= 0 || limit > len(tags) {
		limit = len(tags)
	}
	parts := make([]string, 0, limit+1)
	for index := 0; index < limit; index++ {
		tag := tags[index]
		parts = append(parts, fmt.Sprintf("{id:%d name:%q color:%q}", tag.ID, tag.Name, tag.Color))
	}
	if len(tags) > limit {
		parts = append(parts, fmt.Sprintf("...+%d more", len(tags)-limit))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func summarizeDirectories(dirs []models.ScanDirectory, limit int) string {
	if len(dirs) == 0 {
		return "[]"
	}
	if limit <= 0 || limit > len(dirs) {
		limit = len(dirs)
	}
	parts := make([]string, 0, limit+1)
	for index := 0; index < limit; index++ {
		dir := dirs[index]
		parts = append(parts, fmt.Sprintf("{id:%d alias:%q path:%q}", dir.ID, dir.Alias, dir.Path))
	}
	if len(dirs) > limit {
		parts = append(parts, fmt.Sprintf("...+%d more", len(dirs)-limit))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func summarizeSettings(settings *models.Settings) string {
	if settings == nil {
		return "<nil>"
	}
	return fmt.Sprintf("{id:%d theme:%q log_enabled:%v auto_scan:%v watch_enabled:%v play_weight:%.2f short_feed_max_minutes:%d bilingual:%v lang:%q}",
		settings.ID,
		settings.Theme,
		settings.LogEnabled,
		settings.AutoScanOnStartup,
		settings.LibraryWatchEnabled,
		settings.PlayWeight,
		settings.ShortFeedMaxDurationMinutes,
		settings.BilingualEnabled,
		settings.BilingualLang,
	)
}
