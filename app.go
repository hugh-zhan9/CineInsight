package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
	"video-master/models"
	"video-master/services"
	"video-master/services/subtitleparser"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx                   context.Context
	videoService          *services.VideoService
	tagService            *services.TagService
	settingsService       *services.SettingsService
	directoryService      *services.DirectoryService
	subtitleService       *services.SubtitleService
	cleanupService        *services.CleanupService
	subtitleSearchService *services.SubtitleSearchService
	startupError          string
	logFile               *os.File // 保持日志文件句柄引用，防止泄漏
}

// NewApp creates a new App application struct
func NewApp() *App {
	// 获取用户目录作为数据根目录
	homeDir, _ := os.UserHomeDir()
	dataDir := filepath.Join(homeDir, ".video-master")

	return &App{
		videoService:          &services.VideoService{},
		tagService:            &services.TagService{},
		settingsService:       &services.SettingsService{},
		directoryService:      &services.DirectoryService{},
		subtitleService:       services.NewSubtitleService(dataDir),
		cleanupService:        &services.CleanupService{},
		subtitleSearchService: &services.SubtitleSearchService{},
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.startupError != "" {
		return
	}
	a.subtitleService.SetContext(ctx) // Inject context
	if settings, err := a.settingsService.GetSettings(); err == nil {
		a.setLogEnabled(settings.LogEnabled)
	}
}

func (a *App) setStartupError(err error) {
	if err == nil {
		a.startupError = ""
		return
	}
	a.startupError = err.Error()
}

func (a *App) GetStartupError() string {
	return a.startupError
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
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
			a.closeLogFile() // 先关闭旧句柄
			a.logFile = f
			log.SetOutput(f)
		}
	}
}

// ===== Video Methods =====

// GetAllVideos 获取所有视频（保持兼容，实际使用分页）
func (a *App) GetAllVideos() ([]models.Video, error) {
	return a.videoService.GetAllVideos()
}

// GetVideosPaginated 分页获取视频
func (a *App) GetVideosPaginated(cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.GetVideosPaginated(cursorScore, cursorSize, cursorID, limit)
	log.Printf("API GetVideosPaginated cursorScore=%.4f cursorSize=%d cursorID=%d limit=%d result=%d err=%v", cursorScore, cursorSize, cursorID, limit, len(videos), err)
	return videos, err
}

// SearchVideos 搜索视频（支持分页）
func (a *App) SearchVideos(keyword string, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.SearchVideos(keyword, cursorScore, cursorSize, cursorID, limit)
	log.Printf("API SearchVideos keyword=%q cursorScore=%.4f cursorSize=%d cursorID=%d limit=%d result=%d err=%v", keyword, cursorScore, cursorSize, cursorID, limit, len(videos), err)
	return videos, err
}

// SearchSubtitleMatches 按字幕内容搜索视频片段
func (a *App) SearchSubtitleMatches(keyword string, limit int) ([]services.SubtitleSearchMatch, error) {
	matches, err := a.subtitleSearchService.SearchSubtitleMatches(keyword, limit)
	log.Printf("API SearchSubtitleMatches keyword=%q limit=%d result=%d err=%v", keyword, limit, len(matches), err)
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
	log.Printf("API SearchVideosWithFilters keyword=%q tags=%v size=[%d,%d] height=[%d,%d] cursorScore=%.4f cursorSize=%d cursorID=%d limit=%d result=%d err=%v", keyword, tagIDs, minSize, maxSize, minHeight, maxHeight, cursorScore, cursorSize, cursorID, limit, len(videos), err)
	return videos, err
}

// SelectDirectory 选择目录对话框
func (a *App) SelectDirectory() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择视频目录",
	})
	return dir, err
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

// RefreshVideoMetadata 刷新并补全视频元数据 (时长/分辨率)
func (a *App) RefreshVideoMetadata(id uint) error {
	return a.videoService.RefreshVideoMetadata(id)
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
	log.Printf("API GetVideosByDirectory dir=%s result=%d err=%v", dir, len(videos), err)
	return videos, err
}

