package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 2026-10-13「直接播 + 自动代理」：桌面预览按实际编码判定直放，其余走自动代理。

func previewSnapshot(format, videoCodec, audioCodec string) playbackProxySnapshot {
	return playbackProxySnapshot{
		FormatName: format, HasVideo: true, VideoCodec: videoCodec, VideoStreamIndex: 0,
		HasAudio: audioCodec != "", AudioCodec: audioCodec, AudioStreamIndex: 1,
	}
}

const isoBMFF = "mov,mp4,m4a,3gp,3g2,mj2"

// videoDetail 给快照补上首个视频流的 codec_tag_string 与 pix_fmt。
func videoDetail(snapshot playbackProxySnapshot, tag, pixelFormat string) playbackProxySnapshot {
	snapshot.VideoCodecTag, snapshot.VideoPixelFormat = tag, pixelFormat
	return snapshot
}

func TestDecideDesktopPreviewTable(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		snapshot playbackProxySnapshot
		ready    bool
		route    desktopPreviewRoute
		mime     string
		strategy string // 走代理时 planPlaybackProxy 应选的策略；空表示不查
	}{
		{"mov+h264+aac 直放", "/v/a.mov", previewSnapshot(isoBMFF, "h264", "aac"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"大写扩展名 .MOV", "/v/a.MOV", previewSnapshot(isoBMFF, "h264", "aac"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"mov+h264 无音频", "/v/a.mov", previewSnapshot(isoBMFF, "h264", ""), true, desktopPreviewDirect, "video/quicktime", ""},
		{"mp4+hevc+eac3", "/v/a.mp4", previewSnapshot(isoBMFF, "hevc", "eac3"), true, desktopPreviewDirect, "video/mp4", ""},
		{"m4v+prores+alac", "/v/a.m4v", previewSnapshot(isoBMFF, "prores", "alac"), true, desktopPreviewDirect, "video/x-m4v", ""},
		{"mp4+h264+ac3", "/v/a.mp4", previewSnapshot(isoBMFF, "h264", "ac3"), true, desktopPreviewDirect, "video/mp4", ""},
		{"mp4+h264+mp3", "/v/a.mp4", previewSnapshot(isoBMFF, "h264", "mp3"), true, desktopPreviewDirect, "video/mp4", ""},
		{"老快照没有 format_name 不据此否决", "/v/a.mov", previewSnapshot("", "h264", "aac"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"mov+mpeg4 → 转码代理", "/v/a.mov", previewSnapshot(isoBMFF, "mpeg4", "aac"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyTranscode},
		{"mov+h264+pcm_s16le 直放", "/v/a.mov", previewSnapshot(isoBMFF, "h264", "pcm_s16le"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"mov+prores+pcm_s24le 直放", "/v/a.mov", previewSnapshot(isoBMFF, "prores", "pcm_s24le"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"mov+h264+pcm_s16be 直放", "/v/a.mov", previewSnapshot(isoBMFF, "h264", "pcm_s16be"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"mov+h264+pcm_s24be 直放", "/v/a.mov", previewSnapshot(isoBMFF, "h264", "pcm_s24be"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"mov+h264+pcm_s32le 直放", "/v/a.mov", previewSnapshot(isoBMFF, "h264", "pcm_s32le"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"mov+h264+pcm_f32le 直放", "/v/a.mov", previewSnapshot(isoBMFF, "h264", "pcm_f32le"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"mov+h264+pcm_u8 → 转码代理", "/v/a.mov", previewSnapshot(isoBMFF, "h264", "pcm_u8"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyTranscode},
		{"hevc hvc1 直放", "/v/a.mp4", videoDetail(previewSnapshot(isoBMFF, "hevc", "aac"), "hvc1", "yuv420p10le"), true, desktopPreviewDirect, "video/mp4", ""},
		{"hevc hev1 → 转封装代理", "/v/a.mp4", videoDetail(previewSnapshot(isoBMFF, "hevc", "aac"), "hev1", "yuv420p"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyRemux},
		{"h264 nv12 直放", "/v/a.mov", videoDetail(previewSnapshot(isoBMFF, "h264", "aac"), "avc1", "nv12"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"h264 yuvj420p 直放", "/v/a.mov", videoDetail(previewSnapshot(isoBMFF, "h264", "aac"), "avc1", "yuvj420p"), true, desktopPreviewDirect, "video/quicktime", ""},
		{"h264 10 位 → 转码代理", "/v/a.mov", videoDetail(previewSnapshot(isoBMFF, "h264", "aac"), "avc1", "yuv420p10le"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyTranscode},
		{"h264 4:2:2 → 转码代理", "/v/a.mp4", videoDetail(previewSnapshot(isoBMFF, "h264", "aac"), "avc1", "yuv422p"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyTranscode},
		{"mkv h264 10 位 → 转码代理", "/v/a.mkv", videoDetail(previewSnapshot("matroska,webm", "h264", "aac"), "", "yuv420p10le"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyTranscode},
		{"mp4+h264+opus → 代理", "/v/a.mp4", previewSnapshot(isoBMFF, "h264", "opus"), true, desktopPreviewProxy, "", ""},
		{"改了扩展名的 mkv → 代理", "/v/a.mp4", previewSnapshot("matroska,webm", "h264", "aac"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyRemux},
		{"mov 快照判不出 → 旧行为", "/v/a.mov", playbackProxySnapshot{}, false, desktopPreviewLegacy, "", ""},
		{"mp4 快照判不出 → 旧行为", "/v/a.mp4", playbackProxySnapshot{}, false, desktopPreviewLegacy, "", ""},
		{"mkv+h264+aac → 转封装代理", "/v/a.mkv", previewSnapshot("matroska,webm", "h264", "aac"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyRemux},
		{"mkv 快照判不出 → 旧行为", "/v/a.mkv", playbackProxySnapshot{}, false, desktopPreviewLegacy, "", ""},
		{"avi+xvid(mpeg4) → 转码代理", "/v/a.avi", previewSnapshot("avi", "mpeg4", "mp3"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyTranscode},
		{"wmv → 转码代理", "/v/a.wmv", previewSnapshot("asf", "wmv3", "wmav2"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyTranscode},
		{"flv → 转码代理", "/v/a.flv", previewSnapshot("flv", "flv1", "mp3"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyTranscode},
		{"ts h264+aac → 转封装代理", "/v/a.ts", previewSnapshot("mpegts", "h264", "aac"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyRemux},
		{"wmv 快照判不出 → 旧行为", "/v/a.wmv", playbackProxySnapshot{}, false, desktopPreviewLegacy, "", ""},
		{"webm 照旧直放", "/v/a.webm", playbackProxySnapshot{}, false, desktopPreviewDirect, "video/webm", ""},
		{"ogv 照旧直放", "/v/a.ogv", playbackProxySnapshot{}, false, desktopPreviewDirect, "video/ogg", ""},
		{"ogg 照旧直放", "/v/a.ogg", playbackProxySnapshot{}, false, desktopPreviewDirect, "video/ogg", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideDesktopPreview(tc.path, tc.snapshot, tc.ready)
			if got.Route != tc.route || got.MIME != tc.mime {
				t.Fatalf("判定不符: got=%+v want route=%d mime=%q", got, tc.route, tc.mime)
			}
			if tc.strategy != "" {
				if plan := planPlaybackProxy(tc.snapshot, tc.path, "/tmp/out.mp4"); plan.Strategy != tc.strategy {
					t.Fatalf("代理策略应为 %s，实际 %s", tc.strategy, plan.Strategy)
				}
			}
		})
	}
}

// previewProbe 是计数的 ffprobe 替身：payload 为空时探测失败。
type previewProbe struct {
	calls   int
	payload string
}

func (p *previewProbe) run(ctx context.Context, path string) ([]byte, string, error) {
	p.calls++
	if p.payload == "" {
		return nil, "moov atom not found", errors.New("exit status 1")
	}
	return []byte(p.payload), "", nil
}

const movH264AACProbe = `{"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"10.0"},` +
	`"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":640,"height":360},` +
	`{"index":1,"codec_type":"audio","codec_name":"aac"}]}`

// newPreviewTestVideoService 造一个带代理服务与探测替身的 VideoService（与 app 层的接线同形）。
func newPreviewTestVideoService(t *testing.T, probe *previewProbe) (*VideoService, *PlaybackProxyService) {
	t.Helper()
	proxies, _ := newProxyTestService(t)
	videoService := &VideoService{mediaProbe: newMediaProbeServiceWithRunner(probe.run)}
	videoService.SetPlaybackProxyService(proxies)
	stubPreviewFFmpeg(t, true)
	return videoService, proxies
}

// stubPreviewFFmpeg 固定「本机有没有 ffmpeg」，测试不依赖机器上装没装。
func stubPreviewFFmpeg(t *testing.T, available bool) {
	t.Helper()
	original := previewFFmpegAvailable
	previewFFmpegAvailable = func() bool { return available }
	t.Cleanup(func() { previewFFmpegAvailable = original })
}

const mkvH264AACProbe = `{"format":{"format_name":"matroska,webm","duration":"10.0"},` +
	`"streams":[{"index":0,"codec_type":"video","codec_name":"h264","pix_fmt":"yuv420p","width":640,"height":360},` +
	`{"index":1,"codec_type":"audio","codec_name":"aac"}]}`

// writePreviewSnapshot 写一份新鲜的技术快照，并把容器名改成 format（writeProxySnapshot 固定写 matroska）。
func writePreviewSnapshot(t *testing.T, video models.Video, format, videoCodec, audioCodec string) {
	t.Helper()
	writeProxySnapshot(t, video, videoCodec, audioCodec, 0, 0)
	if err := database.DB.Model(&models.VideoTechnicalMetadata{}).Where("video_id = ?", video.ID).
		Update("format_name", format).Error; err != nil {
		t.Fatalf("改写容器名失败: %v", err)
	}
}

func TestGetPreviewSessionDirectPlaysMovH264(t *testing.T) {
	setupVideoServiceTestDB(t)
	probe := &previewProbe{}
	videoService, _ := newPreviewTestVideoService(t, probe)
	video := createProxyTestVideo(t, "camera.mov", 10)
	writePreviewSnapshot(t, video, isoBMFF, "h264", "aac")

	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "inline" || session.Proxy != nil || session.InlineSource == nil ||
		session.InlineSource.MIME != "video/quicktime" || session.InlineSource.LocatorValue != previewMediaPath(video.ID) {
		t.Fatalf("mov+h264 应当直接内嵌源文件: %#v", session)
	}
	media, err := videoService.ResolvePreviewMedia(video.ID)
	if err != nil {
		t.Fatalf("解析预览字节失败: %v", err)
	}
	if media.Path != video.Path || media.MIME != "video/quicktime" {
		t.Fatalf("应当下发源文件并报 video/quicktime: %#v", media)
	}
	if probe.calls != 0 {
		t.Fatalf("快照新鲜时不该探测: %d", probe.calls)
	}
}

func TestGetPreviewSessionMovMpeg4NeedsProxy(t *testing.T) {
	setupVideoServiceTestDB(t)
	videoService, proxies := newPreviewTestVideoService(t, &previewProbe{})
	video := createProxyTestVideo(t, "old.mov", 10)
	writePreviewSnapshot(t, video, isoBMFF, "mpeg4", "aac")

	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "proxy-preparing" || session.InlineSource != nil || session.Proxy != nil ||
		session.ExternalAction == nil || session.ReasonCode != "proxy_required" || session.ReasonMessage == "" {
		t.Fatalf("mov+mpeg4 应当等待代理并保留系统播放器入口: %#v", session)
	}
	// 入队由内嵌播放器发起（RequestPreviewProxy）；读会话本身不排任务，版本对比、字幕工作台也读会话。
	if status := proxies.Status(); status.Total != 0 || status.Running {
		t.Fatalf("读会话不该排代理任务: %#v", status)
	}
}

func TestGetPreviewSessionMkvProbesThenNeedsProxy(t *testing.T) {
	setupVideoServiceTestDB(t)
	probe := &previewProbe{payload: mkvH264AACProbe}
	videoService, _ := newPreviewTestVideoService(t, probe)
	video := createProxyTestVideo(t, "show.mkv", 10)

	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "proxy-preparing" || session.ExternalAction == nil || session.ReasonCode != "proxy_required" {
		t.Fatalf("mkv 应当等待代理: %#v", session)
	}
	if probe.calls != 1 {
		t.Fatalf("缺快照的 mkv 应补探一次（估算代理大小要用）: %d", probe.calls)
	}
}

// 快照缺失：限时补探一次，按探出来的编码判定。
func TestGetPreviewSessionProbesMissingSnapshotOnce(t *testing.T) {
	setupVideoServiceTestDB(t)
	probe := &previewProbe{payload: movH264AACProbe}
	videoService, _ := newPreviewTestVideoService(t, probe)
	video := createProxyTestVideo(t, "fresh.mov", 10)

	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "inline" || session.InlineSource.MIME != "video/quicktime" {
		t.Fatalf("补探出 h264/aac 后应当直放: %#v", session)
	}
	if probe.calls != 1 {
		t.Fatalf("应当只补探一次: %d", probe.calls)
	}
	// 快照已落库：再打开不再探测。
	if _, err := videoService.GetPreviewSession(video.ID); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 {
		t.Fatalf("快照落库后不该再探测: %d", probe.calls)
	}
}

// 探测失败：按扩展名沿用旧行为（mp4 白名单内嵌、mov 外部预览）。
func TestGetPreviewSessionProbeFailureFallsBackByExtension(t *testing.T) {
	setupVideoServiceTestDB(t)
	probe := &previewProbe{}
	videoService, _ := newPreviewTestVideoService(t, probe)
	mov := createProxyTestVideo(t, "broken.mov", 10)
	mp4 := createProxyTestVideo(t, "broken.mp4", 10)

	session, err := videoService.GetPreviewSession(mov.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Mode != "external-preview" || session.ExternalAction == nil || session.ReasonCode != "inline_not_supported" {
		t.Fatalf("mov 探测失败应退回旧的外部预览: %#v", session)
	}
	session, err = videoService.GetPreviewSession(mp4.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Mode != "inline" || session.InlineSource.MIME != "video/mp4" {
		t.Fatalf("mp4 探测失败应退回旧的白名单内嵌: %#v", session)
	}
	if probe.calls != 2 {
		t.Fatalf("每个视频应当只探测一次: %d", probe.calls)
	}
}

// 补探有上限：ffprobe 卡住（替身一直阻塞到 ctx 结束）时期限真的把它停下，退回旧行为；
// 失败记在内存里，再打开同一份源不再探测、立即按旧行为。
func TestGetPreviewSessionHangingProbeStopsAtDeadline(t *testing.T) {
	setupVideoServiceTestDB(t)
	original := previewCodecProbeTimeout
	previewCodecProbeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { previewCodecProbeTimeout = original })
	calls := 0
	videoService, _ := newPreviewTestVideoService(t, &previewProbe{})
	videoService.mediaProbe = newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		calls++
		<-ctx.Done() // 卡住，直到期限到
		return nil, "", ctx.Err()
	})
	video := createProxyTestVideo(t, "slow.mov", 10)

	started := time.Now()
	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil || session.Mode != "external-preview" {
		t.Fatalf("探测超时应退回旧行为: %#v err=%v", session, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("期限应把卡住的探测停下: 用了 %s", elapsed)
	}
	started = time.Now()
	if session, err = videoService.GetPreviewSession(video.ID); err != nil || session.Mode != "external-preview" {
		t.Fatalf("再打开仍按旧行为: %#v err=%v", session, err)
	}
	if calls != 1 || time.Since(started) > previewCodecProbeTimeout {
		t.Fatalf("同一份源失败过就不再探测: calls=%d", calls)
	}
	// 源文件变了（新指纹）：重新探测一次。
	mustSetFileModTime(t, video.Path, time.Now().Add(time.Hour))
	if _, err := videoService.GetPreviewSession(video.ID); err != nil || calls != 2 {
		t.Fatalf("源变了应重新探测: calls=%d err=%v", calls, err)
	}
}

// 快照里没有视频流（只有音频）：生产路径是「不新鲜 → 补探 → 仍判不出 → 旧行为」，并记下不再补探。
func TestGetPreviewSessionSnapshotWithoutVideoFallsBack(t *testing.T) {
	setupVideoServiceTestDB(t)
	probe := &previewProbe{payload: `{"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"10.0"},` +
		`"streams":[{"index":0,"codec_type":"audio","codec_name":"aac"}]}`}
	videoService, _ := newPreviewTestVideoService(t, probe)
	video := createProxyTestVideo(t, "voice.mov", 10)
	for range 2 {
		session, err := videoService.GetPreviewSession(video.ID)
		if err != nil || session.Mode != "external-preview" {
			t.Fatalf("没有视频流应按 mov 的旧行为: %#v err=%v", session, err)
		}
	}
	if probe.calls != 1 {
		t.Fatalf("应只补探一次: %d", probe.calls)
	}
}

// 手机端（与 Jellyfin）不受桌面判定影响：inlinePreviewMIMEs 不动，手机端仍是自己的白名单口径。
func TestMobileWhitelistUnaffectedByDesktopDirectPlay(t *testing.T) {
	mov := models.Video{Path: "/v/a.mov"}
	eac3 := previewSnapshot(isoBMFF, "h264", "eac3")
	if decision := decideDesktopPreview(mov.Path, eac3, true); decision.Route != desktopPreviewDirect {
		t.Fatalf("桌面应当直放 mov+h264+eac3: %+v", decision)
	}
	if mimeType, ok := mobileInlineMIME(mov, &eac3); ok {
		t.Fatalf("手机端不该因桌面判定而认 eac3: %s", mimeType)
	}
	mp4 := models.Video{Path: "/v/b.mp4"}
	mpeg4 := previewSnapshot(isoBMFF, "mpeg4", "aac")
	if decision := decideDesktopPreview(mp4.Path, mpeg4, true); decision.Route != desktopPreviewProxy {
		t.Fatalf("桌面应当给 mp4+mpeg4 走代理: %+v", decision)
	}
	if mimeType, ok := mobileInlineMIME(mp4, &mpeg4); !ok || mimeType != "video/mp4" {
		t.Fatalf("手机端仍按扩展名白名单直连 mp4: %s ok=%t", mimeType, ok)
	}
}

func TestAutoProxySizeLimitAndEstimate(t *testing.T) {
	const gib = int64(1 << 30)
	for limit, want := range map[int64]int64{0: 10 * gib, 50 * gib: 10 * gib, 100 * gib: 10 * gib, 5 * gib: gib} {
		if got := autoProxySizeLimit(limit); got != want {
			t.Fatalf("上限 %d 时自动生成上限应为 %d，实际 %d", limit, want, got)
		}
	}
	remux := previewSnapshot("matroska,webm", "h264", "aac")
	if got := estimatePlaybackProxyBytes(remux, 12345, 3600); got != 12345 {
		t.Fatalf("remux 估算应取源大小: %d", got)
	}
	transcode := previewSnapshot("avi", "mpeg4", "mp3")
	if got := estimatePlaybackProxyBytes(transcode, 1, 3600); got != (8_000_000+160_000)*3600/8 {
		t.Fatalf("转码按码率上限 + 音频 × 时长估算: %d", got)
	}
	transcode.VideoBitRate = 2_000_000
	if got := estimatePlaybackProxyBytes(transcode, 1, 3600); got != (2_000_000+160_000)*3600/8 {
		t.Fatalf("源码率低于上限时按源码率估算: %d", got)
	}
	silent := previewSnapshot("avi", "mpeg4", "")
	if got := estimatePlaybackProxyBytes(silent, 1, 100); got != 8_000_000*100/8 {
		t.Fatalf("无音频不加音频码率: %d", got)
	}
	if got := estimatePlaybackProxyBytes(transcode, 777, 0); got != 777 {
		t.Fatalf("时长未知时退到源大小: %d", got)
	}
}

// 预计产物太大：会话是 proxy-manual（说明大小、只给手动生成），不排任务。
func TestGetPreviewSessionLargeProxyNeedsManualGeneration(t *testing.T) {
	setupVideoServiceTestDB(t)
	videoService, proxies := newPreviewTestVideoService(t, &previewProbe{})
	setLimit := func(bytes int64) {
		if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("proxy_cache_limit_bytes", bytes).Error; err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name     string
		limit    int64
		duration float64
		mode     string
	}{
		{"4 小时转码超过 10 GiB", 0, 14400, "proxy-manual"},
		{"2 小时转码在 10 GiB 内", 0, 7200, "proxy-preparing"},
		{"上限 5 GiB 时超过其 20%", 5 << 30, 1200, "proxy-manual"},
		{"上限 5 GiB 时在其 20% 内", 5 << 30, 600, "proxy-preparing"},
	}
	for index, tc := range cases {
		setLimit(tc.limit)
		video := createProxyTestVideo(t, fmt.Sprintf("long-%d.avi", index), tc.duration)
		writePreviewSnapshot(t, video, "avi", "mpeg4", "mp3")
		session, err := videoService.GetPreviewSession(video.ID)
		if err != nil || session.Mode != tc.mode || session.ExternalAction == nil {
			t.Fatalf("%s: 期望 %s，实际 %#v err=%v", tc.name, tc.mode, session, err)
		}
		if tc.mode == "proxy-manual" && (session.ReasonCode != "proxy_too_large" || !strings.Contains(session.ReasonMessage, "文件较大（约 ") ||
			!strings.Contains(session.ReasonMessage, "GB），生成播放代理会占用相同大小的空间")) {
			t.Fatalf("%s: 说明不对: %#v", tc.name, session)
		}
	}
	setLimit(100) // 20% = 20 字节：remux 按源大小估算，正好 20 字节仍自动，21 字节只给手动。
	for size, mode := range map[int]string{20: "proxy-preparing", 21: "proxy-manual"} {
		video := createProxyTestVideo(t, fmt.Sprintf("remux-%d.mkv", size), 10)
		if err := os.WriteFile(video.Path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		writePreviewSnapshot(t, video, "matroska,webm", "h264", "aac")
		if session, _ := videoService.GetPreviewSession(video.ID); session.Mode != mode {
			t.Fatalf("remux 源 %d 字节应为 %s: %#v", size, mode, session)
		}
	}
	if status := proxies.Status(); status.Total != 0 {
		t.Fatalf("读会话不该排任务: %#v", status)
	}
}

// 本机没有 ffmpeg：不自动生成（也不会落失败记录），按扩展名沿用旧行为。
func TestGetPreviewSessionWithoutFFmpegKeepsLegacy(t *testing.T) {
	setupVideoServiceTestDB(t)
	videoService, proxies := newPreviewTestVideoService(t, &previewProbe{})
	stubPreviewFFmpeg(t, false)
	mkv := createProxyTestVideo(t, "show.mkv", 10)
	writePreviewSnapshot(t, mkv, "matroska,webm", "h264", "aac")
	mp4 := createProxyTestVideo(t, "old.mp4", 10)
	writePreviewSnapshot(t, mp4, isoBMFF, "mpeg4", "aac")
	if session, err := videoService.GetPreviewSession(mkv.ID); err != nil || session.Mode != "external-preview" {
		t.Fatalf("没有 ffmpeg 时 mkv 应是旧的外部预览: %#v err=%v", session, err)
	}
	if session, err := videoService.GetPreviewSession(mp4.ID); err != nil || session.Mode != "inline" || session.InlineSource.MIME != "video/mp4" {
		t.Fatalf("没有 ffmpeg 时 mp4 应是旧的白名单内嵌: %#v err=%v", session, err)
	}
	if status := proxies.Status(); status.Total != 0 {
		t.Fatalf("不该排任务: %#v", status)
	}
	if row, _ := loadProxyRow(mkv.ID); row != nil {
		t.Fatalf("不该落失败记录: %#v", row)
	}
}
