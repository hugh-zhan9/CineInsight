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
	watchlistService      *services.WatchlistService
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
	imagePHashBackfill  *services.ImagePerceptualHashBackfillService
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
	jellyfinServer        *services.JellyfinServer
	shortFeedStartupError string
	// 浏览器插件桥接（D-B03、D-B04）。与手机端 feed 服务是两条互不相干的通道：
	// feed 绑 0.0.0.0、只读、无鉴权；桥接只绑 127.0.0.1、要令牌，因为它能让
	// 桌面端按外部请求去取任意地址并往磁盘写文件。
	browserDownloads   *services.BrowserDownloadService
	browserBridge      *services.BrowserBridgeServer
	startupError       string
	logFile            *os.File // 保持日志文件句柄引用，防止泄漏
	backupCancel       context.CancelFunc
	backupWG           sync.WaitGroup
	backupOpMu         sync.Mutex
	backupOpsClosed    bool
	semanticMu         sync.RWMutex
	imageAITagMu       sync.RWMutex
	imageAITagging     *services.ImageAITaggingService
	imageAITagAutoMu   sync.Mutex // 串行化自动触发，防止启动与扫描后触发并发
	imageSemanticMu    sync.RWMutex
	imageSemanticIndex *services.ImageSemanticIndexService
	restoreMu          sync.Mutex
	restoreTerminal    bool
	restoreRelease     func()

	// 年度电影榜单（D-MC01..D-MC13）。与上面几个握着 *gorm.DB 的服务同理，它在
	// 数据库就绪后由 resetMovieChartService 构造、用锁换上，不在 NewApp 里建。
	movieChartMu sync.RWMutex
	movieChart   *services.MovieChartService
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
		watchlistService:      services.NewWatchlistService(dataDir),
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
		imagePHashBackfill:    services.NewImagePerceptualHashBackfillService(imageThumbnail),
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
	app.faceReview = services.NewFaceReviewService(dataDir, app.faceAnalysis)
	app.wireBrowserBridge()
	app.jellyfinServer = services.NewJellyfinServer(videoService, app.thumbnailService, mediaProbeService, personService, collectionService)
	app.wireBackgroundTaskRegistry()
	app.wireDesktopNotifier()
	app.wireServiceHooks()
	if dataDirErr != nil {
		app.setStartupError(dataDirErr)
	}
	return app
}

// wireBrowserBridge 构造下载队列与桥接服务（D-B03..D-B06）。
//
// 下载队列有意不接空闲门、也不占 MediaWorkSlot：门只挡自动触发的任务，而这些
// 是用户在浏览器里点出来的；`-c copy` 是网络与 IO 密集，占重媒体槽会被超分、
// 转封装那类长任务饿死。它自带并发上限。
func (a *App) wireBrowserBridge() {
	a.browserDownloads = services.NewBrowserDownloadService(services.BrowserDownloadDeps{
		Settings: func() (services.BrowserDownloadSettings, error) {
			settings, err := a.settingsService.GetSettings()
			if err != nil {
				return services.BrowserDownloadSettings{}, err
			}
			return services.BrowserDownloadSettings{
				Directory:   settings.BrowserDownloadDirectory,
				Concurrency: settings.BrowserDownloadConcurrency,
			}, nil
		},
		ImportDirectory: services.BrowserDownloadImporterFromScan(a.videoService, a.directoryService.GetAllDirectories),
		Tasks:           a.backgroundTasks,
	})
	a.browserBridge = services.NewBrowserBridgeServer(a.browserDownloads, settingsBridgeAuth{settings: a.settingsService})
	// 「用 IINA 播放」：把流直接交给本机播放器，不下载。请求头一起带过去——
	// 这类站点的 CDN 认 Referer，缺了直接 403。
	a.browserBridge.SetStreamPlayer(services.LaunchStreamInIINA)
	// 播放器传不进请求头，所以流先过一道本机代理，由代理去源站时加 Referer。
	a.browserBridge.SetStreamProxy(services.NewStreamProxy())
}