// DeleteVideo 删除视频
func (a *App) DeleteVideo(id uint, deleteFile bool) error {
	err := a.videoService.DeleteVideo(id, deleteFile)
	log.Printf("API DeleteVideo id=%d deleteFile=%v err=%v", id, deleteFile, err)
	return err
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

// AddTagToVideo 为视频添加标签
func (a *App) AddTagToVideo(videoID uint, tagID uint) error {
	err := a.videoService.AddTagToVideo(videoID, tagID)
	log.Printf("API AddTagToVideo videoID=%d tagID=%d err=%v", videoID, tagID, err)
	return err
}

// RemoveTagFromVideo 移除视频标签
func (a *App) RemoveTagFromVideo(videoID uint, tagID uint) error {
	err := a.videoService.RemoveTagFromVideo(videoID, tagID)
	log.Printf("API RemoveTagFromVideo videoID=%d tagID=%d err=%v", videoID, tagID, err)
	return err
}

// ===== Tag Methods =====

// GetAllTags 获取所有标签
func (a *App) GetAllTags() ([]models.Tag, error) {
	tags, err := a.tagService.GetAllTags()
	log.Printf("API GetAllTags result=%d err=%v", len(tags), err)
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

// ===== Settings Methods =====

// GetSettings 获取设置
func (a *App) GetSettings() (*models.Settings, error) {
	settings, err := a.settingsService.GetSettings()
	if err == nil {
		a.setLogEnabled(settings.LogEnabled)
	}
	return settings, err
}

// UpdateSettings 更新设置
func (a *App) UpdateSettings(input models.Settings) error {
	err := a.settingsService.UpdateSettings(input)
	if err == nil {
		a.setLogEnabled(input.LogEnabled)
	}
	return err
}

// ===== Directory Methods =====

// GetAllDirectories 获取所有扫描目录
func (a *App) GetAllDirectories() ([]models.ScanDirectory, error) {
	return a.directoryService.GetAllDirectories()
}

// AddDirectory 添加扫描目录
func (a *App) AddDirectory(path, alias string) (*models.ScanDirectory, error) {
	return a.directoryService.AddDirectory(path, alias)
}

// UpdateDirectory 更新目录
func (a *App) UpdateDirectory(id uint, path, alias string) error {
	return a.directoryService.UpdateDirectory(id, path, alias)
}

// DeleteDirectory 删除扫描目录
func (a *App) DeleteDirectory(id uint) error {
	return a.directoryService.DeleteDirectory(id)
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
	// 获取双语字幕配置
	settings, _ := a.settingsService.GetSettings()
	bilingualEnabled := false
	bilingualLang := "zh"
	deeplApiKey := ""
	if settings != nil {
		bilingualEnabled = settings.BilingualEnabled
		bilingualLang = settings.BilingualLang
		deeplApiKey = settings.DeepLApiKey
	}
	log.Printf("API GenerateSubtitle id=%d path=%s engine=%s bilingual=%v lang=%s source=%s", req.VideoID, video.Path, req.Engine, bilingualEnabled, bilingualLang, req.SourceLang)
	return a.subtitleService.GenerateSubtitle(req, video.Path, bilingualEnabled, bilingualLang, deeplApiKey, false)
}

// ForceGenerateSubtitle 强制生成字幕（跳过幻觉检测）
func (a *App) ForceGenerateSubtitle(req services.SubtitleGenerateRequest) (*services.SubtitleGenerateResult, error) {
	video, err := a.videoService.GetVideo(req.VideoID)
	if err != nil {
		return nil, err
	}
	settings, _ := a.settingsService.GetSettings()
	bilingualEnabled := false
	bilingualLang := "zh"
	deeplApiKey := ""
	if settings != nil {
		bilingualEnabled = settings.BilingualEnabled
		bilingualLang = settings.BilingualLang
		deeplApiKey = settings.DeepLApiKey
	}
	log.Printf("API ForceGenerateSubtitle id=%d path=%s engine=%s source=%s", req.VideoID, video.Path, req.Engine, req.SourceLang)
	return a.subtitleService.GenerateSubtitle(req, video.Path, bilingualEnabled, bilingualLang, deeplApiKey, true)
}

// CancelSubtitle 取消正在进行的字幕生成任务
func (a *App) CancelSubtitle() {
	a.subtitleService.CancelGeneration()
	log.Printf("API CancelSubtitle")
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

// GetCleanupCandidates 获取清理候选（轻量规则）
func (a *App) GetCleanupCandidates(minDurationSeconds int, minWidth int, minHeight int) (*services.CleanupAnalysis, error) {
	criteria := services.CleanupCriteria{
		MinDuration: time.Duration(minDurationSeconds) * time.Second,
		MinWidth:    minWidth,
		MinHeight:   minHeight,
	}
	analysis, err := a.cleanupService.AnalyzeCleanupCandidates(criteria)
	if err != nil {
		log.Printf("API GetCleanupCandidates duration=%d width=%d height=%d err=%v", minDurationSeconds, minWidth, minHeight, err)
		return nil, err
	}

	log.Printf("API GetCleanupCandidates duration=%d width=%d height=%d duplicate_groups=%d low_duration=%d low_resolution=%d",
		minDurationSeconds, minWidth, minHeight,
		len(analysis.DuplicateGroups), len(analysis.LowDuration), len(analysis.LowResolution),
	)
	return analysis, nil
}
