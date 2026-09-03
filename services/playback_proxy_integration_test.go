package services

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

// 真实 ffmpeg 集成：本机装了 ffmpeg/ffprobe 才跑，否则 skip。
// stub 测试钉的是参数数组，这里钉的是"这套参数真的能产出可播的 mp4"——
// 前者防回归，后者防"参数看着对但 ffmpeg 不接受"。

func requireRealFFmpeg(t *testing.T) (string, string) {
	t.Helper()
	ffmpeg, err := findThumbnailFFmpeg()
	if err != nil {
		t.Skipf("本机没有 ffmpeg，跳过真实转封装集成测试: %v", err)
	}
	ffprobe, err := findFFProbeBinary()
	if err != nil {
		t.Skipf("本机没有 ffprobe，跳过真实转封装集成测试: %v", err)
	}
	return ffmpeg, ffprobe
}

func ffmpegHasEncoder(t *testing.T, ffmpeg, encoder string) bool {
	t.Helper()
	output, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil {
		t.Logf("列出 ffmpeg 编码器失败: %v", err)
		return false
	}
	return strings.Contains(string(output), encoder)
}

// synthesizeMedia 用 lavfi 合成一段素材，返回文件路径。
func synthesizeMedia(t *testing.T, ffmpeg, name string, args ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	full := append([]string{"-hide_banner", "-v", "error", "-y"}, args...)
	full = append(full, path)
	output, err := exec.Command(ffmpeg, full...).CombinedOutput()
	if err != nil {
		t.Skipf("合成 %s 失败（本机 ffmpeg 缺少所需编码器）: %v\n%s", name, err, output)
	}
	return path
}

// createRegisteredVideo 把一个已经存在的文件登记进片库。
func createRegisteredVideo(t *testing.T, path string, duration float64) models.Video {
	t.Helper()
	video := models.Video{
		Name:      filepath.Base(path),
		Path:      path,
		Directory: filepath.Dir(path),
		Duration:  duration,
	}
	if info, err := os.Stat(path); err == nil {
		video.Size = info.Size()
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("登记合成视频失败: %v", err)
	}
	return video
}

// newRealProxyService 用真 ffmpeg / ffprobe 跑完整流程。
func newRealProxyService(t *testing.T) *PlaybackProxyService {
	t.Helper()
	service := NewPlaybackProxyService(t.TempDir(), NewMediaProbeService())
	t.Cleanup(service.StopAndWait)
	return service
}

// H.264 + AAC 的 mkv：走 remux，产物可被 ffprobe 读出且时长与源相差不超过 2%。
func TestPlaybackProxyRealRemuxFromMKV(t *testing.T) {
	ffmpeg, _ := requireRealFFmpeg(t)
	setupVideoServiceTestDB(t)
	source := synthesizeMedia(t, ffmpeg, "real-remux.mkv",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=duration=2",
		"-c:v", "libx264", "-c:a", "aac")
	video := createRegisteredVideo(t, source, 2)

	service := newRealProxyService(t)
	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("真实 mkv 应当生成成功: %#v", result)
	}
	if result.Strategy != models.PlaybackProxyStrategyRemux {
		t.Fatalf("H.264 + AAC 应走 remux: %s", result.Strategy)
	}
	assertProxyPlayable(t, service, video, 2)
}

// mpeg4 的 avi：走 VideoToolbox 转码。本机没有该编码器时 skip 并说明。
func TestPlaybackProxyRealTranscodeFromAVI(t *testing.T) {
	ffmpeg, _ := requireRealFFmpeg(t)
	if !ffmpegHasEncoder(t, ffmpeg, "h264_videotoolbox") {
		t.Skip("本机 ffmpeg 没有 h264_videotoolbox 编码器（非 macOS 或自建 ffmpeg），跳过真实转码集成测试")
	}
	setupVideoServiceTestDB(t)
	source := synthesizeMedia(t, ffmpeg, "real-transcode.avi",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-c:v", "mpeg4", "-an")
	video := createRegisteredVideo(t, source, 2)

	service := newRealProxyService(t)
	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("真实 avi 应当生成成功: %#v", result)
	}
	if result.Strategy != models.PlaybackProxyStrategyTranscode {
		t.Fatalf("mpeg4 应走转码: %s", result.Strategy)
	}
	assertProxyPlayable(t, service, video, 2)
}

