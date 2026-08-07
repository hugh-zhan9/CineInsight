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
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
	"video-master/services/subtitleparser"

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
	videoDetailService    *services.VideoDetailService
	libraryStatsService   *services.LibraryStatsService
	localMetadata         *services.LocalMetadataService
	mediaProbeService     *services.MediaProbeService
	technicalBackfill     *services.TechnicalBackfillService
	perceptualHash        *services.PerceptualHashService
	enhancement           *services.EnhancementService
	semanticIndex         *services.SemanticIndexService
	libraryWatcher        *services.LibraryWatcherService
	imageService          *services.ImageService
	imageEXIFBackfill     *services.ImageEXIFBackfillService
	imageThumbnail        *services.ImageThumbnailService
	imageLibraryService   *services.ImageLibraryService
	imageStatsService     *services.ImageStatsService
	imageCleanupService   *services.ImageCleanupService
	shortFeedServer       *services.ShortFeedHTTPServer
	shortFeedStartupError string
	startupError          string
	logFile               *os.File // 保持日志文件句柄引用，防止泄漏
	backupCancel          context.CancelFunc
	backupWG              sync.WaitGroup
	backupOpMu            sync.Mutex
	backupOpsClosed       bool
	semanticMu            sync.RWMutex
	imageAIDescMu         sync.RWMutex
	imageAIDescription    *services.ImageAIDescriptionService
	imageSemanticMu       sync.RWMutex
	imageSemanticIndex    *services.ImageSemanticIndexService
	restoreMu             sync.Mutex
	restoreTerminal       bool
	restoreRelease        func()
}

