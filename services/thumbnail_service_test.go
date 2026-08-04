package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestThumbnailServiceCachesAndInvalidatesBySourceModTime(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	mustCreateFile(t, videoPath)
	sourceTime := time.Now().Add(-time.Hour).Round(time.Second)
	mustSetFileModTime(t, videoPath, sourceTime)
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Duration: 100}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	service := NewThumbnailService(&VideoService{}, filepath.Join(root, "data"))
	service.findFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	runs := 0
	service.runFFmpeg = func(_ context.Context, _, _, destination string, seek float64) error {
		runs++
		if seek != 10 {
			t.Fatalf("seek 错误 got=%v want=10", seek)
		}
		return os.WriteFile(destination, []byte("jpeg"), 0644)
	}

	first, err := service.ResolveThumbnail(context.Background(), video.ID)
	if err != nil {
		t.Fatalf("首次生成失败: %v", err)
	}
	if _, err := service.ResolveThumbnail(context.Background(), video.ID); err != nil {
		t.Fatalf("缓存命中失败: %v", err)
	}
	if runs != 1 {
		t.Fatalf("缓存命中不应重复生成 runs=%d", runs)
	}
	future := first.ModTime.Add(time.Hour)
	mustSetFileModTime(t, videoPath, future)
	if _, err := service.ResolveThumbnail(context.Background(), video.ID); err != nil {
		t.Fatalf("缓存失效重建失败: %v", err)
	}
	if runs != 2 {
		t.Fatalf("源文件更新后应重建 runs=%d", runs)
	}
}

func TestThumbnailServiceDoesNotPublishFailedGeneration(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Duration: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	service := NewThumbnailService(&VideoService{}, filepath.Join(root, "data"))
	service.findFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	service.runFFmpeg = func(_ context.Context, _, _, destination string, _ float64) error {
		if err := os.WriteFile(destination, []byte("partial"), 0644); err != nil {
			return err
		}
		return errors.New("generation failed")
	}

	if _, err := service.ResolveThumbnail(context.Background(), video.ID); err == nil {
		t.Fatalf("生成失败应返回错误")
	}
	videoIDText := strconv.FormatUint(uint64(video.ID), 10)
	cachePath := filepath.Join(service.cacheDir, videoIDText+".jpg")
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("失败生成不应发布缓存: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(service.cacheDir, "."+videoIDText+"-*.jpg"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("失败生成应清理临时文件 matches=%v err=%v", matches, err)
	}
}

func TestThumbnailServiceSerializesConcurrentGenerationPerVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Duration: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	service := NewThumbnailService(&VideoService{}, filepath.Join(root, "data"))
	service.findFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	var mu sync.Mutex
	runs := 0
	service.runFFmpeg = func(_ context.Context, _, _, destination string, _ float64) error {
		mu.Lock()
		runs++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		return os.WriteFile(destination, []byte("jpeg"), 0644)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.ResolveThumbnail(context.Background(), video.ID)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发生成失败: %v", err)
		}
	}
	if runs != 1 {
		t.Fatalf("同一视频并发请求应只生成一次 runs=%d", runs)
	}
}

func TestSeekSpriteIndexCapsFramesAndKeepsSamplingErrorBounded(t *testing.T) {
	index := SeekSpriteIndex(7, 7200)
	if index == nil {
		t.Fatalf("长视频应返回 seek sprite 索引")
	}
	if index.LocatorValue != "/preview/seek-sprite/7" {
		t.Fatalf("locator 错误: %s", index.LocatorValue)
	}
	if index.FrameCount != seekSpriteMaxFrames || index.Columns != 10 || index.Rows != 10 {
		t.Fatalf("长视频索引布局错误: %+v", index)
	}
	if index.IntervalSeconds != 72 {
		t.Fatalf("长视频采样间隔错误: %v", index.IntervalSeconds)
	}

	targetSeconds := 7199.0
	frame := int(targetSeconds / index.IntervalSeconds)
	if frame >= index.FrameCount {
		frame = index.FrameCount - 1
	}
	frameSeconds := float64(frame) * index.IntervalSeconds
	if targetSeconds-frameSeconds >= index.IntervalSeconds {
		t.Fatalf("hover 定位误差超出采样间隔: target=%v frame=%v interval=%v", targetSeconds, frameSeconds, index.IntervalSeconds)
	}
	if SeekSpriteIndex(7, 0) != nil {
		t.Fatalf("未知时长不应暴露 seek sprite")
	}
}