// wireBackgroundTaskRegistry 把全部长任务服务接进登记表（D-014）。
// 在 NewApp 里做而不是 startup：数据库恢复后重建的服务各自在重建处补接，
// 这里保证"构造出来就已登记"，不依赖启动顺序。
func (a *App) wireBackgroundTaskRegistry() {
	a.technicalBackfill.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.perceptualHash.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.cleanupService.SetBackgroundTaskRegistry(a.backgroundTasks)
	// 图片清理分析登记为 image_cleanup（D-PC51），任务中心与退出判定都读登记表。
	a.imageCleanupService.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.imageEXIFBackfill.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.imagePHashBackfill.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.localMetadata.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.aiTaggingService.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.subtitleService.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.enhancement.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.backupService.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.collectionSuggestions.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.frameHash.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.playbackProxies.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.faceAnalysis.SetBackgroundTaskRegistry(a.backgroundTasks)
	// 想看片单的在线补全（D-WM13）。它有意不接空闲门：补全由用户添加条目或点
	// 重试触发，属用户显式动作，与浏览器下载队列同口径。
	a.watchlistService.SetBackgroundTaskRegistry(a.backgroundTasks)
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

// wireServiceHooks 接上服务之间经 App 注入的回调（产品完善度批次的接线项）。与登记表一样在
// NewApp 里做：它们都是对象之间的引用，构造出来就接好，不依赖启动顺序。数据库就绪后才构造的
// 榜单服务那一侧见 rebuildMovieChartService；只能在 startup 里接的包级钩子见 wireStartupHooks。
func (a *App) wireServiceHooks() {
	// AI 打标 worker 的启动批次与定时轮次经空闲门，显式触发照常直通（D-PC19）。
	a.aiTaggingService.SetIdleGate(a.idleGate)
	// IINA 同步直接写库，已看翻转经 VideoService 转发给它持有的那一个观察者（D-PC52）。
	a.iinaProgress.SetWatchedNotifier(a.videoService.NotifyWatchStateChanged)
	// 视频 → 榜单：观察者每次回调时才取当前的榜单服务，榜单服务重建之后不必重新注入。
	a.videoService.SetWatchStateObserver(movieChartWatchObserver{app: a})
	// 撤销「标签转人物」删掉新建人物时一并删掉它的托管头像（D-PC34）。
	a.tagService.SetAvatarRemover(a.personService.RemoveManagedAvatar)
}

// movieChartWatchObserver 把视频的已看翻转转给当前的榜单服务（D-PC52、APP-05）。
// 榜单服务在数据库就绪后才构造，恢复失败续跑时还会重建；直接注入某一个实例的话，重建之后
// 观察者就指向一个已经收摊、握着旧连接的服务。
type movieChartWatchObserver struct{ app *App }

func (o movieChartWatchObserver) OnVideoWatchedChanged(videoID uint, watched bool) {
	if svc := o.app.movieChartService(); svc != nil {
		svc.OnVideoWatchedChanged(videoID, watched)
	}
}

// rebuildMovieChartService 重建榜单服务，并把榜单 → 视频的回写接到新实例上（D-PC52）。
// 回写口是实例字段，每个新实例都要接一次；反方向的观察者见 movieChartWatchObserver。
// 凡是调用 resetMovieChartService 的地方都应改调它。
func (a *App) rebuildMovieChartService() {
	a.resetMovieChartService()
	if svc := a.movieChartService(); svc != nil {
		svc.SetLinkedVideoWatchSetter(a.videoService)
	}
}

// markInterruptedSubtitleJobs 把上一次进程退出时还在排队或运行的字幕任务标成 interrupted（D-PC20）。
// 必须在数据库就绪之后、前端加载之前调用：darwin 上 OnStartup 与页面加载并发，放进 startup 的话
// 前端可能先读到「没有中断任务」，那条提示就丢了。本进程里已经入队的任务不会被标记。
func (a *App) markInterruptedSubtitleJobs() {
	if a.subtitleService == nil {
		return
	}
	count, err := a.subtitleService.MarkInterruptedSubtitleJobs()
	if err != nil {
		log.Printf("App startup mark interrupted subtitle jobs failed err=%v", err)
		return
	}
	if count > 0 {
		log.Printf("App startup marked interrupted subtitle jobs count=%d", count)
	}
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
//
// darwin 上 Wails 在 goroutine 里调 OnStartup，与页面加载并发：前端可能在这里跑完之前就调用
// 绑定。必须赶在前端之前做完的事（字幕中断标记）放在 main.go 的 wails.Run 之前。
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
	a.wireStartupHooks(emit)
	a.resetSemanticIndexService()
	a.resetImageSemanticIndexService()
	a.resetImageAITaggingService()
	// 年度电影榜单：读接口不出网，但它得握着数据库连接才能读缓存，所以与上面
	// 几个一样等数据库就绪后再构造。
	a.rebuildMovieChartService()
	// 对账可能包含大文件 SHA-256，异步执行避免阻塞窗口可用。孤儿工作目录的清扫跟在对账之后、
	// 同一个 goroutine 里：对账先把中断的任务续上或收尾，之后才判得准哪些目录已经没有任务要用。
	go func() {
		a.enhancement.RecoverOnStartup(ctx)
		result, err := a.enhancement.SweepOrphanWorkdirs(ctx)
		if err != nil {
			log.Printf("App startup enhancement workdir sweep failed err=%v", err)
			return
		}
		if result.Found > 0 || result.Errors > 0 {
			log.Printf("App startup enhancement workdir sweep found=%d removed=%d kept=%d errors=%d",
				result.Found, result.Removed, result.Kept, result.Errors)
		}
	}()
	// 外部播放多半发生在应用没开着的时候，启动时先补一次 IINA 断点；
	// 之后靠监听断点目录实时跟进，看完切回来就已经更新好了，不用重启应用。
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
	a.aiTaggingService.Start(ctx)
	// 想看片单的在线补全：先把进程重启后残留的 running 刷回 pending，再起 worker。
	a.watchlistService.StartEnrichment(ctx)
	// 启动时后台增量生成图片描述（仅处理尚无描述的图，配置缺失则静默跳过）。
	go a.triggerImageAITaggingAuto("startup")
	// 手机端服务的启停一律经生命周期锁，与设置页开关、数据库维护互斥；开关关着时不监听（D-PC45）。
	if err := a.withShortFeedLifecycle(func() error { return a.restartShortFeedServerLocked(ctx) }); err != nil {
		log.Printf("App startup short feed server not started err=%v", err)
	}
	a.jellyfinServer.Start()
	a.startBrowserBridge(ctx)
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
		// 常驻期间按「自动备份间隔」继续检查（D-PC56）。同样登记在 backupWG 上：shutdown 先取消
		// backupCtx 再 Wait，不会有一轮备份与关库赛跑。
		a.backupWG.Add(1)
		go func() {
			defer a.backupWG.Done()
			a.backupService.StartPeriodic(backupCtx)
		}()
	} else {
		log.Printf("App startup settings load failed err=%v", err)
	}
	// 「无字幕」视图与 Jellyfin 的 HasSubtitles 都读 has_sidecar，而它只在全库同步时写入
	// （D-PC17）：启动完成后安排一轮。同步在后台跑，完成后发 subtitle-index-synced。
	if _, err := a.subtitleSearchService.SyncSubtitleIndexNow(); err != nil {
		log.Printf("App startup subtitle index sync not scheduled err=%v", err)
	}
}