// NewApp creates a new App application struct
func NewApp() *App {
	// 获取用户目录作为数据根目录
	homeDir, _ := os.UserHomeDir()
	dataDir := filepath.Join(homeDir, ".video-master")
	mediaProbeService := services.NewMediaProbeService()
	videoService := services.NewVideoService(mediaProbeService)
	libraryWatcher := services.NewLibraryWatcherService(videoService)
	subtitleService := services.NewSubtitleService(dataDir)
	aiTaggingService := services.NewAITaggingService()
	aiTaggingService.SetTemporaryTranscriptProvider(subtitleService)

	personService := services.NewPersonService(dataDir)
	collectionService := services.NewCollectionService(dataDir)
	localMetadata := services.NewLocalMetadataService(dataDir, personService, collectionService)
	return &App{
		videoService:          videoService,
		thumbnailService:      services.NewThumbnailService(videoService, dataDir),
		tagService:            &services.TagService{},
		settingsService:       &services.SettingsService{},
		backupService:         services.NewBackupService(dataDir),
		directoryService:      &services.DirectoryService{},
		subtitleService:       subtitleService,
		subtitleWorkbench:     services.NewSubtitleWorkbenchService(subtitleService),
		cleanupService:        &services.CleanupService{},
		subtitleSearchService: &services.SubtitleSearchService{},
		aiTaggingService:      aiTaggingService,
		aiQualityService:      services.NewAIQualityService(),
		shortFeedService:      services.NewShortFeedService(videoService),
		personService:         personService,
		collectionService:     collectionService,
		videoDetailService:    services.NewVideoDetailService(personService, collectionService),
		libraryStatsService:   services.NewLibraryStatsService(),
		localMetadata:         localMetadata,
		mediaProbeService:     mediaProbeService,
		technicalBackfill:     services.NewTechnicalBackfillService(mediaProbeService),
		perceptualHash:        services.NewPerceptualHashService(),
		enhancement:           services.NewEnhancementService(videoService, mediaProbeService, aiTaggingService.SameSourceService()),
		libraryWatcher:        libraryWatcher,
		imageService:          services.NewImageService(),
		imageEXIFBackfill:     services.NewImageEXIFBackfillService(),
		imageThumbnail:        services.NewImageThumbnailService(dataDir),
		imageLibraryService:   services.NewImageLibraryService(),
		imageStatsService:     services.NewImageStatsService(),
		imageCleanupService:   services.NewImageCleanupService(),
	}
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
	a.resetSemanticIndexService()
	a.resetImageSemanticIndexService()
	a.resetImageAIDescriptionService()
	a.technicalBackfill.SetEventEmitter(func(status services.TechnicalBackfillStatus) {
		emit("technical-backfill-state", status)
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
	a.libraryWatcher.SetEventEmitters(func(status services.LibraryWatcherStatus) {
		emit("library-watcher-status", status)
	}, func(event services.LibraryReconcileEvent) {
		if event.Result != nil && (event.Result.Added > 0 || event.Result.Relocated > 0 || event.Result.Stale > 0 || event.Result.MetadataRefreshed > 0) {
			a.cleanupService.InvalidateAnalysis()
		}
		if event.Result != nil && event.Result.Added > 0 {
			a.aiTaggingService.Trigger()
		}
		emit("library-watcher-reconciled", event)
	})
	a.localMetadata.SetBackfillEventEmitter(func(status services.LocalMetadataBackfillStatus) {
		emit("local-metadata-backfill", status)
	})
	a.localMetadata.SetExportEventEmitter(func(status services.LocalMetadataExportStatus) {
		emit("local-metadata-export", status)
	})
	a.enhancement.SetEventEmitter(func(view services.EnhancementTaskView) {
		emit("video-enhancement-state", view)
	})
	// 对账可能包含大文件 SHA-256，异步执行避免阻塞窗口可用。
	go a.enhancement.RecoverOnStartup(ctx)
	a.cleanupService.SetContext(ctx)
	if result, err := a.tagService.SyncShortVideoTags(); err != nil {
		log.Printf("App startup short-video tag sync failed err=%v", err)
	} else {
		log.Printf("App startup short-video tag sync tag=%d added=%d removed=%d", result.TagID, result.Added, result.Removed)
	}
	if result, err := a.shortFeedService.SyncFeedback(); err != nil {
		log.Printf("App startup short-feed feedback sync failed err=%v", err)
	} else if result.Enabled {
		log.Printf("App startup short-feed feedback sync tag=%d likes_added=%d likes_removed=%d favorites_added=%d", result.TagID, result.LikesAdded, result.LikesRemoved, result.FavoritesAdded)
	}
	a.aiTaggingService.Start(ctx)
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

// semanticIndexService 以读锁返回当前语义索引服务指针；恢复失败路径会
// 重建该指针，绑定读取与重建之间需要同步。
func (a *App) semanticIndexService() *services.SemanticIndexService {
	a.semanticMu.RLock()
	defer a.semanticMu.RUnlock()
	return a.semanticIndex
}

func (a *App) shutdown(ctx context.Context) {
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
	if a.imageEXIFBackfill != nil {
		a.imageEXIFBackfill.StopAndWait()
	}
	if svc := a.semanticIndexService(); svc != nil {
		svc.StopAndWait()
	}
	if svc := a.imageAIDescriptionService(); svc != nil {
		svc.StopAndWait()
	}
	if svc := a.imageSemanticIndexService(); svc != nil {
		svc.StopAndWait()
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
	if homeDir, err := os.UserHomeDir(); err == nil {
		dataDir := filepath.Join(homeDir, ".video-master")
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

// ===== Video Methods =====

// GetAllVideos 获取所有视频（保持兼容，实际使用分页）
func (a *App) GetAllVideos() ([]models.Video, error) {
	return a.videoService.GetAllVideos()
}

// GetVideosPaginated 分页获取视频
func (a *App) GetVideosPaginated(cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.GetVideosPaginated(cursorScore, cursorSize, cursorID, limit)
	log.Printf("API GetVideosPaginated cursorScore=%.4f cursorSize=%d cursorID=%d limit=%d result=%d err=%v sample=%s", cursorScore, cursorSize, cursorID, limit, len(videos), err, summarizeVideos(videos, 3))
	return videos, err
}

// SearchVideos 搜索视频（支持分页）
func (a *App) SearchVideos(keyword string, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.SearchVideos(keyword, cursorScore, cursorSize, cursorID, limit)
	log.Printf("API SearchVideos keyword=%q cursorScore=%.4f cursorSize=%d cursorID=%d limit=%d result=%d err=%v sample=%s", keyword, cursorScore, cursorSize, cursorID, limit, len(videos), err, summarizeVideos(videos, 3))
	return videos, err
}

// SearchSubtitleMatches 按字幕内容搜索视频片段
func (a *App) SearchSubtitleMatches(keyword string, limit int) ([]services.SubtitleSearchMatch, error) {
	matches, err := a.subtitleSearchService.SearchSubtitleMatches(keyword, limit)
	log.Printf("API SearchSubtitleMatches keyword=%q limit=%d result=%d err=%v", keyword, limit, len(matches), err)
	return matches, err
}

// SearchSubtitleMatchesWithFilters 按字幕内容及视频属性搜索视频片段。
func (a *App) SearchSubtitleMatchesWithFilters(keyword string, tagIDs []uint, minSize, maxSize int64, minHeight, maxHeight, limit int) ([]services.SubtitleSearchMatch, error) {
	matches, err := a.subtitleSearchService.SearchSubtitleMatchesWithFilters(keyword, services.SubtitleSearchFilters{
		TagIDs: tagIDs, MinSize: minSize, MaxSize: maxSize, MinHeight: minHeight, MaxHeight: maxHeight, Limit: limit,
	})
	log.Printf("API SearchSubtitleMatchesWithFilters keyword=%q tags=%v size=[%d,%d] height=[%d,%d] limit=%d result=%d err=%v", keyword, tagIDs, minSize, maxSize, minHeight, maxHeight, limit, len(matches), err)
	return matches, err
}

// SearchVideosByTags 按标签搜索视频（多选 AND，支持分页）
func (a *App) SearchVideosByTags(tagIDs []uint, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.SearchVideosByTags(tagIDs, cursorScore, cursorSize, cursorID, limit)
	log.Printf("API SearchVideosByTags tags=%v cursorScore=%.4f cursorSize=%d cursorID=%d limit=%d result=%d err=%v", tagIDs, cursorScore, cursorSize, cursorID, limit, len(videos), err)
	return videos, err
}

// SearchVideosWithFilters 组合搜索视频（名称 + 标签 + 体积 + 分辨率，支持分页）
func (a *App) SearchVideosWithFilters(keyword string, tagIDs []uint, minSize, maxSize int64, minHeight, maxHeight int, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.SearchVideosWithFilters(keyword, tagIDs, minSize, maxSize, minHeight, maxHeight, cursorScore, cursorSize, cursorID, limit)
	log.Printf("API SearchVideosWithFilters keyword=%q tags=%v size=[%d,%d] height=[%d,%d] cursorScore=%.4f cursorSize=%d cursorID=%d limit=%d result=%d err=%v sample=%s", keyword, tagIDs, minSize, maxSize, minHeight, maxHeight, cursorScore, cursorSize, cursorID, limit, len(videos), err, summarizeVideos(videos, 3))
	return videos, err
}

// SearchLibraryVideos 使用主片库智能视图与筛选条件查询视频。
func (a *App) SearchLibraryVideos(filter services.LibraryFilter, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.SearchLibraryVideos(filter, cursorScore, cursorSize, cursorID, limit)
	log.Printf("API SearchLibraryVideos view=%s mode=%s count=%d err=%v", filter.SmartView, filter.SearchMode, len(videos), err)
	return videos, err
}

// ListRecentlyPlayed 返回最近正式播放的视频。
func (a *App) ListRecentlyPlayed(limit int) ([]models.Video, error) {
	videos, err := a.videoService.ListRecentlyPlayed(limit)
	log.Printf("API ListRecentlyPlayed count=%d err=%v", len(videos), err)
	return videos, err
}

// ListRecentlyPlayedWithFilter 按当前片库条件稳定分页返回最近播放视频。
func (a *App) ListRecentlyPlayedWithFilter(filter services.LibraryFilter, cursorLastPlayedAt string, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.ListRecentlyPlayedWithFilter(filter, cursorLastPlayedAt, cursorID, limit)
	log.Printf("API ListRecentlyPlayedWithFilter cursorLastPlayedAt=%q cursorID=%d count=%d err=%v", cursorLastPlayedAt, cursorID, len(videos), err)
	return videos, err
}

// GetLibrarySubtitleHits 为当前片库页补充首个字幕命中片段。
func (a *App) GetLibrarySubtitleHits(keyword string, videoIDs []uint) ([]services.LibrarySubtitleHit, error) {
	hits, err := a.videoService.GetLibrarySubtitleHits(keyword, videoIDs)
	log.Printf("API GetLibrarySubtitleHits videos=%d hits=%d err=%v", len(videoIDs), len(hits), err)
	return hits, err
}

func (a *App) SearchLibraryVideoPage(request services.LibraryVideoPageRequest) (*services.LibraryVideoPage, error) {
	return a.videoService.SearchLibraryVideoPage(request.Filter, request.Cursor, request.Limit)
}

func (a *App) GetVideoDetails(videoID uint) (*services.VideoDetails, error) {
	return a.videoDetailService.GetVideoDetails(videoID)
}

func (a *App) GetLibraryInsights() (*services.LibraryStats, error) {
	return a.libraryStatsService.GetStats()
}

func (a *App) UpdateVideoDetails(input services.VideoDetailsUpdate) (*services.VideoDetails, error) {
	return a.videoDetailService.UpdateVideoDetails(input)
}

func (a *App) RefreshVideoTechnicalMetadata(videoID uint) (*services.VideoDetails, error) {
	if err := a.videoService.RefreshVideoMetadata(videoID); err != nil {
		return nil, err
	}
	return a.videoDetailService.GetVideoDetails(videoID)
}

func (a *App) GetLocalMetadataDiff(videoID uint) (*services.LocalMetadataDiff, error) {
	return a.localMetadata.GetDiff(videoID)
}

func (a *App) ApplyLocalMetadata(request services.LocalMetadataApplyRequest) (*services.LocalMetadataApplyResult, error) {
	result, err := a.localMetadata.Apply(request)
	if err == nil && a.cleanupService != nil {
		a.cleanupService.InvalidateAnalysis()
	}
	return result, err
}

func (a *App) PreviewLocalMetadataBatch(videoIDs []uint) services.LocalMetadataBatchPreview {
	return a.localMetadata.PreviewBatch(videoIDs)
}

func (a *App) ApplyLocalMetadataBatch(request services.LocalMetadataBatchApplyRequest) services.LocalMetadataBatchResult {
	result := a.localMetadata.ApplyBatch(request)
	if result.Succeeded > 0 && a.cleanupService != nil {
		a.cleanupService.InvalidateAnalysis()
	}
	return result
}

func (a *App) StartLocalMetadataBackfill() (services.LocalMetadataBackfillStatus, error) {
	settings, err := a.settingsService.GetSettings()
	if err != nil {
		return services.LocalMetadataBackfillStatus{}, err
	}
	if !settings.LocalMetadataEnabled {
		return services.LocalMetadataBackfillStatus{}, fmt.Errorf("本地元数据自动补全已关闭")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.localMetadata.StartBackfill(ctx)
}

func (a *App) GetLocalMetadataBackfillStatus() services.LocalMetadataBackfillStatus {
	return a.localMetadata.BackfillStatus()
}

func (a *App) CancelLocalMetadataBackfill() error {
	return a.localMetadata.CancelBackfill()
}

func (a *App) ExportLocalMetadataNFO(videoID uint) (*services.LocalMetadataNFOExportResult, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.localMetadata.ExportVideoNFO(ctx, videoID)
}

func (a *App) StartLocalMetadataExport(request services.LocalMetadataExportRequest) (services.LocalMetadataExportStatus, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.localMetadata.StartExport(ctx, request)
}

func (a *App) GetLocalMetadataExportStatus() services.LocalMetadataExportStatus {
	return a.localMetadata.ExportStatus()
}

func (a *App) CancelLocalMetadataExport() error {
	return a.localMetadata.CancelExport()
}

func (a *App) ResolveVideoArtwork(videoID uint, kind string) (*services.VideoArtworkData, error) {
	return a.localMetadata.ResolveVideoArtwork(videoID, kind)
}

func (a *App) StartTechnicalBackfill() (services.TechnicalBackfillStatus, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.technicalBackfill.Start(ctx)
}

func (a *App) GetTechnicalBackfillStatus() services.TechnicalBackfillStatus {
	return a.technicalBackfill.Status()
}

func (a *App) CancelTechnicalBackfill() error {
	return a.technicalBackfill.Cancel()
}

func (a *App) StartPerceptualHashBackfill() (services.PerceptualHashStatus, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.perceptualHash.Start(ctx)
}

func (a *App) GetPerceptualHashBackfillStatus() services.PerceptualHashStatus {
	return a.perceptualHash.Status()
}

func (a *App) CancelPerceptualHashBackfill() error {
	return a.perceptualHash.Cancel()
}

func (a *App) StartSemanticIndex(request services.SemanticIndexBuildRequest) (services.SemanticIndexStatus, error) {
	svc := a.semanticIndexService()
	if svc == nil {
		return services.SemanticIndexStatus{}, services.ErrSemanticIndexUnavailable
	}
	// 全局互斥（D-010）：图片语义索引运行中时拒绝视频任务。
	if image := a.imageSemanticIndexService(); image != nil && image.Status().Running {
		return services.SemanticIndexStatus{}, fmt.Errorf("已有语义索引任务运行中（图片），请先取消后再启动视频索引")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return svc.Start(ctx, request)
}

func (a *App) GetSemanticIndexStatus() services.SemanticIndexStatus {
	svc := a.semanticIndexService()
	if svc == nil {
		return services.SemanticIndexStatus{Available: false, Unavailable: "数据库未初始化"}
	}
	return svc.Status()
}

func (a *App) CancelSemanticIndex() error {
	svc := a.semanticIndexService()
	if svc == nil {
		return services.ErrSemanticIndexUnavailable
	}
	return svc.Cancel()
}

func (a *App) SearchSemanticVideos(request services.SemanticSearchRequest) (*services.SemanticSearchPage, error) {
	svc := a.semanticIndexService()
	if svc == nil {
		return nil, services.ErrSemanticIndexUnavailable
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return svc.Search(ctx, request)
}

func (a *App) FindSimilarVideos(request services.SemanticSimilarRequest) (*services.SemanticSearchPage, error) {
	svc := a.semanticIndexService()
	if svc == nil {
		return nil, services.ErrSemanticIndexUnavailable
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return svc.FindSimilar(ctx, request)
}

// ListPeople returns stable local person candidates and their active video counts.
func (a *App) ListPeople(keyword, cursorName string, cursorID uint, limit int) ([]services.PersonListItem, error) {
	return a.personService.ListPeople(keyword, cursorName, cursorID, limit)
}

func (a *App) GetPersonDetail(personID, cursorVideoID uint, limit int) (*services.PersonDetail, error) {
	return a.personService.GetPersonDetail(personID, cursorVideoID, limit)
}

func (a *App) CreatePerson(displayName, originalName string) (*models.Person, error) {
	return a.personService.CreatePerson(displayName, originalName)
}

func (a *App) UpdatePerson(personID uint, displayName, originalName string) (*models.Person, error) {
	return a.personService.UpdatePerson(personID, displayName, originalName)
}

func (a *App) AddPersonVideo(personID, videoID uint) error {
	return a.personService.AddPersonVideo(personID, videoID)
}

func (a *App) AddPersonVideos(personID uint, videoIDs []uint) error {
	return a.personService.AddPersonVideos(personID, videoIDs)
}

func (a *App) RemovePersonVideo(personID, videoID uint) (bool, error) {
	return a.personService.RemovePersonVideo(personID, videoID)
}

func (a *App) SelectPersonAvatar() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择人物头像",
		Filters: []runtime.FileFilter{
			{DisplayName: "图片 (*.jpg;*.jpeg;*.png;*.webp)", Pattern: "*.jpg;*.jpeg;*.png;*.webp"},
		},
	})
}

func (a *App) SetPersonAvatar(personID uint, sourcePath string) (*models.Person, error) {
	return a.personService.SetPersonAvatar(personID, sourcePath)
}

func (a *App) RemovePersonAvatar(personID uint) error {
	return a.personService.RemovePersonAvatar(personID)
}

func (a *App) ListCollections(keyword, cursorName string, cursorID uint, limit int) ([]services.CollectionListItem, error) {
	return a.collectionService.ListCollections(keyword, cursorName, cursorID, limit)
}

func (a *App) GetCollectionDetail(collectionID uint) (*services.CollectionDetail, error) {
	return a.collectionService.GetCollectionDetail(collectionID)
}

func (a *App) CreateCollection(name, description string) (*models.MediaCollection, error) {
	return a.collectionService.CreateCollection(name, description)
}

func (a *App) UpdateCollection(collectionID uint, name, description string) (*models.MediaCollection, error) {
	return a.collectionService.UpdateCollection(collectionID, name, description)
}

func (a *App) DeleteCollection(collectionID uint) error {
	return a.collectionService.DeleteCollection(collectionID)
}

func (a *App) AddCollectionVideo(collectionID, videoID uint) error {
	return a.collectionService.AddCollectionVideo(collectionID, videoID)
}

func (a *App) AddCollectionVideos(collectionID uint, videoIDs []uint) error {
	return a.collectionService.AddCollectionVideos(collectionID, videoIDs)
}

func (a *App) RemoveCollectionVideo(collectionID, videoID uint) error {
	return a.collectionService.RemoveCollectionVideo(collectionID, videoID)
}

func (a *App) ReorderCollectionVideos(collectionID uint, activeVideoIDs []uint) error {
	return a.collectionService.ReorderCollectionVideos(collectionID, activeVideoIDs)
}

func (a *App) SelectCollectionCover() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择作品集封面",
		Filters: []runtime.FileFilter{
			{DisplayName: "图片 (*.jpg;*.jpeg;*.png;*.webp)", Pattern: "*.jpg;*.jpeg;*.png;*.webp"},
		},
	})
}

func (a *App) SetCollectionCover(collectionID uint, sourcePath string) (*models.MediaCollection, error) {
	return a.collectionService.SetCollectionCover(collectionID, sourcePath)
}

func (a *App) RemoveCollectionCover(collectionID uint) error {
	return a.collectionService.RemoveCollectionCover(collectionID)
}

// SelectDirectory 选择目录对话框
func (a *App) SelectDirectory() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择视频目录",
	})
	return dir, err
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

// ListSavedLibraryViews 返回用户保存的片库筛选。
func (a *App) ListSavedLibraryViews() ([]models.SavedLibraryView, error) {
	views, err := a.videoService.ListSavedLibraryViews()
	log.Printf("API ListSavedLibraryViews count=%d err=%v", len(views), err)
	return views, err
}

// SaveLibraryView 创建用户命名的片库筛选。
func (a *App) SaveLibraryView(input services.SavedLibraryViewInput) (*models.SavedLibraryView, error) {
	view, err := a.videoService.SaveLibraryView(input)
	log.Printf("API SaveLibraryView name=%q err=%v", input.Name, err)
	return view, err
}

// DeleteSavedLibraryView 删除用户保存的片库筛选。
func (a *App) DeleteSavedLibraryView(viewID uint) error {
	err := a.videoService.DeleteSavedLibraryView(viewID)
	log.Printf("API DeleteSavedLibraryView id=%d err=%v", viewID, err)
	return err
}

func (a *App) BatchRemoveTagFromVideos(videoIDs []uint, tagID uint) *services.BatchVideoOperationResult {
	result := a.videoService.BatchRemoveTagFromVideos(videoIDs, tagID)
	log.Printf("API BatchRemoveTagFromVideos requested=%d succeeded=%d failed=%d tagID=%d", result.Requested, result.Succeeded, result.Failed, tagID)
	return result
}

// ===== Tag Methods =====

// GetAllTags 获取所有标签
func (a *App) GetAllTags() ([]models.Tag, error) {
	tags, err := a.tagService.GetAllTags()
	log.Printf("API GetAllTags result=%d err=%v sample=%s", len(tags), err, summarizeTags(tags, 5))
	return tags, err
}

func (a *App) GetAITagLibrary() ([]models.Tag, error) {
	tags, err := a.tagService.GetAITagLibrary()
	log.Printf("API GetAITagLibrary result=%d err=%v", len(tags), err)
	return tags, err
}

func (a *App) SaveAITagLibrary(inputs []services.AITagLibraryInput) ([]models.Tag, error) {
	tags, err := a.tagService.SaveAITagLibrary(inputs)
	log.Printf("API SaveAITagLibrary requested=%d result=%d err=%v", len(inputs), len(tags), err)
	return tags, err
}

// CreateTag 创建标签
func (a *App) CreateTag(name, color string) (*models.Tag, error) {
	tag, err := a.tagService.CreateTag(name, color)
	if tag != nil {
		log.Printf("API CreateTag name=%s color=%s id=%d err=%v", name, color, tag.ID, err)
	} else {
		log.Printf("API CreateTag name=%s color=%s id=0 err=%v", name, color, err)
	}
	return tag, err
}

// UpdateTag 更新标签
func (a *App) UpdateTag(id uint, name, color string) error {
	err := a.tagService.UpdateTag(id, name, color)
	log.Printf("API UpdateTag id=%d name=%s color=%s err=%v", id, name, color, err)
	return err
}

// DeleteTag 删除标签
func (a *App) DeleteTag(id uint) error {
	err := a.tagService.DeleteTag(id)
	log.Printf("API DeleteTag id=%d err=%v", id, err)
	return err
}

func (a *App) MergeTags(sourceTagIDs []uint, targetTagID uint) (*services.MergeTagsResult, error) {
	result, err := a.tagService.MergeTags(sourceTagIDs, targetTagID)
	log.Printf("API MergeTags sources=%v target=%d result=%+v err=%v", sourceTagIDs, targetTagID, result, err)
	return result, err
}

// ===== AI Tagging Methods =====

func (a *App) ListAITagCandidates(videoID uint, confidence string, status string) ([]services.AITaggingReviewItem, error) {
	items, err := a.aiTaggingService.ListCandidates(videoID, confidence, status)
	log.Printf("API ListAITagCandidates videoID=%d confidence=%s status=%s result=%d err=%v", videoID, confidence, status, len(items), err)
	return items, err
}

func (a *App) ApproveAITagCandidate(candidateID uint) (*services.AITaggingReviewItem, error) {
	item, err := a.aiTaggingService.ApproveCandidate(candidateID)
	log.Printf("API ApproveAITagCandidate candidateID=%d err=%v", candidateID, err)
	return item, err
}

func (a *App) RejectAITagCandidate(candidateID uint) error {
	err := a.aiTaggingService.RejectCandidate(candidateID)
	log.Printf("API RejectAITagCandidate candidateID=%d err=%v", candidateID, err)
	return err
}

func (a *App) RejectAITagCandidatesByVideo(videoID uint) (int64, error) {
	count, err := a.aiTaggingService.RejectPendingCandidatesByVideo(videoID)
	log.Printf("API RejectAITagCandidatesByVideo videoID=%d rejected=%d err=%v", videoID, count, err)
	return count, err
}

func (a *App) RetryAITagging(videoID uint) error {
	err := a.aiTaggingService.RetryVideo(videoID)
	triggered := false
	if err == nil {
		triggered = a.aiTaggingService.Trigger()
	}
	log.Printf("API RetryAITagging videoID=%d ai_triggered=%v err=%v", videoID, triggered, err)
	return err
}

func (a *App) TriggerAITagging() bool {
	triggered := a.aiTaggingService.Trigger()
	log.Printf("API TriggerAITagging triggered=%v", triggered)
	return triggered
}

func (a *App) GetAITaggingStatusSummary() (*services.AITaggingStatusSummary, error) {
	summary, err := a.aiTaggingService.StatusSummary()
	log.Printf("API GetAITaggingStatusSummary err=%v summary=%+v", err, summary)
	return summary, err
}

func (a *App) GetAIQualityReport(filter services.AIQualityFilter) (*services.AIQualityReport, error) {
	startedAt := time.Now()
	report, err := a.aiQualityService.Report(filter)
	var tagSamples, sameSourceSamples, runs int64
	if report != nil {
		tagSamples = report.TagSummary.Decided
		sameSourceSamples = report.SameSourceSummary.Decided
		runs = report.RunSummary.Total
	}
	log.Printf("API GetAIQualityReport window=%s filters={tag:%v confidence:%v model:%v tag_prompt:%v comparison_prompt:%v detection:%v} samples={tag:%d same_source:%d runs:%d} duration_ms=%d failed=%v",
		filter.Window, filter.TagID > 0, filter.Confidence != "", filter.ModelIdentifier != "", filter.PromptSchemaVersion != "", filter.ComparisonPromptVersion != "", filter.DetectionVersion != "",
		tagSamples, sameSourceSamples, runs, time.Since(startedAt).Milliseconds(), err != nil)
	return report, err
}

func (a *App) ListSameSourceRelations(status string, unreadOnly bool) ([]services.VideoSameSourceReviewItem, error) {
	items, err := a.aiTaggingService.ListSameSourceRelations(status, unreadOnly)
	log.Printf("API ListSameSourceRelations status=%s unreadOnly=%v result=%d err=%v", status, unreadOnly, len(items), err)
	return items, err
}

func (a *App) MarkSameSourceRelationRead(relationID uint) error {
	err := a.aiTaggingService.MarkSameSourceRelationRead(relationID)
	log.Printf("API MarkSameSourceRelationRead relationID=%d err=%v", relationID, err)
	return err
}

func (a *App) ConfirmSameSourceRelation(relationID uint) error {
	err := a.aiTaggingService.ConfirmSameSourceRelation(relationID)
	log.Printf("API ConfirmSameSourceRelation relationID=%d err=%v", relationID, err)
	return err
}

func (a *App) RejectSameSourceRelation(relationID uint) error {
	err := a.aiTaggingService.RejectSameSourceRelation(relationID)
	if err == nil {
		a.cleanupService.InvalidateAnalysis()
	}
	log.Printf("API RejectSameSourceRelation relationID=%d err=%v", relationID, err)
	return err
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
	err := a.settingsService.UpdateSettings(input)
	if err == nil {
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
	if svc := a.imageAIDescriptionService(); svc != nil {
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
	a.resetImageAIDescriptionService()
	a.startShortFeedServer(a.ctx)
	if settings, err := a.settingsService.GetSettings(); err == nil {
		_ = a.configureLibraryWatcher(settings.LibraryWatchEnabled)
		a.configureLocalMetadata(settings.LocalMetadataEnabled)
	}
}

func (a *App) resetSemanticIndexService() {
	if database.DB == nil {
		a.semanticMu.Lock()
		a.semanticIndex = nil
		a.semanticMu.Unlock()
		return
	}
	capability := database.PrepareSemanticVectorStorage(database.DB)
	provider := services.SemanticIndexConfigProviderFunc(func() (services.SemanticIndexConfig, error) {
		config, err := (services.SettingsAITaggingConfigProvider{}).Load()
		if err != nil {
			return services.SemanticIndexConfig{}, err
		}
		semanticConfig := services.SemanticIndexConfigFromAITagging(config)
		var settings models.Settings
		if err := database.DB.First(&settings).Error; err == nil && strings.TrimSpace(settings.SemanticEmbeddingModel) != "" {
			semanticConfig.Model = strings.TrimSpace(settings.SemanticEmbeddingModel)
		}
		return semanticConfig, nil
	})
	service := services.NewSemanticIndexService(database.DB, capability, provider)
	service.SetEventEmitter(func(status services.SemanticIndexStatus) {
		if a.ctx != nil && a.ctx.Err() == nil {
			runtime.EventsEmit(a.ctx, "semantic-index-state", status)
		}
	})
	a.semanticMu.Lock()
	a.semanticIndex = service
	a.semanticMu.Unlock()
}

// resetImageAIDescriptionService 在数据库就绪后（启动或恢复失败续跑）重建图片 AI 描述服务。
func (a *App) resetImageAIDescriptionService() {
	a.imageAIDescMu.Lock()
	old := a.imageAIDescription
	a.imageAIDescription = nil
	a.imageAIDescMu.Unlock()
	if old != nil {
		old.StopAndWait()
	}
	if database.DB == nil {
		return
	}
	svc := services.NewImageAIDescriptionService(database.DB, a.imageThumbnail, services.SettingsAITaggingConfigProvider{})
	if err := svc.RecoverInterruptedImageDescriptions(); err != nil {
		log.Printf("App startup image AI description recovery failed err=%v", err)
	}
	svc.SetEventEmitter(func(status services.ImageAIDescriptionStatus) {
		if a.ctx != nil && a.ctx.Err() == nil {
			runtime.EventsEmit(a.ctx, "image-ai-description-progress", status)
		}
	})
	a.imageAIDescMu.Lock()
	a.imageAIDescription = svc
	a.imageAIDescMu.Unlock()
}

func (a *App) imageAIDescriptionService() *services.ImageAIDescriptionService {
	a.imageAIDescMu.RLock()
	defer a.imageAIDescMu.RUnlock()
	return a.imageAIDescription
}

// imageSemanticIndexService 以读锁返回当前图片语义索引服务指针。
func (a *App) imageSemanticIndexService() *services.ImageSemanticIndexService {
	a.imageSemanticMu.RLock()
	defer a.imageSemanticMu.RUnlock()
	return a.imageSemanticIndex
}

// resetImageSemanticIndexService 在数据库就绪后重建图片语义索引服务，镜像 resetSemanticIndexService。
func (a *App) resetImageSemanticIndexService() {
	a.imageSemanticMu.Lock()
	old := a.imageSemanticIndex
	a.imageSemanticIndex = nil
	a.imageSemanticMu.Unlock()
	if old != nil {
		old.StopAndWait()
	}
	if database.DB == nil {
		return
	}
	capability := database.PrepareImageSemanticVectorStorage(database.DB)
	provider := services.SemanticIndexConfigProviderFunc(func() (services.SemanticIndexConfig, error) {
		config, err := (services.SettingsAITaggingConfigProvider{}).Load()
		if err != nil {
			return services.SemanticIndexConfig{}, err
		}
		semanticConfig := services.SemanticIndexConfigFromAITagging(config)
		var settings models.Settings
		if err := database.DB.First(&settings).Error; err == nil && strings.TrimSpace(settings.SemanticEmbeddingModel) != "" {
			semanticConfig.Model = strings.TrimSpace(settings.SemanticEmbeddingModel)
		}
		return semanticConfig, nil
	})
	service := services.NewImageSemanticIndexService(database.DB, capability, provider)
	service.SetEventEmitter(func(status services.ImageSemanticIndexStatus) {
		if a.ctx != nil && a.ctx.Err() == nil {
			runtime.EventsEmit(a.ctx, "image-semantic-index-state", status)
		}
	})
	a.imageSemanticMu.Lock()
	a.imageSemanticIndex = service
	a.imageSemanticMu.Unlock()
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

// AddDirectory 添加扫描目录
func (a *App) AddDirectory(path, alias string) (*models.ScanDirectory, error) {
	dir, err := a.directoryService.AddDirectory(path, alias)
	if err == nil {
		a.reconfigureLibraryWatcher()
	}
	return dir, err
}

// UpdateDirectory 更新目录
func (a *App) UpdateDirectory(id uint, path, alias string) error {
	err := a.directoryService.UpdateDirectory(id, path, alias)
	if err == nil {
		a.reconfigureLibraryWatcher()
	}
	return err
}

// DeleteDirectory 删除扫描目录
func (a *App) DeleteDirectory(id uint) error {
	err := a.directoryService.DeleteDirectory(id)
	if err == nil {
		a.reconfigureLibraryWatcher()
	}
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

func (a *App) SyncScanDirectories() (*services.ScanSyncResult, error) {
	dirs, err := a.directoryService.GetAllDirectories()
	if err != nil {
		log.Printf("API SyncScanDirectories load dirs err=%v", err)
		return nil, err
	}
	result := a.videoService.SyncScanDirectories(dirs)
	if a.cleanupService != nil && (result.Added > 0 || result.Relocated > 0 || result.Deleted > 0 || result.MetadataRefreshed > 0) {
		a.cleanupService.InvalidateAnalysis()
	}
	aiTriggered := false
	if result.Added > 0 && a.aiTaggingService != nil {
		aiTriggered = a.aiTaggingService.Trigger()
	}
	log.Printf("API SyncScanDirectories dirs=%d scanned=%d added=%d relocated=%d deleted=%d refreshed=%d skipped=%d errors=%d ai_triggered=%v",
		result.Directories, result.Scanned, result.Added, result.Relocated, result.Deleted, result.MetadataRefreshed, result.Skipped, len(result.Errors), aiTriggered)
	return result, nil
}

// ===== Subtitle Methods =====

// GetSubtitleEngineStatuses 获取字幕引擎可用性状态
func (a *App) GetSubtitleEngineStatuses() ([]services.SubtitleEngineStatus, error) {
	return a.subtitleService.GetEngineStatuses()
}

// PrepareSubtitleEngine 准备指定字幕引擎所需依赖
func (a *App) PrepareSubtitleEngine(engine services.SubtitleEngine) error {
	return a.subtitleService.PrepareEngine(engine)
}

// CheckSubtitleDependencies 检查字幕生成依赖
func (a *App) CheckSubtitleDependencies() (map[string]bool, error) {
	return a.subtitleService.CheckDependencies()
}

// DownloadSubtitleDependencies 下载字幕生成依赖
func (a *App) DownloadSubtitleDependencies() error {
	return a.subtitleService.DownloadDependencies()
}

// GenerateSubtitle 生成字幕
func (a *App) GenerateSubtitle(req services.SubtitleGenerateRequest) (*services.SubtitleGenerateResult, error) {
	video, err := a.videoService.GetVideo(req.VideoID)
	if err != nil {
		log.Printf("API GenerateSubtitle id=%d failed to get video: %v", req.VideoID, err)
		return nil, err
	}
	settings, _ := a.settingsService.GetSettings()
	options := subtitleGenerateOptionsFromSettings(settings, false)
	req.VideoName = video.Name
	log.Printf("API GenerateSubtitle id=%d path=%s engine=%s bilingual=%v lang=%s source=%s provider=%s", req.VideoID, video.Path, req.Engine, options.BilingualEnabled, options.BilingualLang, req.SourceLang, options.TranslationConfig.Provider)
	return a.subtitleService.GenerateSubtitle(req, video.Path, options)
}

// ForceGenerateSubtitle 强制生成字幕（跳过幻觉检测）
func (a *App) ForceGenerateSubtitle(req services.SubtitleGenerateRequest) (*services.SubtitleGenerateResult, error) {
	video, err := a.videoService.GetVideo(req.VideoID)
	if err != nil {
		return nil, err
	}
	settings, _ := a.settingsService.GetSettings()
	options := subtitleGenerateOptionsFromSettings(settings, true)
	req.VideoName = video.Name
	log.Printf("API ForceGenerateSubtitle id=%d path=%s engine=%s source=%s", req.VideoID, video.Path, req.Engine, req.SourceLang)
	return a.subtitleService.GenerateSubtitle(req, video.Path, options)
}

// CancelSubtitle 取消正在进行的字幕生成任务
func (a *App) CancelSubtitle() {
	a.subtitleService.CancelGeneration()
	log.Printf("API CancelSubtitle")
}

// CancelSubtitleTask 取消指定的字幕任务。
func (a *App) CancelSubtitleTask(taskID uint) error {
	err := a.subtitleService.CancelSubtitleTask(taskID)
	log.Printf("API CancelSubtitleTask task_id=%d err=%v", taskID, err)
	return err
}

// GetSubtitleQueueState 返回当前字幕任务队列。
func (a *App) GetSubtitleQueueState() services.SubtitleQueueSnapshot {
	return a.subtitleService.GetSubtitleQueueState()
}

func subtitleGenerateOptionsFromSettings(settings *models.Settings, force bool) services.SubtitleGenerateOptions {
	options := services.SubtitleGenerateOptions{
		BilingualLang: "zh",
		ForceGenerate: force,
		RecognitionConfig: services.SubtitleRecognitionConfig{
			WhisperXModel:       "medium",
			WhisperXBatchSize:   8,
			WhisperXComputeType: "int8",
		},
		TranslationConfig: services.SubtitleTranslationConfig{Provider: "deepl"},
	}
	if settings == nil {
		return options
	}
	options.BilingualEnabled = settings.BilingualEnabled
	options.BilingualLang = settings.BilingualLang
	options.TranslationConfig = services.SubtitleTranslationConfig{
		Provider:    settings.SubtitleTranslationProvider,
		DeepLAPIKey: settings.DeepLApiKey,
		BaseURL:     settings.SubtitleTranslationBaseURL,
		APIKey:      settings.SubtitleTranslationAPIKey,
		Model:       settings.SubtitleTranslationModel,
	}
	options.RecognitionConfig.WhisperXModel = settings.SubtitleWhisperXModel
	options.RecognitionConfig.WhisperXBatchSize = settings.SubtitleWhisperXBatchSize
	return options
}

// GetSubtitleSegments 获取已生成字幕的结构化片段
func (a *App) GetSubtitleSegments(videoID uint) ([]subtitleparser.Segment, error) {
	video, err := a.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}

	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	segments, err := subtitleparser.ParseFile(srtPath)
	if err != nil {
		log.Printf("API GetSubtitleSegments id=%d path=%s err=%v", videoID, srtPath, err)
		return nil, err
	}

	log.Printf("API GetSubtitleSegments id=%d path=%s segments=%d", videoID, srtPath, len(segments))
	return segments, nil
}

// GetSubtitleEditDocument loads the external SRT through the strict editor parser.
func (a *App) GetSubtitleEditDocument(videoID uint) (*services.SubtitleEditDocument, error) {
	video, err := a.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}
	document, err := a.subtitleWorkbench.GetDocument(*video)
	log.Printf("API GetSubtitleEditDocument id=%d entries=%d err=%v", videoID, subtitleEditEntryCount(document), err)
	return document, err
}

func subtitleEditEntryCount(document *services.SubtitleEditDocument) int {
	if document == nil {
		return 0
	}
	return len(document.Entries)
}

// ValidateSubtitleEditDocument validates an in-memory edit without touching the source SRT.
func (a *App) ValidateSubtitleEditDocument(request services.SubtitleSaveRequest) services.SubtitleValidationResult {
	return a.subtitleWorkbench.Validate(request.Entries)
}

// RetranslateSubtitleEntries translates a selection without persisting it.
func (a *App) RetranslateSubtitleEntries(request services.SubtitleRetranslateRequest) (*services.SubtitleRetranslateResult, error) {
	settings, err := a.settingsService.GetSettings()
	if err != nil {
		return nil, err
	}
	config := subtitleGenerateOptionsFromSettings(settings, false).TranslationConfig
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := a.subtitleWorkbench.Retranslate(ctx, request, config)
	log.Printf("API RetranslateSubtitleEntries id=%d entries=%d err=%v", request.VideoID, len(request.Entries), err)
	return result, err
}

// SaveSubtitleEditDocument atomically replaces the current SRT if its fingerprint is unchanged.
func (a *App) SaveSubtitleEditDocument(request services.SubtitleSaveRequest) (*services.SubtitleSaveResult, error) {
	video, err := a.videoService.GetVideo(request.VideoID)
	if err != nil {
		return nil, err
	}
	result, err := a.subtitleWorkbench.SaveDocument(*video, request)
	status := services.SubtitleSaveStatus("")
	if result != nil {
		status = result.Status
	}
	log.Printf("API SaveSubtitleEditDocument id=%d entries=%d status=%s err=%v", request.VideoID, len(request.Entries), status, err)
	return result, err
}

// GetCleanupCandidates 获取清理候选（轻量规则）
func (a *App) GetCleanupCandidates(minDurationSeconds int, minWidth int, minHeight int) (*services.CleanupAnalysis, error) {
	criteria := services.CleanupCriteria{
		MinDuration: time.Duration(minDurationSeconds) * time.Second,
		MinWidth:    minWidth,
		MinHeight:   minHeight,
	}
	startedAt := time.Now()
	log.Printf("API GetCleanupCandidates begin duration=%d width=%d height=%d", minDurationSeconds, minWidth, minHeight)
	analysis, err := a.cleanupService.AnalyzeCleanupCandidates(criteria)
	if err != nil {
		log.Printf("API GetCleanupCandidates duration=%d width=%d height=%d elapsed=%s err=%v",
			minDurationSeconds, minWidth, minHeight, time.Since(startedAt).Round(time.Millisecond), err)
		return nil, err
	}

	log.Printf("API GetCleanupCandidates duration=%d width=%d height=%d elapsed=%s duplicate_groups=%d low_duration=%d low_resolution=%d",
		minDurationSeconds, minWidth, minHeight,
		time.Since(startedAt).Round(time.Millisecond),
		len(analysis.DuplicateGroups), len(analysis.LowDuration), len(analysis.LowResolution),
	)
	return analysis, nil
}

func (a *App) StartCleanupAnalysis(minDurationSeconds int, minWidth int, minHeight int) (*services.CleanupStatus, error) {
	criteria := services.CleanupCriteria{
		MinDuration: time.Duration(minDurationSeconds) * time.Second,
		MinWidth:    minWidth,
		MinHeight:   minHeight,
	}
	status, err := a.cleanupService.StartAnalysis(criteria)
	log.Printf("API StartCleanupAnalysis duration=%d width=%d height=%d running=%v completed=%v err=%v",
		minDurationSeconds, minWidth, minHeight, status != nil && status.Running, status != nil && status.Completed, err)
	return status, err
}

func (a *App) GetCleanupStatus() *services.CleanupStatus {
	status := a.cleanupService.Status()
	log.Printf("API GetCleanupStatus running=%v completed=%v hasAnalysis=%v err=%q",
		status.Running, status.Completed, status.Analysis != nil, status.Error)
	return status
}

// ===== Video Enhancement (P-013) =====

func (a *App) GetEnhancementCapability() services.EnhancementRuntimeCapability {
	return a.enhancement.Capability()
}

func (a *App) CreateEnhancementTask(request services.EnhancementCreateRequest) (*services.EnhancementTaskView, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	view, err := a.enhancement.CreateTask(ctx, request)
	log.Printf("API CreateEnhancementTask video=%d profile=%s err=%v", request.VideoID, request.Profile, err)
	return view, err
}

func (a *App) GetEnhancementVideoPreflight(videoID uint) (*services.EnhancementVideoPreflight, error) {
	return a.enhancement.PreflightVideo(videoID)
}

func (a *App) ListEnhancementTasks(limit int) ([]services.EnhancementTaskView, error) {
	return a.enhancement.ListTasks(limit)
}

func (a *App) CancelEnhancementTask(taskID uint) error {
	err := a.enhancement.CancelTask(taskID)
	log.Printf("API CancelEnhancementTask task=%d err=%v", taskID, err)
	return err
}

func (a *App) RetryEnhancementTask(taskID uint) (*services.EnhancementTaskView, error) {
	view, err := a.enhancement.RetryTask(taskID)
	log.Printf("API RetryEnhancementTask task=%d err=%v", taskID, err)
	return view, err
}

// DismissNearDuplicateGroup 持久忽略一组近似重复视频，后续分析不再报出。
func (a *App) DismissNearDuplicateGroup(videoIDs []uint) error {
	err := services.DismissNearDuplicateGroup(videoIDs)
	log.Printf("API DismissNearDuplicateGroup videos=%d err=%v", len(videoIDs), err)
	return err
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

// ===== Image Directory Methods =====

// GetAllImageDirectories 获取所有图片扫描目录
func (a *App) GetAllImageDirectories() ([]models.ImageDirectory, error) {
	dirs, err := a.imageService.GetAllImageDirectories()
	log.Printf("API GetAllImageDirectories result=%d err=%v", len(dirs), err)
	return dirs, err
}

// AddImageDirectory 添加图片扫描目录
func (a *App) AddImageDirectory(path, alias string) (*models.ImageDirectory, error) {
	dir, err := a.imageService.AddImageDirectory(path, alias)
	log.Printf("API AddImageDirectory path=%s alias=%s err=%v", path, alias, err)
	return dir, err
}

// UpdateImageDirectory 更新图片扫描目录
func (a *App) UpdateImageDirectory(id uint, path, alias string) error {
	err := a.imageService.UpdateImageDirectory(id, path, alias)
	log.Printf("API UpdateImageDirectory id=%d path=%s alias=%s err=%v", id, path, alias, err)
	return err
}

// DeleteImageDirectory 删除图片扫描目录（软删除）
func (a *App) DeleteImageDirectory(id uint) error {
	err := a.imageService.DeleteImageDirectory(id)
	log.Printf("API DeleteImageDirectory id=%d err=%v", id, err)
	return err
}

// SyncImageDirectories 对账扫描全部活跃图片目录
func (a *App) SyncImageDirectories() (*services.ImageScanResult, error) {
	result, err := a.imageService.SyncImageDirectories()
	if err != nil {
		log.Printf("API SyncImageDirectories err=%v", err)
		return nil, err
	}
	log.Printf("API SyncImageDirectories added=%d relocated=%d removed=%d skipped=%d errors=%d",
		result.Added, result.Relocated, result.Removed, result.Skipped, len(result.Errors))
	return result, nil
}

// ===== Image Library Methods =====

// SearchImagePage 照片页游标分页查询。
func (a *App) SearchImagePage(request services.ImagePageRequest) (*services.ImagePage, error) {
	page, err := a.imageLibraryService.SearchImagePage(request)
	if page != nil {
		log.Printf("API SearchImagePage sort=%s result=%d hasNext=%v err=%v", request.Filter.SortMode, len(page.Images), page.NextCursor != nil, err)
	} else {
		log.Printf("API SearchImagePage sort=%s result=nil err=%v", request.Filter.SortMode, err)
	}
	return page, err
}

// ListImageTimelineBuckets 返回照片时间线分组的年月计数摘要（供分组头显示总张数）。
func (a *App) ListImageTimelineBuckets(filter services.ImageFilter) ([]services.ImageTimelineBucket, error) {
	buckets, err := a.imageLibraryService.ListImageTimelineBuckets(filter)
	log.Printf("API ListImageTimelineBuckets buckets=%d err=%v", len(buckets), err)
	return buckets, err
}

// GetImageDetail 返回图片详情（含标签与 AI 描述）。
func (a *App) GetImageDetail(imageID uint) (*services.ImageDetail, error) {
	detail, err := a.imageLibraryService.GetImageDetail(imageID)
	log.Printf("API GetImageDetail image_id=%d err=%v", imageID, err)
	return detail, err
}

// SetImageFavorite 更新照片收藏状态。
func (a *App) SetImageFavorite(imageID uint, favorite bool) (*models.Image, error) {
	image, err := a.imageLibraryService.SetImageFavorite(imageID, favorite)
	log.Printf("API SetImageFavorite image_id=%d favorite=%v err=%v", imageID, favorite, err)
	return image, err
}

// SetImageRating 更新照片个人评分（0–10 半分制，nil 清空）。
func (a *App) SetImageRating(imageID uint, rating *float64) (*models.Image, error) {
	image, err := a.imageLibraryService.SetImageRating(imageID, rating)
	log.Printf("API SetImageRating image_id=%d rating=%v err=%v", imageID, rating, err)
	return image, err
}

// AddTagToImage 为图片添加标签
func (a *App) AddTagToImage(imageID uint, tagID uint) error {
	err := a.imageLibraryService.AddTagToImage(imageID, tagID)
	log.Printf("API AddTagToImage imageID=%d tagID=%d err=%v", imageID, tagID, err)
	return err
}

// RemoveTagFromImage 移除图片标签
func (a *App) RemoveTagFromImage(imageID uint, tagID uint) error {
	err := a.imageLibraryService.RemoveTagFromImage(imageID, tagID)
	log.Printf("API RemoveTagFromImage imageID=%d tagID=%d err=%v", imageID, tagID, err)
	return err
}

// BatchAddTagToImages 批量为图片添加标签
func (a *App) BatchAddTagToImages(imageIDs []uint, tagID uint) *services.BatchImageOperationResult {
	result := a.imageLibraryService.BatchAddTagToImages(imageIDs, tagID)
	log.Printf("API BatchAddTagToImages requested=%d succeeded=%d failed=%d tagID=%d", result.Requested, result.Succeeded, result.Failed, tagID)
	return result
}

// BatchRemoveTagFromImages 批量移除图片标签
func (a *App) BatchRemoveTagFromImages(imageIDs []uint, tagID uint) *services.BatchImageOperationResult {
	result := a.imageLibraryService.BatchRemoveTagFromImages(imageIDs, tagID)
	log.Printf("API BatchRemoveTagFromImages requested=%d succeeded=%d failed=%d tagID=%d", result.Requested, result.Succeeded, result.Failed, tagID)
	return result
}

// ===== Image Trash Methods =====

// DeleteImage 删除图片（deleteFile=false 仅软删记录，不建回收站条目）。
func (a *App) DeleteImage(id uint, deleteFile bool) error {
	err := a.imageService.DeleteImage(id, deleteFile)
	log.Printf("API DeleteImage id=%d deleteFile=%v err=%v", id, deleteFile, err)
	if err == nil && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
	return err
}

// BatchDeleteImages 批量删除图片
func (a *App) BatchDeleteImages(imageIDs []uint, deleteFile bool) *services.BatchImageOperationResult {
	result := a.imageService.BatchDeleteImages(imageIDs, deleteFile)
	log.Printf("API BatchDeleteImages requested=%d succeeded=%d failed=%d deleteFile=%v", result.Requested, result.Succeeded, result.Failed, deleteFile)
	if result.Succeeded > 0 && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
	return result
}

// ListImageTrashEntries 返回当前可恢复的图片删除记录。
func (a *App) ListImageTrashEntries() ([]models.ImageTrashEntry, error) {
	entries, err := a.imageService.ListImageTrashEntries()
	log.Printf("API ListImageTrashEntries result=%d err=%v", len(entries), err)
	return entries, err
}

// RestoreImageTrashEntry 将一张图片恢复到删除前的路径。
func (a *App) RestoreImageTrashEntry(entryID uint) (*models.Image, error) {
	image, err := a.imageService.RestoreImageTrashEntry(entryID)
	log.Printf("API RestoreImageTrashEntry entryID=%d err=%v", entryID, err)
	if err == nil && image != nil && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
	return image, err
}

// ===== Image Semantic Methods =====

// StartImageSemanticIndex 启动图片语义索引构建任务
func (a *App) StartImageSemanticIndex() (services.ImageSemanticIndexStatus, error) {
	svc := a.imageSemanticIndexService()
	if svc == nil {
		return services.ImageSemanticIndexStatus{}, services.ErrImageSemanticIndexUnavailable
	}
	// 全局互斥（D-010）：视频语义索引运行中时拒绝图片任务。
	if video := a.semanticIndexService(); video != nil && video.Status().Running {
		return services.ImageSemanticIndexStatus{}, fmt.Errorf("已有语义索引任务运行中（视频），请先取消后再启动图片索引")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	status, err := svc.Start(ctx)
	log.Printf("API StartImageSemanticIndex err=%v", err)
	return status, err
}

// GetImageSemanticIndexStatus 返回图片语义索引任务状态
func (a *App) GetImageSemanticIndexStatus() services.ImageSemanticIndexStatus {
	svc := a.imageSemanticIndexService()
	if svc == nil {
		return services.ImageSemanticIndexStatus{Available: false, Unavailable: "数据库未初始化"}
	}
	return svc.Status()
}

// CancelImageSemanticIndex 取消图片语义索引任务
func (a *App) CancelImageSemanticIndex() error {
	svc := a.imageSemanticIndexService()
	if svc == nil {
		return services.ErrImageSemanticIndexUnavailable
	}
	err := svc.Cancel()
	log.Printf("API CancelImageSemanticIndex err=%v", err)
	return err
}

// SearchImagesSemantic 照片页内语义检索
func (a *App) SearchImagesSemantic(request services.ImageSemanticSearchRequest) (*services.ImageSemanticSearchPage, error) {
	svc := a.imageSemanticIndexService()
	if svc == nil {
		return nil, services.ErrImageSemanticIndexUnavailable
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	page, err := svc.SearchImagesSemantic(ctx, request)
	log.Printf("API SearchImagesSemantic offset=%d limit=%d err=%v", request.Offset, request.Limit, err)
	return page, err
}

// ===== Image Cleanup Methods =====

// StartImageCleanupAnalysis 启动图片清理候选分析（异步，进度走 image-cleanup-progress 事件）。
func (a *App) StartImageCleanupAnalysis() (*services.ImageCleanupStatus, error) {
	status, err := a.imageCleanupService.StartImageCleanupAnalysis()
	log.Printf("API StartImageCleanupAnalysis err=%v", err)
	return status, err
}

// GetImageCleanupStatus 返回图片清理分析的当前状态与结果快照。
func (a *App) GetImageCleanupStatus() *services.ImageCleanupStatus {
	return a.imageCleanupService.GetImageCleanupStatus()
}

// DismissImageNearDuplicateGroup 忽略一组近似重复图片，后续分析不再报告。
func (a *App) DismissImageNearDuplicateGroup(imageIDs []uint) error {
	err := services.DismissImageNearDuplicateGroup(imageIDs)
	log.Printf("API DismissImageNearDuplicateGroup images=%d err=%v", len(imageIDs), err)
	if err == nil && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
	return err
}

// ===== Image AI Description Methods =====

// StartImageAIDescription 启动图片 AI 描述批量生成任务
func (a *App) StartImageAIDescription() (services.ImageAIDescriptionStatus, error) {
	svc := a.imageAIDescriptionService()
	if svc == nil {
		return services.ImageAIDescriptionStatus{}, fmt.Errorf("数据库未初始化")
	}
	status, err := svc.StartImageAIDescription(a.ctx)
	log.Printf("API StartImageAIDescription err=%v", err)
	return status, err
}

// GetImageAIDescriptionStatus 返回图片 AI 描述任务状态
func (a *App) GetImageAIDescriptionStatus() services.ImageAIDescriptionStatus {
	svc := a.imageAIDescriptionService()
	if svc == nil {
		return services.ImageAIDescriptionStatus{}
	}
	return svc.GetImageAIDescriptionStatus()
}

// CancelImageAIDescription 取消图片 AI 描述批量任务
func (a *App) CancelImageAIDescription() error {
	svc := a.imageAIDescriptionService()
	if svc == nil {
		return fmt.Errorf("数据库未初始化")
	}
	err := svc.CancelImageAIDescription()
	log.Printf("API CancelImageAIDescription err=%v", err)
	return err
}

// RegenerateImageAIDescription 对单张图片同步重新生成 AI 描述
func (a *App) RegenerateImageAIDescription(imageID uint) (*models.ImageAIDescription, error) {
	svc := a.imageAIDescriptionService()
	if svc == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	desc, err := svc.RegenerateImageAIDescription(imageID)
	log.Printf("API RegenerateImageAIDescription image_id=%d err=%v", imageID, err)
	return desc, err
}

// ===== Image EXIF Backfill Methods =====

// StartImageEXIFBackfill 启动历史图片的 EXIF 补全任务
func (a *App) StartImageEXIFBackfill() (services.ImageEXIFBackfillStatus, error) {
	if a.imageEXIFBackfill == nil {
		return services.ImageEXIFBackfillStatus{}, fmt.Errorf("数据库未初始化")
	}
	status, err := a.imageEXIFBackfill.StartImageEXIFBackfill(a.ctx)
	log.Printf("API StartImageEXIFBackfill err=%v", err)
	return status, err
}

// GetImageEXIFBackfillStatus 返回 EXIF 补全任务状态
func (a *App) GetImageEXIFBackfillStatus() services.ImageEXIFBackfillStatus {
	if a.imageEXIFBackfill == nil {
		return services.ImageEXIFBackfillStatus{}
	}
	return a.imageEXIFBackfill.GetImageEXIFBackfillStatus()
}

// CancelImageEXIFBackfill 取消 EXIF 补全任务
func (a *App) CancelImageEXIFBackfill() error {
	if a.imageEXIFBackfill == nil {
		return fmt.Errorf("数据库未初始化")
	}
	err := a.imageEXIFBackfill.CancelImageEXIFBackfill()
	log.Printf("API CancelImageEXIFBackfill err=%v", err)
	return err
}

// ===== Image Insights Methods =====

// GetImageInsights 返回图片维度洞察统计
func (a *App) GetImageInsights() (*services.ImageStats, error) {
	stats, err := a.imageStatsService.GetImageInsights()
	log.Printf("API GetImageInsights err=%v", err)
	return stats, err
}