func TestThumbnailServiceCachesInvalidatesAndBoundsSeekSprites(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	mustCreateFile(t, videoPath)
	sourceTime := time.Now().Add(-time.Hour).Round(time.Second)
	mustSetFileModTime(t, videoPath, sourceTime)
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Duration: 125}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	service := NewThumbnailService(&VideoService{}, filepath.Join(root, "data"))
	service.findFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	service.maxSeekSpriteCacheBytes = 12
	runs := 0
	service.runSeekSprite = func(_ context.Context, _, _, destination string, descriptor SeekSpriteDescriptor) error {
		runs++
		if descriptor.FrameCount != 13 || descriptor.Columns != 10 || descriptor.Rows != 2 {
			t.Fatalf("生成器收到错误索引: %+v", descriptor)
		}
		return os.WriteFile(destination, []byte("sprite"), 0644)
	}

	first, err := service.generateSeekSprite(context.Background(), video.ID)
	if err != nil {
		t.Fatalf("首次生成 seek sprite 失败: %v", err)
	}
	if _, err := service.ResolveSeekSprite(context.Background(), video.ID); err != nil {
		t.Fatalf("seek sprite 缓存命中失败: %v", err)
	}
	if runs != 1 {
		t.Fatalf("缓存命中不应重复生成 runs=%d", runs)
	}

	oldCache := filepath.Join(service.cacheDir, "999.seek.jpg")
	if err := os.WriteFile(oldCache, []byte("old-old"), 0644); err != nil {
		t.Fatalf("创建旧缓存失败: %v", err)
	}
	mustSetFileModTime(t, oldCache, time.Now().Add(-2*time.Hour))
	future := first.ModTime.Add(time.Hour)
	mustSetFileModTime(t, videoPath, future)
	if _, err := service.ResolveSeekSprite(context.Background(), video.ID); !errors.Is(err, ErrSeekSpriteNotReady) {
		t.Fatalf("源文件变化后缓存应失效并转入后台生成: %v", err)
	}
	if _, err := service.generateSeekSprite(context.Background(), video.ID); err != nil {
		t.Fatalf("源文件变化后重建 seek sprite 失败: %v", err)
	}
	if runs != 2 {
		t.Fatalf("源文件变化后应重建 runs=%d", runs)
	}
	if _, err := os.Stat(oldCache); !os.IsNotExist(err) {
		t.Fatalf("超出上限时应淘汰最旧 seek sprite: %v", err)
	}
	if info, err := os.Stat(filepath.Join(service.cacheDir, seekSpriteCacheName(video.ID, *SeekSpriteIndex(video.ID, video.Duration)))); err != nil || info.Size() != 6 {
		t.Fatalf("当前 seek sprite 应保留: info=%v err=%v", info, err)
	}
}

func TestThumbnailServiceDoesNotPublishFailedSeekSprite(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Duration: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	service := NewThumbnailService(&VideoService{}, filepath.Join(root, "data"))
	service.findFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	service.runSeekSprite = func(_ context.Context, _, _, destination string, _ SeekSpriteDescriptor) error {
		if err := os.WriteFile(destination, []byte("partial"), 0644); err != nil {
			return err
		}
		return errors.New("generation failed")
	}

	if _, err := service.generateSeekSprite(context.Background(), video.ID); err == nil {
		t.Fatalf("seek sprite 生成失败应返回错误")
	}
	cachePath := filepath.Join(service.cacheDir, seekSpriteCacheName(video.ID, *SeekSpriteIndex(video.ID, video.Duration)))
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("失败生成不应发布 seek sprite: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(service.cacheDir, fmt.Sprintf(".%d-seek-*.jpg", video.ID)))
	if err != nil || len(matches) != 0 {
		t.Fatalf("失败生成应清理 seek sprite 临时文件 matches=%v err=%v", matches, err)
	}
}

func TestResolveSeekSpriteQueuesBackgroundGenerationWithoutBlocking(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Duration: 125}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	service := NewThumbnailService(&VideoService{}, filepath.Join(root, "data"))
	service.findFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	generated := make(chan struct{}, 4)
	service.runSeekSprite = func(_ context.Context, _, _, destination string, _ SeekSpriteDescriptor) error {
		generated <- struct{}{}
		return os.WriteFile(destination, []byte("sprite"), 0644)
	}

	if _, err := service.ResolveSeekSprite(context.Background(), video.ID); !errors.Is(err, ErrSeekSpriteNotReady) {
		t.Fatalf("缓存未就绪应返回 ErrSeekSpriteNotReady: %v", err)
	}
	select {
	case <-generated:
	case <-time.After(3 * time.Second):
		t.Fatalf("后台生成未被触发")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		media, err := service.ResolveSeekSprite(context.Background(), video.ID)
		if err == nil && media != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("后台生成完成后应命中缓存: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
