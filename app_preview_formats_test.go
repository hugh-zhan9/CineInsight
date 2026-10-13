package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// 2026-10-13「直接播 + 自动代理」的真机链路：真实 ffmpeg 合成样片，经 App 的 GetPreviewSession /
// RequestPreviewProxy 走完判定、自动代理与切回内嵌。本机没有 ffmpeg / ffprobe 时 skip。
func TestPreviewFormatsEndToEndWithRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("本机没有 ffmpeg: %v", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skipf("本机没有 ffprobe: %v", err)
	}
	setupAppTestDB(t)
	root := t.TempDir()
	app := NewApp()
	proxies := services.NewPlaybackProxyService(filepath.Join(root, "data"), services.NewMediaProbeService())
	t.Cleanup(proxies.StopAndWait)
	app.playbackProxies = proxies
	app.videoService.SetPlaybackProxyService(proxies)

	samples := []struct {
		name, strategy string
		codecArgs      []string
	}{
		{"sample.mov", "", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac"}},
		{"sample.mkv", models.PlaybackProxyStrategyRemux, []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac"}},
		{"sample.avi", models.PlaybackProxyStrategyTranscode, []string{"-c:v", "mpeg4", "-vtag", "xvid", "-c:a", "libmp3lame"}},
	}
	for _, sample := range samples {
		path := filepath.Join(root, sample.name)
		args := append([]string{"-hide_banner", "-v", "error", "-y",
			"-f", "lavfi", "-i", "testsrc=duration=5:size=1280x720:rate=30",
			"-f", "lavfi", "-i", "sine=duration=5"}, sample.codecArgs...)
		if output, err := exec.Command(ffmpeg, append(args, path)...).CombinedOutput(); err != nil {
			t.Skipf("合成 %s 失败（本机 ffmpeg 缺编码器）: %v\n%s", sample.name, err, output)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		video := models.Video{Name: sample.name, Path: path, Directory: root, Size: info.Size(), Duration: 5}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}

		started := time.Now()
		session, err := app.GetPreviewSession(video.ID)
		if err != nil {
			t.Fatalf("%s 读取会话失败: %v", sample.name, err)
		}
		decided := time.Since(started)
		if sample.strategy == "" {
			if session.Mode != "inline" || session.Proxy != nil || session.InlineSource.MIME != "video/quicktime" {
				t.Fatalf("%s 应当直接内嵌源文件: %#v", sample.name, session)
			}
			t.Logf("%s：直放判定（含首次 ffprobe 补探）%s", sample.name, decided)
			continue
		}
		if session.Mode != "proxy-preparing" {
			t.Fatalf("%s 应当等待代理: %#v", sample.name, session)
		}
		enqueued := time.Now()
		if _, err := app.RequestPreviewProxy(video.ID); err != nil {
			t.Fatalf("%s 自动代理入队失败: %v", sample.name, err)
		}
		waitForAppProxyIdle(t, app)
		elapsed := time.Since(enqueued)
		status := app.GetPlaybackProxyStatus()
		last := status.Results[len(status.Results)-1]
		if last.VideoID != video.ID || last.Code != services.PlaybackProxyCodeCreated || last.Strategy != sample.strategy {
			t.Fatalf("%s 应当以 %s 生成代理: %#v", sample.name, sample.strategy, last)
		}
		session, err = app.GetPreviewSession(video.ID)
		if err != nil || session.Mode != "inline" || session.Proxy == nil || session.Proxy.Strategy != sample.strategy {
			t.Fatalf("%s 代理就绪后应当内嵌代理: %#v err=%v", sample.name, session, err)
		}
		t.Logf("%s：判定 %s，自动代理（%s）%s，产物 %d 字节", sample.name, decided, sample.strategy, elapsed, session.Proxy.Size)
	}
}

// 预览关闭时取消正在转码的自动代理：真实 ffmpeg 被停掉、不留临时文件、不落失败记录。
func TestPreviewProxyCancelStopsRealTranscode(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("本机没有 ffmpeg: %v", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skipf("本机没有 ffprobe: %v", err)
	}
	setupAppTestDB(t)
	root := t.TempDir()
	app := NewApp()
	dataDir := filepath.Join(root, "data")
	proxies := services.NewPlaybackProxyService(dataDir, services.NewMediaProbeService())
	t.Cleanup(proxies.StopAndWait)
	app.playbackProxies = proxies
	app.videoService.SetPlaybackProxyService(proxies)

	path := filepath.Join(root, "long.avi")
	synth := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=60:size=1920x1080:rate=30",
		"-c:v", "mpeg4", "-q:v", "2", "-an", path)
	if output, err := synth.CombinedOutput(); err != nil {
		t.Skipf("合成样片失败: %v\n%s", err, output)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "long.avi", Path: path, Directory: root, Size: info.Size(), Duration: 60}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := app.RequestPreviewProxy(video.ID); err != nil {
		t.Fatal(err)
	}
	// 等到 ffmpeg 真的在写临时产物再取消：取消的是一个正在跑的转码进程，而不是还没开始的项。
	tempGlob := filepath.Join(dataDir, "proxies", ".tmp", "*")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if found, _ := filepath.Glob(tempGlob); len(found) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if found, _ := filepath.Glob(tempGlob); len(found) == 0 {
		t.Fatalf("转码没有开始写临时产物: %#v", app.GetPlaybackProxyStatus())
	}
	cancelledAt := time.Now()
	if !app.CancelPreviewProxy(video.ID) {
		t.Fatalf("处理中的自动代理应可取消: %#v", app.GetPlaybackProxyStatus())
	}
	waitForAppProxyIdle(t, app)
	status := app.GetPlaybackProxyStatus()
	last := status.Results[len(status.Results)-1]
	if last.VideoID != video.ID || last.Code != services.PlaybackProxyCodeCancelled {
		t.Fatalf("应当记为取消: %#v", status)
	}
	t.Logf("取消到 worker 收尾 %s", time.Since(cancelledAt))
	leftovers, _ := filepath.Glob(tempGlob)
	if len(leftovers) != 0 {
		t.Fatalf("取消后不该留下临时文件: %v", leftovers)
	}
	if view, err := app.GetPlaybackProxy(video.ID); err != nil || view != nil {
		t.Fatalf("取消不该落代理记录: %#v err=%v", view, err)
	}
}
