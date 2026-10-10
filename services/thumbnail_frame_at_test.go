package services

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func newFrameAtTestVideo(t *testing.T, duration float64) (*ThumbnailService, models.Video, string) {
	t.Helper()
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	mustCreateFile(t, videoPath)
	mustSetFileModTime(t, videoPath, time.Now().Add(-time.Hour).Round(time.Second))
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Duration: duration}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	service := NewThumbnailService(&VideoService{}, filepath.Join(root, "data"))
	service.findFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	return service, video, videoPath
}

func TestRenderFrameAtSeeksExactlyAndCachesBySourceVersion(t *testing.T) {
	service, video, videoPath := newFrameAtTestVideo(t, 100)
	type call struct {
		seek  float64
		width int
	}
	var calls []call
	service.runFrameAt = func(_ context.Context, _, source string, seek float64, width int) ([]byte, error) {
		if source != videoPath {
			t.Fatalf("源路径错误: %s", source)
		}
		calls = append(calls, call{seek, width})
		return []byte(fmt.Sprintf("jpeg-%v", seek)), nil
	}

	data, _, err := service.RenderFrameAt(context.Background(), video.ID, 61234, 0)
	if err != nil {
		t.Fatalf("抽帧失败: %v", err)
	}
	if string(data) != "jpeg-61.234" || len(calls) != 1 || calls[0].width != frameAtDefaultWidth {
		t.Fatalf("应按毫秒精确定位并使用默认宽度 data=%s calls=%v", data, calls)
	}
	if _, _, err := service.RenderFrameAt(context.Background(), video.ID, 61234, 0); err != nil || len(calls) != 1 {
		t.Fatalf("同一源版本应命中缓存 err=%v calls=%d", err, len(calls))
	}
	if _, _, err := service.RenderFrameAt(context.Background(), video.ID, 61234, 5000); err != nil || calls[len(calls)-1].width != frameAtMaxWidth {
		t.Fatalf("宽度应夹到上限 err=%v calls=%v", err, calls)
	}
	mustSetFileModTime(t, videoPath, time.Now().Round(time.Second))
	if _, _, err := service.RenderFrameAt(context.Background(), video.ID, 61234, 0); err != nil || len(calls) != 3 {
		t.Fatalf("源文件变化后应重新抽帧 err=%v calls=%d", err, len(calls))
	}
}

func TestRenderFrameAtRejectsOutOfRangeAndPropagatesFailures(t *testing.T) {
	service, video, _ := newFrameAtTestVideo(t, 10)
	runs := 0
	service.runFrameAt = func(context.Context, string, string, float64, int) ([]byte, error) {
		runs++
		return nil, errors.New("decode failed")
	}
	if _, _, err := service.RenderFrameAt(context.Background(), video.ID, 10001, 0); !errors.Is(err, ErrFrameAtOutOfRange) {
		t.Fatalf("超过时长应报越界 err=%v", err)
	}
	if _, _, err := service.RenderFrameAt(context.Background(), video.ID, -1, 0); !errors.Is(err, ErrFrameAtOutOfRange) {
		t.Fatalf("负数毫秒应报越界 err=%v", err)
	}
	if runs != 0 {
		t.Fatalf("越界不应调用 ffmpeg runs=%d", runs)
	}
	if _, _, err := service.RenderFrameAt(context.Background(), video.ID, 0, 0); err == nil || runs != 1 {
		t.Fatalf("抽帧失败应原样报错 err=%v runs=%d", err, runs)
	}
	if service.frameCache.len() != 0 {
		t.Fatalf("失败结果不应进入缓存")
	}
	service.runFrameAt = func(context.Context, string, string, float64, int) ([]byte, error) { return nil, nil }
	if _, _, err := service.RenderFrameAt(context.Background(), video.ID, 9000, 0); !errors.Is(err, ErrFrameAtOutOfRange) {
		t.Fatalf("空输出应视为越界 err=%v", err)
	}
}

func TestFrameAtCacheStaysBounded(t *testing.T) {
	cache := newFrameAtCache(3)
	for i := 0; i < 5; i++ {
		cache.put(frameAtImage{key: fmt.Sprint(i), data: []byte{byte(i)}})
	}
	if cache.len() != 3 {
		t.Fatalf("缓存容量应为 3，实际 %d", cache.len())
	}
	if _, ok := cache.get("0"); ok {
		t.Fatalf("最旧的条目应被淘汰")
	}
	if _, ok := cache.get("4"); !ok {
		t.Fatalf("最新条目应保留")
	}
}
