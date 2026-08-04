package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	thumbnailRoutePrefix           = "/preview/thumbnail/"
	seekSpriteRoutePrefix          = "/preview/seek-sprite/"
	seekSpriteFrameWidth           = 160
	seekSpriteFrameHeight          = 90
	seekSpriteMaxColumns           = 10
	seekSpriteMaxFrames            = 100
	seekSpriteTargetInterval       = 10.0
	seekSpriteCacheLimit     int64 = 256 << 20
)

// ThumbnailMedia 描述可由 HTTP 资源处理器返回的缩略图。
type ThumbnailMedia struct {
	Path    string
	ModTime time.Time
}

type thumbnailRunner func(ctx context.Context, binary, sourcePath, destinationPath string, seekSeconds float64) error
type seekSpriteRunner func(ctx context.Context, binary, sourcePath, destinationPath string, descriptor SeekSpriteDescriptor) error

// SeekSpriteDescriptor 描述 seek sprite 的帧索引和资源位置。
type SeekSpriteDescriptor struct {
	LocatorValue    string  `json:"locator_value"`
	FrameWidth      int     `json:"frame_width"`
	FrameHeight     int     `json:"frame_height"`
	Columns         int     `json:"columns"`
	Rows            int     `json:"rows"`
	FrameCount      int     `json:"frame_count"`
	IntervalSeconds float64 `json:"interval_seconds"`
}

// ErrSeekSpriteNotReady 表示 sprite 尚未生成完成，后台任务已经排队。
var ErrSeekSpriteNotReady = errors.New("seek sprite 尚未生成")

// ThumbnailService 管理可重建的本地视频缩略图缓存。
type ThumbnailService struct {
	videoService            *VideoService
	cacheDir                string
	findFFmpeg              func() (string, error)
	runFFmpeg               thumbnailRunner
	runSeekSprite           seekSpriteRunner
	maxSeekSpriteCacheBytes int64
	locks                   sync.Map
	spriteLocks             sync.Map
	spriteJobs              sync.Map
	spriteSem               chan struct{}
	cacheMu                 sync.Mutex
}

// NewThumbnailService 创建缩略图服务。
func NewThumbnailService(videoService *VideoService, dataDir string) *ThumbnailService {
	return &ThumbnailService{
		videoService:            videoService,
		cacheDir:                filepath.Join(dataDir, "thumbnails"),
		findFFmpeg:              findThumbnailFFmpeg,
		runFFmpeg:               runThumbnailFFmpeg,
		runSeekSprite:           runSeekSpriteFFmpeg,
		maxSeekSpriteCacheBytes: seekSpriteCacheLimit,
		spriteSem:               make(chan struct{}, 1),
	}
}

// ThumbnailPath 返回主前端可使用的缩略图资源路径。
func ThumbnailPath(videoID uint) string {
	return fmt.Sprintf("%s%d", thumbnailRoutePrefix, videoID)
}

// SeekSpriteIndex 返回给定时长对应的固定 seek sprite 索引。
func SeekSpriteIndex(videoID uint, durationSeconds float64) *SeekSpriteDescriptor {
	if videoID == 0 || !isFinitePositive(durationSeconds) {
		return nil
	}
	frameCount := int(math.Ceil(durationSeconds / seekSpriteTargetInterval))
	if frameCount < 1 {
		frameCount = 1
	}
	if frameCount > seekSpriteMaxFrames {
		frameCount = seekSpriteMaxFrames
	}
	columns := frameCount
	if columns > seekSpriteMaxColumns {
		columns = seekSpriteMaxColumns
	}
	return &SeekSpriteDescriptor{
		LocatorValue:    fmt.Sprintf("%s%d", seekSpriteRoutePrefix, videoID),
		FrameWidth:      seekSpriteFrameWidth,
		FrameHeight:     seekSpriteFrameHeight,
		Columns:         columns,
		Rows:            (frameCount + columns - 1) / columns,
		FrameCount:      frameCount,
		IntervalSeconds: durationSeconds / float64(frameCount),
	}
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

// seekSpriteCacheName 把帧数与采样间隔编进缓存名：DB 时长被元数据重探修正
// 后描述符变化，旧缓存自然失配并重建，避免 hover 网格与描述符错位。
func seekSpriteCacheName(videoID uint, descriptor SeekSpriteDescriptor) string {
	return fmt.Sprintf("%d.seek.f%d-i%d.jpg", videoID, descriptor.FrameCount, int(math.Round(descriptor.IntervalSeconds*1000)))
}

// ResolveSeekSprite 只返回已就绪的 seek sprite 缓存；未就绪时排入后台生成
// 并返回 ErrSeekSpriteNotReady。生成不占用请求生命周期，也不与缩略图共用锁，
// 长视频的全片解码不会阻塞悬停请求或同视频的缩略图路由。
func (s *ThumbnailService) ResolveSeekSprite(ctx context.Context, videoID uint) (*ThumbnailMedia, error) {
	if videoID == 0 {
		return nil, fmt.Errorf("视频 ID 不能为空")
	}
	video, err := s.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}
	descriptor := SeekSpriteIndex(videoID, video.Duration)
	if descriptor == nil {
		return nil, fmt.Errorf("视频时长无效，无法生成 seek sprite")
	}
	sourceInfo, err := os.Stat(video.Path)
	if err != nil {
		return nil, err
	}
	if sourceInfo.IsDir() {
		return nil, fmt.Errorf("seek sprite 源路径不是文件")
	}
	cachePath := filepath.Join(s.cacheDir, seekSpriteCacheName(videoID, *descriptor))
	if media, ok := validThumbnailCache(cachePath, sourceInfo.ModTime()); ok {
		return media, nil
	}
	s.ensureSeekSpriteAsync(videoID)
	return nil, ErrSeekSpriteNotReady
}

