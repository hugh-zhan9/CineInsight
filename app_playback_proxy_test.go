package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// 路由级：桌面预览 /preview/media/{id} 在有效代理存在时下发代理字节（D-004）。
//
// 这一条走真实 ffmpeg、真实 asset handler 与真实 GetPreviewSession：
// stub 测试钉的是策略与落位，这里钉的是"整条链路接起来之后前端拿到的是代理"。
// 本机没有 ffmpeg 时 skip。
func TestPreviewMediaRouteServesPlaybackProxy(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("本机没有 ffmpeg，跳过预览路由代理集成测试: %v", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skipf("本机没有 ffprobe，跳过预览路由代理集成测试: %v", err)
	}
	setupAppTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "route.mkv")
	synth := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=duration=2",
		"-c:v", "libx264", "-c:a", "aac", sourcePath)
	if output, err := synth.CombinedOutput(); err != nil {
		t.Skipf("合成 mkv 失败（本机 ffmpeg 缺少所需编码器）: %v\n%s", err, output)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatalf("读取合成文件失败: %v", err)
	}
	video := models.Video{Name: "route.mkv", Path: sourcePath, Directory: root, Size: info.Size(), Duration: 2}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	app := NewApp()
	// 代理写进测试自己的数据目录，绝不碰 ~/.CineInsight。
	proxies := services.NewPlaybackProxyService(filepath.Join(root, "data"), services.NewMediaProbeService())
	t.Cleanup(proxies.StopAndWait)
	app.playbackProxies = proxies
	app.videoService.SetPlaybackProxyService(proxies)
	handler := newAssetHandler(app)
	target := fmt.Sprintf("/preview/media/%d", video.ID)

	// 没有代理时：mkv 不在内嵌白名单里，会话给外部预览，路由下发源字节。
	session, err := app.videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "external-preview" || session.Proxy != nil {
		t.Fatalf("无代理时 mkv 应当只能外部预览: %#v", session)
	}

	if _, err := app.CreatePlaybackProxy(video.ID); err != nil {
		t.Fatalf("生成代理失败: %v", err)
	}
	waitForAppProxyIdle(t, app)
	if final := app.GetPlaybackProxyStatus(); final.Succeeded != 1 {
		t.Fatalf("代理应当生成成功: %#v", final)
	}

	session, err = app.videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "inline" || session.Proxy == nil {
		t.Fatalf("有代理时应当内嵌播放: %#v", session)
	}
	if session.InlineSource == nil || session.InlineSource.LocatorValue != target {
		t.Fatalf("locator 形态不该变: %#v", session.InlineSource)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("预览媒体路由应返回 200: %d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("命中代理时应报 video/mp4: %s", got)
	}
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("读取源文件失败: %v", err)
	}
	if bytes.Equal(recorder.Body.Bytes(), sourceBytes) {
		t.Fatalf("命中代理时不该下发源文件字节")
	}
	if recorder.Body.Len() == 0 {
		t.Fatalf("代理响应体为空")
	}

	usage, err := app.GetPlaybackProxyUsage()
	if err != nil {
		t.Fatalf("读取代理占用失败: %v", err)
	}
	if usage.Count != 1 || usage.TotalBytes != int64(recorder.Body.Len()) {
		t.Fatalf("占用统计应与产物一致: %#v body=%d", usage, recorder.Body.Len())
	}

	// videos 行数不变：代理不入库为视频记录。
	var count int64
	if err := database.DB.Model(&models.Video{}).Count(&count).Error; err != nil {
		t.Fatalf("统计视频失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("代理不该新增视频记录: videos=%d", count)
	}

	// 删除之后立刻退回无代理行为。
	if err := app.DeletePlaybackProxy(video.ID); err != nil {
		t.Fatalf("删除代理失败: %v", err)
	}
	session, err = app.videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "external-preview" || session.Proxy != nil {
		t.Fatalf("删除代理后应退回外部预览: %#v", session)
	}
}

func waitForAppProxyIdle(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !app.GetPlaybackProxyStatus().Running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待代理任务结束超时: %#v", app.GetPlaybackProxyStatus())
}

// 桌面预览路由必须支持 Range：内嵌 <video> 拖进度条靠的就是 206。
// 走真实 asset handler，本机没有 ffmpeg 时 skip。
func TestPreviewMediaRouteServesProxyRangeRequests(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("本机没有 ffmpeg，跳过预览路由 Range 测试: %v", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skipf("本机没有 ffprobe，跳过预览路由 Range 测试: %v", err)
	}
	setupAppTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "range.mkv")
	synth := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=duration=2",
		"-c:v", "libx264", "-c:a", "aac", sourcePath)
	if output, err := synth.CombinedOutput(); err != nil {
		t.Skipf("合成 mkv 失败: %v\n%s", err, output)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatalf("读取合成文件失败: %v", err)
	}
	video := models.Video{Name: "range.mkv", Path: sourcePath, Directory: root, Size: info.Size(), Duration: 2}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	app := NewApp()
	proxies := services.NewPlaybackProxyService(filepath.Join(root, "data"), services.NewMediaProbeService())
	t.Cleanup(proxies.StopAndWait)
	app.playbackProxies = proxies
	app.videoService.SetPlaybackProxyService(proxies)
	handler := newAssetHandler(app)
	target := fmt.Sprintf("/preview/media/%d", video.ID)

	if _, err := app.CreatePlaybackProxy(video.ID); err != nil {
		t.Fatalf("生成代理失败: %v", err)
	}
	waitForAppProxyIdle(t, app)
	if final := app.GetPlaybackProxyStatus(); final.Succeeded != 1 {
		t.Fatalf("代理应当生成成功: %#v", final)
	}

	// 先整取一遍拿到完整字节，作为分段比对的基准。
	whole := httptest.NewRecorder()
	handler.ServeHTTP(whole, httptest.NewRequest(http.MethodGet, target, nil))
	if whole.Code != http.StatusOK {
		t.Fatalf("整取应当返回 200: %d", whole.Code)
	}
	full := whole.Body.Bytes()
	if len(full) < 200 {
		t.Fatalf("产物太小，无法测 Range: %d", len(full))
	}

	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Range", "bytes=100-199")
	partial := httptest.NewRecorder()
	handler.ServeHTTP(partial, request)

	if partial.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求应当返回 206，实际 %d", partial.Code)
	}
	wantRange := fmt.Sprintf("bytes 100-199/%d", len(full))
	if got := partial.Header().Get("Content-Range"); got != wantRange {
		t.Fatalf("Content-Range 不符: got=%q want=%q", got, wantRange)
	}
	if got := partial.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("应当声明支持 Range: %q", got)
	}
	if got := partial.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("命中代理时应报 video/mp4: %s", got)
	}
	if partial.Body.Len() != 100 {
		t.Fatalf("应当只回 100 字节，实际 %d", partial.Body.Len())
	}
	if !bytes.Equal(partial.Body.Bytes(), full[100:200]) {
		t.Fatalf("回的应当正好是 [100,200) 这一段")
	}
}
