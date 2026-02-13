package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"video-master/models"
	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx              context.Context
	videoService     *services.VideoService
	tagService       *services.TagService
	settingsService  *services.SettingsService
	directoryService *services.DirectoryService
	logFile          *os.File // 保持日志文件句柄引用，防止泄漏
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{
		videoService:     &services.VideoService{},
		tagService:       &services.TagService{},
		settingsService:  &services.SettingsService{},
		directoryService: &services.DirectoryService{},
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if settings, err := a.settingsService.GetSettings(); err == nil {
		a.setLogEnabled(settings.LogEnabled)
	}
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

// SearchVideosByTags 按标签搜索视频（多选 AND，支持分页）
func (a *App) SearchVideosByTags(tagIDs []uint, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.SearchVideosByTags(tagIDs, cursorScore, cursorSize, cursorID, limit)
	log.Printf("API SearchVideosByTags tags=%v cursorScore=%.4f cursorSize=%d cursorID=%d limit=%d result=%d err=%v", tagIDs, cursorScore, cursorSize, cursorID, limit, len(videos), err)
	return videos, err
}

// SearchVideosWithFilters 组合搜索视频（名称 + 标签，支持分页）
func (a *App) SearchVideosWithFilters(keyword string, tagIDs []uint, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.SearchVideosWithFilters(keyword, tagIDs, cursorScore, cursorSize, cursorID, limit)
	log.Printf("API SearchVideosWithFilters keyword=%q tags=%v cursorScore=%.4f cursorSize=%d cursorID=%d limit=%d result=%d err=%v", keyword, tagIDs, cursorScore, cursorSize, cursorID, limit, len(videos), err)
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

// PlayVideo 播放视频
func (a *App) PlayVideo(videoID uint) error {
	err := a.videoService.PlayVideo(videoID)
	log.Printf("API PlayVideo id=%d err=%v", videoID, err)
	return err
}

// PlayRandomVideo 随机播放视频
func (a *App) PlayRandomVideo() (*models.Video, error) {
	video, err := a.videoService.PlayRandomVideo()
	if video != nil {
		log.Printf("API PlayRandomVideo id=%d err=%v", video.ID, err)
	} else {
		log.Printf("API PlayRandomVideo id=0 err=%v", err)
	}
	return video, err
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