// ensureSeekSpriteAsync 为单个视频排队一次后台生成；全局同时只跑一个
// sprite 任务，重复请求在任务完成前直接去重。
func (s *ThumbnailService) ensureSeekSpriteAsync(videoID uint) {
	if _, running := s.spriteJobs.LoadOrStore(videoID, struct{}{}); running {
		return
	}
	go func() {
		defer s.spriteJobs.Delete(videoID)
		s.spriteSem <- struct{}{}
		defer func() { <-s.spriteSem }()
		_, _ = s.generateSeekSprite(context.Background(), videoID)
	}()
}

// generateSeekSprite 同步生成并提交 seek sprite 缓存。
func (s *ThumbnailService) generateSeekSprite(ctx context.Context, videoID uint) (*ThumbnailMedia, error) {
	video, err := s.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}
	descriptor := SeekSpriteIndex(videoID, video.Duration)
	if descriptor == nil {
		return nil, fmt.Errorf("视频时长无效，无法生成 seek sprite")
	}
	sourceInfo, err := os.Stat(video.Path)
	if err != nil {
		return nil, err
	}
	if sourceInfo.IsDir() {
		return nil, fmt.Errorf("seek sprite 源路径不是文件")
	}
	if err := os.MkdirAll(s.cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("创建缩略图缓存目录失败: %w", err)
	}

	lockValue, _ := s.spriteLocks.LoadOrStore(videoID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	cachePath := filepath.Join(s.cacheDir, seekSpriteCacheName(videoID, *descriptor))
	if media, ok := validThumbnailCache(cachePath, sourceInfo.ModTime()); ok {
		return media, nil
	}

	ffmpegPath, err := s.findFFmpeg()
	if err != nil {
		return nil, err
	}
	tempFile, err := os.CreateTemp(s.cacheDir, fmt.Sprintf(".%d-seek-*.jpg", videoID))
	if err != nil {
		return nil, fmt.Errorf("创建 seek sprite 临时文件失败: %w", err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("关闭 seek sprite 临时文件失败: %w", err)
	}
	if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("准备 seek sprite 临时路径失败: %w", err)
	}
	defer os.Remove(tempPath)

	if err := s.runSeekSprite(ctx, ffmpegPath, video.Path, tempPath, *descriptor); err != nil {
		return nil, fmt.Errorf("生成 seek sprite 失败: %w", err)
	}
	generatedInfo, err := os.Stat(tempPath)
	if err != nil {
		return nil, fmt.Errorf("读取生成的 seek sprite 失败: %w", err)
	}
	if generatedInfo.IsDir() || generatedInfo.Size() == 0 {
		return nil, fmt.Errorf("生成的 seek sprite 为空")
	}
	if generatedInfo.Size() > s.maxSeekSpriteCacheBytes {
		return nil, fmt.Errorf("生成的 seek sprite 超过缓存上限")
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if runtime.GOOS == "windows" {
		if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("替换旧 seek sprite 失败: %w", err)
		}
	}
	if err := os.Rename(tempPath, cachePath); err != nil {
		return nil, fmt.Errorf("提交 seek sprite 缓存失败: %w", err)
	}
	if sourceInfo.ModTime().After(time.Now()) {
		_ = os.Chtimes(cachePath, sourceInfo.ModTime(), sourceInfo.ModTime())
	}
	cacheInfo, err := os.Stat(cachePath)
	if err != nil {
		return nil, err
	}
	if variants, globErr := filepath.Glob(filepath.Join(s.cacheDir, fmt.Sprintf("%d.seek*.jpg", videoID))); globErr == nil {
		for _, variant := range variants {
			if variant != cachePath {
				_ = os.Remove(variant)
			}
		}
	}
	s.pruneSeekSpriteCacheLocked(cachePath)
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

func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func (s *ThumbnailService) pruneSeekSpriteCacheLocked(keepPath string) {
	entries, err := filepath.Glob(filepath.Join(s.cacheDir, "*.seek*.jpg"))
	if err != nil {
		return
	}
	type cacheEntry struct {
		path    string
		size    int64
		modTime time.Time
	}
	files := make([]cacheEntry, 0, len(entries))
	var total int64
	for _, path := range entries {
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			continue
		}
		files = append(files, cacheEntry{path: path, size: info.Size(), modTime: info.ModTime()})
		total += info.Size()
	}
	if total <= s.maxSeekSpriteCacheBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for _, file := range files {
		if total <= s.maxSeekSpriteCacheBytes {
			return
		}
		if file.path == keepPath {
			continue
		}
		if err := os.Remove(file.path); err == nil || os.IsNotExist(err) {
			total -= file.size
		}
	}
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

func runSeekSpriteFFmpeg(ctx context.Context, binary, sourcePath, destinationPath string, descriptor SeekSpriteDescriptor) error {
	interval := strconv.FormatFloat(descriptor.IntervalSeconds, 'f', 6, 64)
	filter := fmt.Sprintf(
		"fps=1/%s,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,tile=%dx%d:nb_frames=%d",
		interval,
		descriptor.FrameWidth,
		descriptor.FrameHeight,
		descriptor.FrameWidth,
		descriptor.FrameHeight,
		descriptor.Columns,
		descriptor.Rows,
		descriptor.FrameCount,
	)
	cmd := exec.CommandContext(ctx, binary,
		"-v", "error",
		"-i", sourcePath,
		"-an",
		"-vf", filter,
		"-frames:v", "1",
		"-q:v", "5",
		"-y", destinationPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, truncateLogSnippet(string(output), 400))
	}
	return nil
}
