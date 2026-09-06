package services

import (
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// 手机端直连上限与代理规格一致：长边 >1920 或码率 >8 Mbps 即"重"。
func TestSourceTooHeavyForMobile(t *testing.T) {
	cases := []struct {
		name     string
		video    models.Video
		fileSize int64
		snapshot int64
		heavy    bool
	}{
		{"1080p 低码率", models.Video{Width: 1920, Height: 1080, Duration: 100}, 50_000_000, 0, false},
		{"竖屏 1080p", models.Video{Width: 1080, Height: 1920, Duration: 100}, 50_000_000, 0, false},
		{"4K", models.Video{Width: 3840, Height: 2160, Duration: 100}, 50_000_000, 0, true},
		{"1080p 但按大小估出高码率", models.Video{Width: 1920, Height: 1080, Duration: 100}, 200_000_000, 0, true},
		{"快照码率优先于估算", models.Video{Width: 1920, Height: 1080, Duration: 100}, 200_000_000, 4_000_000, false},
		{"快照码率超标", models.Video{Width: 1280, Height: 720, Duration: 100}, 10, 9_000_000, true},
		{"没有分辨率也没有时长：只能放行", models.Video{}, 900_000_000, 0, false},
	}
	for _, tc := range cases {
		if got := sourceTooHeavyForMobile(tc.video, tc.fileSize, tc.snapshot); got != tc.heavy {
			t.Fatalf("%s: heavy=%v want %v", tc.name, got, tc.heavy)
		}
	}
}

// 白名单里的 4K mp4：有代理发代理，没代理照发源文件并（在开关打开时）请求后台生成一份。
func TestShortFeedResolveMediaPrefersProxyForHeavyWhitelistedSource(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	feed := NewShortFeedService(newProxyBackedVideoService(service))

	heavy := createProxyTestVideo(t, "heavy.mp4", 10)
	if err := database.DB.Model(&models.Video{}).Where("id = ?", heavy.ID).Updates(map[string]interface{}{"width": 3840, "height": 2160}).Error; err != nil {
		t.Fatalf("设置分辨率失败: %v", err)
	}
	heavy.Width, heavy.Height = 3840, 2160
	// 代理生成要读技术快照选策略；这里给一份 h264+aac 的快照，让 stub ffmpeg 走 remux。
	writeProxySnapshot(t, heavy, "h264", "aac", 0, 0)
	light := createProxyTestVideo(t, "light.mp4", 10)
	if err := database.DB.Model(&models.Video{}).Where("id = ?", light.ID).Updates(map[string]interface{}{"width": 1920, "height": 1080}).Error; err != nil {
		t.Fatalf("设置分辨率失败: %v", err)
	}

	// 开关关着：重文件没有代理时照发源文件，也不排队。
	media, err := feed.ResolveMedia(videoRef(heavy.ID))
	if err != nil || media.Path != heavy.Path {
		t.Fatalf("没有代理时应下发源文件: %+v err=%v", media, err)
	}
	if got := service.Status(); got.Total != 0 || got.Running {
		t.Fatalf("开关关闭时不该排队生成代理: %#v", got)
	}

	// 开关打开：同一条视频再被播到就排队（节流窗口内只提一次）。
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("auto_compatibility_proxy", true).Error; err != nil {
		t.Fatalf("打开自动代理开关失败: %v", err)
	}
	feed.now = func() time.Time { return time.Now().Add(shortFeedAutoProxyInterval + time.Second) }
	if _, err := feed.ResolveMedia(videoRef(heavy.ID)); err != nil {
		t.Fatalf("下发失败: %v", err)
	}
	status := waitForProxyIdle(t, service)
	if status.Total != 1 || len(stub.calls) != 1 {
		t.Fatalf("重文件应自动排队生成一份代理: %#v calls=%d", status, len(stub.calls))
	}
	// 节流：紧接着的 Range 请求不会再提一次。
	if _, err := feed.ResolveMedia(videoRef(heavy.ID)); err != nil {
		t.Fatalf("下发失败: %v", err)
	}
	if got := len(stub.calls); got != 1 {
		t.Fatalf("十分钟内不该重复排队: %d", got)
	}

	// 自动生成的代理就位后，重文件发代理字节；轻文件始终发源文件。
	proxyPath := service.proxyPathForRow(mustLoadProxyRow(t, heavy.ID))
	media, err = feed.ResolveMedia(videoRef(heavy.ID))
	if err != nil || media.Path != proxyPath || media.MIME != playbackProxyMIME {
		t.Fatalf("有代理时重文件应下发代理: %+v err=%v", media, err)
	}
	media, err = feed.ResolveMedia(videoRef(light.ID))
	if err != nil || media.Path != light.Path {
		t.Fatalf("1080p 源文件应直接下发: %+v err=%v", media, err)
	}
}

// 扫描后的自动候选：白名单里的 4K mp4 也入队，1080p mp4 仍然不做代理。
func TestPlaybackProxyAutoCandidatesIncludeHeavyWhitelistedVideos(t *testing.T) {
	setupVideoServiceTestDB(t)
	light := createProxyTestVideo(t, "light.mp4", 10)
	heavy := createProxyTestVideo(t, "heavy.mp4", 10)
	if err := database.DB.Model(&models.Video{}).Where("id = ?", heavy.ID).Updates(map[string]interface{}{"width": 3840, "height": 2160}).Error; err != nil {
		t.Fatalf("设置分辩率失败: %v", err)
	}
	candidates, err := filterPlaybackProxyCandidates([]uint{light.ID, heavy.ID})
	if err != nil {
		t.Fatalf("筛选失败: %v", err)
	}
	if len(candidates) != 1 || candidates[0] != heavy.ID {
		t.Fatalf("只有超标的那条该成为候选: %v", candidates)
	}
}