// assertProxyPlayable 用真 ffprobe 读产物并核对时长。
func assertProxyPlayable(t *testing.T, service *PlaybackProxyService, video models.Video, wantDuration float64) {
	t.Helper()
	row := mustLoadProxyRow(t, video.ID)
	path := service.proxyPathForRow(row)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("产物缺失: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("产物为空文件")
	}
	if row.OutputSize != info.Size() {
		t.Fatalf("产物大小应记进表里: row=%d file=%d", row.OutputSize, info.Size())
	}
	duration, err := probePlaybackProxyDuration(context.Background(), path)
	if err != nil {
		t.Fatalf("ffprobe 读不出产物: %v", err)
	}
	if math.Abs(duration-wantDuration)/wantDuration > playbackProxyDurationTolerance {
		t.Fatalf("产物时长 %.3fs 与源 %.3fs 相差超过 2%%", duration, wantDuration)
	}
	if names := proxyTempFiles(t, service); len(names) != 0 {
		t.Fatalf("临时目录应为空: %v", names)
	}
	// 产物只在代理目录里，源目录一个字节都没多。
	entries, err := os.ReadDir(filepath.Dir(video.Path))
	if err != nil {
		t.Fatalf("读取源目录失败: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != video.Name {
		t.Fatalf("源目录不该被写入: %v", entries)
	}
}

// 路由级：手机端 /short-media/video/{id} 真的把代理字节发出去（D-004）。
// 走 ShortFeedHTTPServer.Handler() 的真实 mux 与真实 ffmpeg 产物，
// 断的是"路径形态不变、字节换成代理、Content-Type 是 video/mp4"。
func TestPlaybackProxyRealShortFeedRouteServesProxyBytes(t *testing.T) {
	ffmpeg, _ := requireRealFFmpeg(t)
	setupVideoServiceTestDB(t)
	source := synthesizeMedia(t, ffmpeg, "route-remux.mkv",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=duration=2",
		"-c:v", "libx264", "-c:a", "aac")
	video := createRegisteredVideo(t, source, 2)

	service := newRealProxyService(t)
	videoService := newProxyBackedVideoService(service)
	feed := NewShortFeedService(videoService)
	server := NewShortFeedHTTPServer(feed, nil, ShortFeedHTTPServerConfig{})
	handler := server.Handler()
	target := fmt.Sprintf("/short-media/video/%d", video.ID)

	// 没有代理时手机端拿不到任何可播字节：路由报 404，而不是把不可播的 mkv
	// 发过去（#7）。发过去只会得到一个放不出来的黑框，还白占流量。
	before := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.RemoteAddr = "127.0.0.1:12345"
	handler.ServeHTTP(before, request)
	if before.Code != http.StatusNotFound {
		t.Fatalf("无代理时媒体路由应当报 404，实际 %d body=%s", before.Code, before.Body.String())
	}

	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("代理应当生成成功: %#v", result)
	}
	row := mustLoadProxyRow(t, video.ID)
	proxyBytes, err := os.ReadFile(service.proxyPathForRow(row))
	if err != nil {
		t.Fatalf("读取代理产物失败: %v", err)
	}

	feed.invalidateCandidates()
	after := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, target, nil)
	request.RemoteAddr = "127.0.0.1:12345"
	handler.ServeHTTP(after, request)
	if after.Code != http.StatusOK {
		t.Fatalf("媒体路由应返回 200: %d", after.Code)
	}
	if got := after.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("命中代理时应报 video/mp4: %s", got)
	}
	if !bytes.Equal(after.Body.Bytes(), proxyBytes) {
		t.Fatalf("下发的字节应当逐字节等于代理产物: got=%d want=%d", after.Body.Len(), len(proxyBytes))
	}

	// videos 行数不变：代理绝不入库为视频记录。
	var count int64
	if err := database.DB.Model(&models.Video{}).Count(&count).Error; err != nil {
		t.Fatalf("统计视频失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("代理不该新增视频记录: videos=%d", count)
	}
}

// 桌面预览路由的 Range 覆盖在 app_playback_proxy_test.go 里（那边能拿到真实的
// asset handler）；这里只覆盖手机端那条路由。

// probeDimensions 用真 ffprobe 读产物的宽高。
func probeDimensions(t *testing.T, path string) (int, int) {
	t.Helper()
	output, stderr, err := runLocalFFProbe(context.Background(), path)
	if err != nil {
		t.Fatalf("ffprobe 读不出产物: %v %s", err, stderr)
	}
	parsed, err := parseMediaProbeOutput(output)
	if err != nil {
		t.Fatalf("解析 ffprobe 输出失败: %v", err)
	}
	for _, stream := range parsed.Streams {
		if stream.StreamType != "video" || stream.Width == nil || stream.Height == nil {
			continue
		}
		return *stream.Width, *stream.Height
	}
	t.Fatalf("产物里没有视频流")
	return 0, 0
}

// 横向源：长边收到 1920，比例保持，高度落在 1080。
func TestPlaybackProxyRealTranscodeClampsLandscapeLongEdge(t *testing.T) {
	ffmpeg, _ := requireRealFFmpeg(t)
	if !ffmpegHasEncoder(t, ffmpeg, "h264_videotoolbox") {
		t.Skip("本机 ffmpeg 没有 h264_videotoolbox 编码器，跳过真实转码尺寸测试")
	}
	setupVideoServiceTestDB(t)
	source := synthesizeMedia(t, ffmpeg, "landscape.avi",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=2560x1440:rate=5",
		"-c:v", "mpeg4", "-an")
	video := createRegisteredVideo(t, source, 1)

	service := newRealProxyService(t)
	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("应当转码成功: %#v", result)
	}
	width, height := probeDimensions(t, service.proxyPathForRow(mustLoadProxyRow(t, video.ID)))
	if width != 1920 || height != 1080 {
		t.Fatalf("2560×1440 应当收到 1920×1080，实际 %d×%d", width, height)
	}
}