// wireStartupHooks 接上只能在 startup 里接的回调：发给前端的事件（要 Wails 的运行时上下文）
// 与 services 的包级钩子。startup 传入 emit，测试传记录函数。
func (a *App) wireStartupHooks(emit func(event string, data any)) {
	// 任务中心的输入变了（D-PC18）：登记表、空闲门与各后台任务的状态事件之后，由
	// notifyTaskCenterChanged 合并 500ms 发一次 task-center-changed。字幕队列与视频清理的进度
	// 由服务直接发事件，App 层收不到，前端自己监听那两个事件。
	emitTaskState := func(event string, data any) {
		emit(event, data)
		a.notifyTaskCenterChanged()
	}
	// 登记表变化驱动三件事：清掉已跑完任务的一次性 bypass、刷 Dock 角标（D-014），
	// 再广播给前端。角标不看开关也不看前后台，它就是"现在几个任务在跑"。
	a.backgroundTasks.SetOnChange(func(running []string) {
		a.idleGate.SyncRunningTasks(running)
		a.desktopNotify.SetBadge(services.BadgeLabelForCount(len(running)))
		emitTaskState("background-tasks", running)
	})
	a.idleGate.SetEventEmitter(func(status services.IdleSchedulerStatus) {
		emitTaskState("idle-scheduler-state", status)
	})
	a.technicalBackfill.SetEventEmitter(func(status services.TechnicalBackfillStatus) {
		emitTaskState("technical-backfill-state", status)
	})
	// 维护与待重启会让任务中心改报「暂时无法读取」，所以切换进度同样触发一次快照。
	a.databaseSwitchService.SetProgressSink(func(status services.DatabaseSwitchStatus) {
		emitTaskState("database-switch-state", status)
	})
	a.imageCleanupService.SetEventEmitter(func(progress services.ImageCleanupProgress) {
		emitTaskState("image-cleanup-progress", progress)
	})
	a.imageEXIFBackfill.SetEventEmitter(func(status services.ImageEXIFBackfillStatus) {
		emitTaskState("image-exif-backfill-progress", status)
	})
	a.imagePHashBackfill.SetEventEmitter(func(status services.ImagePerceptualHashBackfillStatus) {
		// 补出新指纹意味着近似重复的输入变了，缓存的清理结果要标为可能过期。
		if status.Completed && status.Succeeded > 0 {
			a.imageCleanupService.InvalidateAnalysis()
		}
		emitTaskState("image-perceptual-hash-backfill-progress", status)
	})
	a.perceptualHash.SetEventEmitter(func(status services.PerceptualHashStatus) {
		if status.Completed && status.Succeeded > 0 {
			a.cleanupService.InvalidateAnalysis()
		}
		emitTaskState("perceptual-hash-state", status)
	})
	a.frameHash.SetEventEmitter(func(status services.FrameHashStatus) {
		// 新算出来的序列会改变"截取片段"这一类候选，缓存的清理结果因此可能过期。
		// 与感知哈希同口径：只标记过期，不静默重跑。
		if status.Completed && status.Succeeded > 0 {
			a.cleanupService.InvalidateAnalysis()
		}
		emitTaskState("frame-hash-backfill-state", status)
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
	a.browserDownloads.SetEventEmitter(func(tasks []services.BrowserDownloadTask) {
		emitTaskState("browser-download-tasks", tasks)
	})
	a.watchlistService.SetEnrichmentEventEmitter(func(progress services.WatchlistEnrichProgress) {
		emitTaskState("watchlist-enrich-progress", progress)
	})
	a.localMetadata.SetBackfillEventEmitter(func(status services.LocalMetadataBackfillStatus) {
		emitTaskState("local-metadata-backfill", status)
	})
	a.localMetadata.SetExportEventEmitter(func(status services.LocalMetadataExportStatus) {
		emitTaskState("local-metadata-export", status)
	})
	a.collectionSuggestions.SetEventEmitter(func(status services.CollectionSuggestionStatus) {
		emitTaskState("collection-suggestion-state", status)
	})
	a.playbackProxies.SetEventEmitter(func(status services.PlaybackProxyStatus) {
		emitTaskState("playback-proxy-state", status)
	})
	a.faceRuntime.SetEventEmitter(func(status services.FaceRuntimeStatus) {
		emit("face-runtime-state", status)
	})
	a.faceAnalysis.SetEventEmitter(func(status services.FaceAnalysisStatus) {
		emitTaskState("face-analysis-state", status)
	})
	a.enhancement.SetEventEmitter(func(view services.EnhancementTaskView) {
		emitTaskState("video-enhancement-state", view)
	})
	a.enhancementModels.SetEventEmitter(func(status services.EnhancementModelStatus) {
		emit("enhancement-model-state", status)
	})
	// 模型装好后立刻重探能力并广播，界面不用重启就能看到"可用"。
	a.enhancementModels.SetOnInstalled(func() {
		emit("video-enhancement-capability", a.enhancement.RefreshCapability())
	})
	a.iinaProgress.SetOnSynced(func(result services.IINAProgressSyncResult) {
		emit("iina-progress-synced", result)
	})
	// 正式播放启动成功后登记 IINA 会话，断点文件被删时据此结算看完与否（D-PC41）。
	services.SetPlaybackLaunchedHook(a.iinaProgress.OnPlaybackLaunched)
	// 播放失败后的后台重定位找到新位置时通知前端，就地更新那一行（D-PC11）。
	services.SetVideoRelocatedNotifier(func(event services.VideoRelocatedEvent) {
		emit("video-relocated", event)
	})
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
	// 先取消进行中的迁移，再拿 restoreMu：迁移 goroutine 全程持有这把锁，不先取消，退出就要
	// 等整个迁移跑完。取消按失败处理（配置不改，目标库可清空重试）。
	a.cancelDatabaseSwitchForShutdown()
	a.restoreMu.Lock()
	a.restoreTerminal = true
	a.restoreMu.Unlock()
	// 播放失败后的后台重定位会改路径写库：取消排队与进行中的，等它们退出。
	services.StopPlaybackRelocation()
	// 30 秒「换一个」窗口里还没提交的那次随机播放照样记账（D-PC43）。数据库由 main 在
	// wails.Run 返回之后才关闭，这里一定早于关库。
	services.FlushPendingRandomCommit()
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
	if a.collectionSuggestions != nil {
		a.collectionSuggestions.StopAndWait()
	}
	if a.aiTaggingService != nil {
		a.aiTaggingService.StopAndWait()
	}
	if a.watchlistService != nil {
		a.watchlistService.StopEnrichmentAndWait()
	}
	if svc := a.movieChartService(); svc != nil {
		// 必须等它真的收摊：详情阶段是 1 次/秒的限速循环，不取消就会在退出途中
		// 继续把整年剩下的请求发出去；半途被杀还会在库里留下 running 的认领行，
		// 要等下一次刷新的 resetYearDetailBacklog 才放得回队列。
		svc.StopRefreshAndWait()
	}
	// 与设置页开关、数据库维护同一把生命周期锁，不会与一次进行中的启停交错。
	if err := a.withShortFeedLifecycle(a.stopShortFeedForSetting); err != nil {
		log.Printf("Short feed server shutdown failed: %v", err)
	}
	if a.browserBridge != nil {
		if err := a.browserBridge.Stop(ctx); err != nil {
			log.Printf("Browser bridge shutdown failed: %v", err)
		}
	}
	if a.jellyfinServer != nil {
		a.jellyfinServer.Stop()
	}
	if a.browserDownloads != nil {
		// 等在跑的 ffmpeg 收完摊。不等的话，任务的清理（删掉没下完的 .part）
		// 会和进程退出赛跑，输给它就在下载目录里留下垃圾。
		a.browserDownloads.Wait()
	}
	a.closeLogFile()
}

// startShortFeedServer 起手机端服务。调用方必须持有手机端生命周期锁（withShortFeedLifecycle，
// 一般经 restartShortFeedServerLocked）。开关关着时不监听端口（D-PC45）：判定放在启动函数里，
// 不依赖每个调用点各自记得检查。
func (a *App) startShortFeedServer(ctx context.Context) {
	if !a.shortFeedService.ShouldStart() {
		a.shortFeedStartupError = ""
		log.Printf("Short feed server not started: mobile access is switched off")
		return
	}
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

// GetShortFeedServerStatus 返回手机端服务的监听状态。服务实例与启动错误都在生命周期锁内读：
// 开关、恢复续跑随时可能把实例换掉。
func (a *App) GetShortFeedServerStatus() services.ShortFeedServerStatus {
	var status services.ShortFeedServerStatus
	_ = a.withShortFeedLifecycle(func() error {
		if a.shortFeedServer == nil {
			// 没在监听（开关关着或启动失败）：访问范围文案与运行中的服务同一口径（按是否已设 PIN），
			// 由一个未启动的服务实例算出，不在这里另写一份。
			idle := services.NewShortFeedHTTPServer(a.shortFeedService, nil, services.ShortFeedHTTPServerConfig{}).Status()
			status = services.ShortFeedServerStatus{
				Running:       false,
				StartupError:  a.shortFeedStartupError,
				AllowedAccess: idle.AllowedAccess,
			}
			return nil
		}
		status = a.shortFeedServer.Status()
		if status.StartupError == "" && a.shortFeedStartupError != "" {
			status.StartupError = a.shortFeedStartupError
		}
		return nil
	})
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
