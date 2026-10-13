package services

import (
	"context"
	"errors"
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
		{"mov+h264+pcm → 代理", "/v/a.mov", previewSnapshot(isoBMFF, "h264", "pcm_s16le"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyTranscode},
		{"mp4+h264+opus → 代理", "/v/a.mp4", previewSnapshot(isoBMFF, "h264", "opus"), true, desktopPreviewProxy, "", ""},
		{"改了扩展名的 mkv → 代理", "/v/a.mp4", previewSnapshot("matroska,webm", "h264", "aac"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyRemux},
		{"快照没有视频流 → 代理", "/v/a.mov", playbackProxySnapshot{FormatName: isoBMFF}, true, desktopPreviewProxy, "", ""},
		{"mov 快照判不出 → 旧行为", "/v/a.mov", playbackProxySnapshot{}, false, desktopPreviewLegacy, "", ""},
		{"mp4 快照判不出 → 旧行为", "/v/a.mp4", playbackProxySnapshot{}, false, desktopPreviewLegacy, "", ""},
		{"mkv+h264+aac → 转封装代理", "/v/a.mkv", previewSnapshot("matroska,webm", "h264", "aac"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyRemux},
		{"mkv 不看快照", "/v/a.mkv", playbackProxySnapshot{}, false, desktopPreviewProxy, "", ""},
		{"avi+xvid(mpeg4) → 转码代理", "/v/a.avi", previewSnapshot("avi", "mpeg4", "mp3"), true, desktopPreviewProxy, "", models.PlaybackProxyStrategyTranscode},
		{"wmv → 代理", "/v/a.wmv", playbackProxySnapshot{}, false, desktopPreviewProxy, "", ""},
		{"flv → 代理", "/v/a.flv", playbackProxySnapshot{}, false, desktopPreviewProxy, "", ""},
		{"ts → 代理", "/v/a.ts", playbackProxySnapshot{}, false, desktopPreviewProxy, "", ""},
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
	return videoService, proxies
}

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

func TestGetPreviewSessionMkvNeedsProxyWithoutProbing(t *testing.T) {
	setupVideoServiceTestDB(t)
	probe := &previewProbe{payload: movH264AACProbe}
	videoService, _ := newPreviewTestVideoService(t, probe)
	video := createProxyTestVideo(t, "show.mkv", 10)

	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "proxy-preparing" || session.ExternalAction == nil {
		t.Fatalf("mkv 应当等待代理: %#v", session)
	}
	if probe.calls != 0 {
		t.Fatalf("mkv 怎样都不能直放，不该为判定探测: %d", probe.calls)
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

// 补探有上限：ffprobe 卡住时按超时放弃，退回旧行为。
func TestGetPreviewSessionProbeHonoursTimeout(t *testing.T) {
	setupVideoServiceTestDB(t)
	proxies, _ := newProxyTestService(t)
	var deadline time.Time
	videoService := &VideoService{mediaProbe: newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		deadline, _ = ctx.Deadline()
		return nil, "", context.DeadlineExceeded
	})}
	videoService.SetPlaybackProxyService(proxies)
	video := createProxyTestVideo(t, "slow.mov", 10)
	started := time.Now()
	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil || session.Mode != "external-preview" {
		t.Fatalf("探测超时应退回旧行为: %#v err=%v", session, err)
	}
	if deadline.IsZero() || deadline.Sub(started) > previewCodecProbeTimeout+time.Second {
		t.Fatalf("补探应带不超过 %s 的期限: deadline=%v", previewCodecProbeTimeout, deadline)
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
