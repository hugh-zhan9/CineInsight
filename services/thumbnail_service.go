package services

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
)

const thumbnailRoutePrefix = "/preview/thumbnail/"

// ThumbnailMedia 描述可由 HTTP 资源处理器返回的缩略图。
type ThumbnailMedia struct {
	Path    string
	ModTime time.Time
}

type thumbnailRunner func(ctx context.Context, binary, sourcePath, destinationPath string, seekSeconds float64) error

// ThumbnailService 管理可重建的本地视频缩略图缓存。
type ThumbnailService struct {
	videoService *VideoService
	cacheDir     string
	findFFmpeg   func() (string, error)
	runFFmpeg    thumbnailRunner
	locks        sync.Map
}

// NewThumbnailService 创建缩略图服务。
func NewThumbnailService(videoService *VideoService, dataDir string) *ThumbnailService {
	return &ThumbnailService{
		videoService: videoService,
		cacheDir:     filepath.Join(dataDir, "thumbnails"),
		findFFmpeg:   findThumbnailFFmpeg,
		runFFmpeg:    runThumbnailFFmpeg,
	}
}

// ThumbnailPath 返回主前端可使用的缩略图资源路径。
func ThumbnailPath(videoID uint) string {
	return fmt.Sprintf("%s%d", thumbnailRoutePrefix, videoID)
}

// ResolveThumbnail 返回有效缓存，必要时按源视频生成。
func (s *ThumbnailService) ResolveThumbnail(ctx context.Context, videoID uint) (*ThumbnailMedia, error) {
	if videoID == 0 {
		return nil, fmt.Errorf("视频 ID 不能为空")
	}
	video, err := s.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}
	sourceInfo, err := os.Stat(video.Path)
	if err != nil {
		return nil, err
	}
	if sourceInfo.IsDir() {
		return nil, fmt.Errorf("缩略图源路径不是文件")
	}
	if err := os.MkdirAll(s.cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("创建缩略图缓存目录失败: %w", err)
	}

	lockValue, _ := s.locks.LoadOrStore(videoID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	cachePath := filepath.Join(s.cacheDir, strconv.FormatUint(uint64(videoID), 10)+".jpg")
	if media, ok := validThumbnailCache(cachePath, sourceInfo.ModTime()); ok {
		return media, nil
	}

	ffmpegPath, err := s.findFFmpeg()
	if err != nil {
		return nil, err
	}
	tempFile, err := os.CreateTemp(s.cacheDir, fmt.Sprintf(".%d-*.jpg", videoID))
	if err != nil {
		return nil, fmt.Errorf("创建缩略图临时文件失败: %w", err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("关闭缩略图临时文件失败: %w", err)
	}
	if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("准备缩略图临时路径失败: %w", err)
	}
	defer os.Remove(tempPath)

	seekSeconds := thumbnailSeekSeconds(video.Duration)
	if err := s.runFFmpeg(ctx, ffmpegPath, video.Path, tempPath, seekSeconds); err != nil {
		return nil, fmt.Errorf("生成缩略图失败: %w", err)
	}
	generatedInfo, err := os.Stat(tempPath)
	if err != nil {
		return nil, fmt.Errorf("读取生成的缩略图失败: %w", err)
	}
	if generatedInfo.IsDir() || generatedInfo.Size() == 0 {
		return nil, fmt.Errorf("生成的缩略图为空")
	}
	if runtime.GOOS == "windows" {
		if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("替换旧缩略图失败: %w", err)
		}
	}
	if err := os.Rename(tempPath, cachePath); err != nil {
		return nil, fmt.Errorf("提交缩略图缓存失败: %w", err)
	}
	if sourceInfo.ModTime().After(time.Now()) {
		_ = os.Chtimes(cachePath, sourceInfo.ModTime(), sourceInfo.ModTime())
	}
	cacheInfo, err := os.Stat(cachePath)
	if err != nil {
		return nil, err
	}
	return &ThumbnailMedia{Path: cachePath, ModTime: cacheInfo.ModTime()}, nil
}

func validThumbnailCache(path string, sourceModTime time.Time) (*ThumbnailMedia, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 || info.ModTime().Before(sourceModTime) {
		return nil, false
	}
	return &ThumbnailMedia{Path: path, ModTime: info.ModTime()}, true
}

func thumbnailSeekSeconds(duration float64) float64 {
	seek := duration * 0.1
	if seek < 1 {
		seek = 1
	}
	if seek > 30 {
		seek = 30
	}
	return seek
}

func findThumbnailFFmpeg() (string, error) {
	if binary, err := exec.LookPath("ffmpeg"); err == nil {
		return binary, nil
	}
	if runtime.GOOS == "darwin" {
		for _, path := range []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg"} {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("未找到 FFmpeg，无法生成缩略图")
}

func runThumbnailFFmpeg(ctx context.Context, binary, sourcePath, destinationPath string, seekSeconds float64) error {
	cmd := exec.CommandContext(ctx, binary,
		"-v", "error",
		"-ss", strconv.FormatFloat(seekSeconds, 'f', 3, 64),
		"-i", sourcePath,
		"-frames:v", "1",
		"-vf", "scale=480:-2:force_original_aspect_ratio=decrease",
		"-q:v", "4",
		"-y", destinationPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, truncateLogSnippet(string(output), 400))
	}
	return nil
}
