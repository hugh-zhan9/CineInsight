package services

import (
	"context"
	"errors"
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
