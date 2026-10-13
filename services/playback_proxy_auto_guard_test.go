package services

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

// 第三轮评审：EnqueueAuto 在服务端过与 GetPreviewSession 同一个判定（autoProxyGuard），
// 直放解码失败后的请求因此同样受 ffmpeg 与大小上限约束；手动生成不受影响。

func setProxyCacheLimit(t *testing.T, bytes int64) {
	t.Helper()
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("proxy_cache_limit_bytes", bytes).Error; err != nil {
		t.Fatal(err)
	}
}

func sizedProxyVideo(t *testing.T, name string, size int, snapshot bool) models.Video {
	t.Helper()
	video := createProxyTestVideo(t, name, 10)
	if err := os.WriteFile(video.Path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if snapshot {
		writeProxySnapshot(t, video, "h264", "aac", 0, 0) // remux：估算取源大小
	}
	return video
}

func TestEnqueueAutoRefusedWithoutFFmpeg(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	stubPreviewFFmpeg(t, false)
	video := proxyVideosWithSnapshots(t, "h264", "decode-failed.mp4")[0]

	if _, err := service.EnqueueAuto(context.Background(), video.ID, "player-1#1"); !errors.Is(err, ErrPlaybackProxyAutoUnavailable) ||
		!strings.HasPrefix(err.Error(), "auto_proxy_unavailable") {
		t.Fatalf("没有 ffmpeg 时应拒绝自动代理: %v", err)
	}
	if status := service.Status(); status.Total != 0 || status.Running {
		t.Fatalf("不该排任务: %#v", status)
	}
	if row, _ := loadProxyRow(video.ID); row != nil {
		t.Fatalf("不该落失败记录: %#v", row)
	}
	// 手动生成照旧（替身 ffmpeg 能跑）。
	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("手动生成不受影响: %#v", result)
	}
}

func TestEnqueueAutoRefusedWhenEstimateTooLarge(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	setProxyCacheLimit(t, 100) // 自动上限 = 20 字节
	large := sizedProxyVideo(t, "large-hev1.mp4", 64, true)
	unprobed := sizedProxyVideo(t, "unprobed.mp4", 21, false) // 快照判不出：按源大小估算
	small := sizedProxyVideo(t, "small.mp4", 10, false)

	for _, video := range []models.Video{large, unprobed} {
		_, err := service.EnqueueAuto(context.Background(), video.ID, "player-1#1")
		var tooLarge *AutoProxyTooLargeError
		if !errors.As(err, &tooLarge) || tooLarge.Estimate != fileSize(t, video) ||
			!strings.HasPrefix(err.Error(), "auto_proxy_too_large: 文件较大（约 ") {
			t.Fatalf("%s: 预计产物超过上限应拒绝: %v", video.Name, err)
		}
	}
	if status := service.Status(); status.Total != 0 {
		t.Fatalf("拒绝时不该排任务: %#v", status)
	}
	if _, err := service.EnqueueAuto(context.Background(), small.ID, "player-1#2"); err != nil {
		t.Fatalf("上限以内应照常入队: %v", err)
	}
	waitForProxyIdle(t, service)
	if result := manualProxyResult(t, service, large.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("手动生成不受大小上限约束: %#v", result)
	}
}

