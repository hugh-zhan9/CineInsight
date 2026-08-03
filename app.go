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
	"time"
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
	localMetadata         *services.LocalMetadataService
	mediaProbeService     *services.MediaProbeService
	technicalBackfill     *services.TechnicalBackfillService
	libraryWatcher        *services.LibraryWatcherService
	shortFeedServer       *services.ShortFeedHTTPServer
	shortFeedStartupError string
	startupError          string
	logFile               *os.File // 保持日志文件句柄引用，防止泄漏
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
		localMetadata:         localMetadata,
		mediaProbeService:     mediaProbeService,
		technicalBackfill:     services.NewTechnicalBackfillService(mediaProbeService),
		libraryWatcher:        libraryWatcher,
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
	a.subtitleService.SetContext(ctx) // Inject context
	// Background workers can still emit while the app is tearing down; the frontend is gone by then.
	emit := func(event string, data any) {
		if ctx.Err() != nil {
			return
		}
		runtime.EventsEmit(ctx, event, data)
	}
	a.technicalBackfill.SetEventEmitter(func(status services.TechnicalBackfillStatus) {
		emit("technical-backfill-state", status)
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
	a.cleanupService.SetContext(ctx)
	if result, err := a.tagService.SyncShortVideoTags(); err != nil {
		log.Printf("App startup short-video tag sync failed err=%v", err)
	} else {
		log.Printf("App startup short-video tag sync tag=%d added=%d removed=%d", result.TagID, result.Added, result.Removed)
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
	} else {
		log.Printf("App startup settings load failed err=%v", err)
	}
}

func (a *App) shutdown(ctx context.Context) {
	if a.localMetadata != nil {
		a.localMetadata.StopBackfill()
	}
	if a.libraryWatcher != nil {
		if err := a.libraryWatcher.Close(); err != nil {
			log.Printf("Library watcher shutdown failed: %v", err)
		}
	}
	if a.technicalBackfill != nil {
		_ = a.technicalBackfill.Cancel()
	}
	if a.aiTaggingService != nil {
		a.aiTaggingService.Stop()
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
	}
	log.Printf("API UpdateSettings err=%v", err)
	return err
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