// 竖向源：约束的是**长边**。只限宽的老写法会产出 1920×3413，那比 1080p 高得多。
func TestPlaybackProxyRealTranscodeClampsPortraitLongEdge(t *testing.T) {
	ffmpeg, _ := requireRealFFmpeg(t)
	if !ffmpegHasEncoder(t, ffmpeg, "h264_videotoolbox") {
		t.Skip("本机 ffmpeg 没有 h264_videotoolbox 编码器，跳过真实转码尺寸测试")
	}
	setupVideoServiceTestDB(t)
	source := synthesizeMedia(t, ffmpeg, "portrait.avi",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=1080x2160:rate=5",
		"-c:v", "mpeg4", "-an")
	video := createRegisteredVideo(t, source, 1)

	service := newRealProxyService(t)
	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("应当转码成功: %#v", result)
	}
	width, height := probeDimensions(t, service.proxyPathForRow(mustLoadProxyRow(t, video.ID)))
	if height != 1920 || width != 960 {
		t.Fatalf("1080×2160 应当收到 960×1920，实际 %d×%d", width, height)
	}
	if width > 1920 || height > 1920 {
		t.Fatalf("长边不得超过 1920：%d×%d", width, height)
	}
}

// 小于上限的源永不放大。
func TestPlaybackProxyRealTranscodeNeverUpscales(t *testing.T) {
	ffmpeg, _ := requireRealFFmpeg(t)
	if !ffmpegHasEncoder(t, ffmpeg, "h264_videotoolbox") {
		t.Skip("本机 ffmpeg 没有 h264_videotoolbox 编码器，跳过真实转码尺寸测试")
	}
	setupVideoServiceTestDB(t)
	source := synthesizeMedia(t, ffmpeg, "small.avi",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=640x480:rate=5",
		"-c:v", "mpeg4", "-an")
	video := createRegisteredVideo(t, source, 1)

	service := newRealProxyService(t)
	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("应当转码成功: %#v", result)
	}
	width, height := probeDimensions(t, service.proxyPathForRow(mustLoadProxyRow(t, video.ID)))
	if width != 640 || height != 480 {
		t.Fatalf("640×480 不该被放大，实际 %d×%d", width, height)
	}
}

// 手机端媒体路由要支持 Range：手机浏览器拖进度条靠的就是 206。
func TestPlaybackProxyRealShortFeedRouteSupportsRange(t *testing.T) {
	ffmpeg, _ := requireRealFFmpeg(t)
	setupVideoServiceTestDB(t)
	source := synthesizeMedia(t, ffmpeg, "range-remux.mkv",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=duration=2",
		"-c:v", "libx264", "-c:a", "aac")
	video := createRegisteredVideo(t, source, 2)

	service := newRealProxyService(t)
	videoService := newProxyBackedVideoService(service)
	feed := NewShortFeedService(videoService)
	server := NewShortFeedHTTPServer(feed, nil, ShortFeedHTTPServerConfig{})
	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("代理应当生成成功: %#v", result)
	}
	proxyBytes, err := os.ReadFile(service.proxyPathForRow(mustLoadProxyRow(t, video.ID)))
	if err != nil {
		t.Fatalf("读取代理产物失败: %v", err)
	}
	if len(proxyBytes) < 200 {
		t.Fatalf("产物太小，无法测 Range: %d", len(proxyBytes))
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/short-media/video/%d", video.ID), nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Range", "bytes=100-199")
	server.Handler().ServeHTTP(recorder, request)

	assertProxyRangeResponse(t, recorder, proxyBytes)
}

func assertProxyRangeResponse(t *testing.T, recorder *httptest.ResponseRecorder, full []byte) {
	t.Helper()
	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求应当返回 206，实际 %d", recorder.Code)
	}
	wantRange := fmt.Sprintf("bytes 100-199/%d", len(full))
	if got := recorder.Header().Get("Content-Range"); got != wantRange {
		t.Fatalf("Content-Range 不符: got=%q want=%q", got, wantRange)
	}
	if got := recorder.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("应当声明支持 Range: %q", got)
	}
	if recorder.Body.Len() != 100 {
		t.Fatalf("应当只回 100 字节，实际 %d", recorder.Body.Len())
	}
	if !bytes.Equal(recorder.Body.Bytes(), full[100:200]) {
		t.Fatalf("回的应当正好是 [100,200) 这一段")
	}
}