func fileSize(t *testing.T, video models.Video) int64 {
	t.Helper()
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// manualProxyResult：手动生成一项，等这一轮跑完，返回这一项的最后一条结果。
func manualProxyResult(t *testing.T, service *PlaybackProxyService, videoID uint) PlaybackProxyItemResult {
	t.Helper()
	if _, err := service.CreatePlaybackProxy(context.Background(), videoID); err != nil {
		t.Fatal(err)
	}
	status := waitForProxyIdle(t, service)
	for index := len(status.Results) - 1; index >= 0; index-- {
		if status.Results[index].VideoID == videoID {
			return status.Results[index]
		}
	}
	t.Fatalf("没有 video_id=%d 的结果: %#v", videoID, status.Results)
	return PlaybackProxyItemResult{}
}

// 已在处理的一项（别的宿主请求的）只加 owner，不再过检查：不能对一个正在跑的任务说「没有 ffmpeg」。
func TestEnqueueAutoJoinsRunningItemWithoutGuard(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	videos := proxyVideosWithSnapshots(t, "h264", "holder.mkv", "shared.mkv")
	slot := holdProxyWorker(t, service, videos[0].ID)
	if _, err := service.EnqueueAuto(context.Background(), videos[1].ID, "drawer#1"); err != nil {
		t.Fatal(err)
	}
	stubPreviewFFmpeg(t, false)
	if _, err := service.EnqueueAuto(context.Background(), videos[1].ID, "detail#1"); err != nil || autoOwnerCount(service, videos[1].ID) != 2 {
		t.Fatalf("已在排队的一项应直接加 owner: err=%v owners=%d", err, autoOwnerCount(service, videos[1].ID))
	}
	slot.Release()
	waitForProxyIdle(t, service)
}

// 评审复现（移植自 zz_review_repro_test.go）：处理中的自动项被取消、结果未落时手动入口接管，
// 随后另一个宿主又请求自动代理——不能再记自动 owner，否则它一走就把手动请求取消了。
func TestPlaybackProxyManualTakeoverInWindowIgnoresLaterAutoOwner(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	a := proxyVideosWithSnapshots(t, "mpeg4", "a.avi")[0]
	gate := installGateFFmpeg(service, stub, a.Path)

	if _, err := service.EnqueueAuto(context.Background(), a.ID, "drawer#1"); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, gate.started, "a 开始编码")
	if !service.CancelAuto(a.ID, "drawer#1") {
		t.Fatal("关闭抽屉应取消 a")
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnqueueAuto(context.Background(), a.ID, "detail#1"); err != nil {
		t.Fatal(err)
	}
	if autoOwnerCount(service, a.ID) != 0 {
		t.Fatalf("手动接管后不该再记自动 owner: %d", autoOwnerCount(service, a.ID))
	}
	close(gate.proceed)
	if service.CancelAuto(a.ID, "detail#1") {
		t.Fatal("自动 owner 离开不能取消手动请求")
	}
	final := waitForProxyIdle(t, service)
	if got := proxyCodesFor(final, a.ID); !strings.HasSuffix(got, PlaybackProxyCodeCancelled+","+PlaybackProxyCodeCreated) {
		t.Fatalf("手动请求应在取消结果落下后重排并做完: %q", got)
	}
}

// 取消窗口里的自动重新请求同样受「最多 2 部」约束。
func TestPlaybackProxyReRequestInWindowRespectsLimit(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	videos := proxyVideosWithSnapshots(t, "mpeg4", "a.avi", "b.avi", "c.avi")
	a, b, c := videos[0], videos[1], videos[2]
	gate := installGateFFmpeg(service, stub, a.Path)

	if _, err := service.EnqueueAuto(context.Background(), a.ID, "p1#1"); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, gate.started, "a 开始编码")
	if !service.CancelAuto(a.ID, "p1#1") {
		t.Fatal("应取消 a")
	}
	for _, video := range []models.Video{b, c} {
		if _, err := service.EnqueueAuto(context.Background(), video.ID, "p2#"+video.Name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.EnqueueAuto(context.Background(), a.ID, "p1#2"); !errors.Is(err, ErrPlaybackProxyAutoLimit) {
		t.Fatalf("已有 2 部在等时窗口内的重新请求也应被拒: %v", err)
	}
	close(gate.proceed)
	final := waitForProxyIdle(t, service)
	if got := proxyCodesFor(final, a.ID); got != PlaybackProxyCodeCancelled {
		t.Fatalf("被拒的重新请求不该让 a 重排: %q", got)
	}
}
