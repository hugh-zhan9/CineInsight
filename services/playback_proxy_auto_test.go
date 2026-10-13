package services

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 自动代理（2026-10-13「直接播 + 自动代理」）：一次最多一项、跟着预览走、手动项不受影响。

func waitForProxyCurrent(t *testing.T, service *PlaybackProxyService, videoID uint) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if service.Status().CurrentVideoID == videoID {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 video_id=%d 开始处理超时: %#v", videoID, service.Status())
}

// proxyCodesFor 按出现顺序列出某个视频的逐项结果码。
func proxyCodesFor(status PlaybackProxyStatus, videoID uint) string {
	codes := []string{}
	for _, result := range status.Results {
		if result.VideoID == videoID {
			codes = append(codes, result.Code)
		}
	}
	return strings.Join(codes, ",")
}

func autoVideoIDOf(service *PlaybackProxyService) uint {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.autoVideoID
}

// 手动项占住 worker（卡在重媒体槽上），自动项留在队列里观察。
func holdProxyWorker(t *testing.T, service *PlaybackProxyService, manualID uint) *MediaWorkSlot {
	t.Helper()
	slot := NewMediaWorkSlot()
	service.SetMediaWorkSlot(slot)
	if err := slot.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BatchCreatePlaybackProxies(context.Background(), []uint{manualID}); err != nil {
		t.Fatalf("手动入队失败: %v", err)
	}
	waitForProxyCurrent(t, service, manualID)
	return slot
}

// 一次最多一项：同一视频不重复入队；换了片先取消上一项；取消只认当前自动项；手动项照跑。
func TestPlaybackProxyEnqueueAutoKeepsAtMostOne(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	manual := createProxyTestVideo(t, "manual.mkv", 10)
	first := createProxyTestVideo(t, "first.mkv", 10)
	second := createProxyTestVideo(t, "second.mkv", 10)
	for _, video := range []uint{manual.ID, first.ID, second.ID} {
		writeProxySnapshot(t, mustGetVideo(t, video), "h264", "aac", 0, 0)
	}
	slot := holdProxyWorker(t, service, manual.ID)

	if _, err := service.EnqueueAuto(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	status, err := service.EnqueueAuto(context.Background(), first.ID)
	if err != nil || status.Total != 2 || len(status.QueuedVideoIDs) != 1 || status.QueuedVideoIDs[0] != first.ID {
		t.Fatalf("同一视频再请求不该重复入队: %#v err=%v", status, err)
	}
	status, err = service.EnqueueAuto(context.Background(), second.ID)
	if err != nil || len(status.QueuedVideoIDs) != 1 || status.QueuedVideoIDs[0] != second.ID {
		t.Fatalf("换片后队列里只该有新的一项: %#v err=%v", status, err)
	}
	if got := proxyCodesFor(status, first.ID); got != PlaybackProxyCodeCancelled {
		t.Fatalf("上一项自动代理应记为取消: %q", got)
	}
	if service.CancelAuto(first.ID) {
		t.Fatalf("已被替换的自动项不该再被取消")
	}
	if service.CancelAuto(manual.ID) {
		t.Fatalf("手动项不能被预览取消")
	}
	if !service.CancelAuto(second.ID) || service.Status().Queued != 0 || autoVideoIDOf(service) != 0 {
		t.Fatalf("关闭预览应摘掉排队中的自动项: %#v", service.Status())
	}

	slot.Release()
	final := waitForProxyIdle(t, service)
	if got := proxyCodesFor(final, manual.ID); got != PlaybackProxyCodeCreated {
		t.Fatalf("手动项应照常完成: %q", got)
	}
	if got := proxyCodesFor(final, second.ID); got != PlaybackProxyCodeCancelled {
		t.Fatalf("被取消的自动项应有 cancelled 结果: %q", got)
	}
	if final.Cancelled || !final.Completed || final.Processed != final.Total {
		t.Fatalf("取消自动项不是取消整轮，计数应闭合: %#v", final)
	}
}

func mustGetVideo(t *testing.T, id uint) models.Video {
	t.Helper()
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

// 正在处理的自动项被预览取消：只停这一项（ffmpeg 收到取消），后面的手动项照跑，整轮不算取消。
func TestPlaybackProxyCancelAutoStopsRunningItemOnly(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	auto := createProxyTestVideo(t, "auto.mkv", 10)
	manual := createProxyTestVideo(t, "after.mkv", 10)
	writeProxySnapshot(t, auto, "mpeg4", "aac", 0, 0)
	writeProxySnapshot(t, manual, "h264", "aac", 0, 0)
	started := make(chan struct{})
	service.runFFmpeg = func(ctx context.Context, args []string) (string, error) {
		if containsArg(args, auto.Path) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
		return stub.run(ctx, args)
	}

	if _, err := service.EnqueueAuto(context.Background(), auto.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), manual.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("自动项的 ffmpeg 没有开始: %#v", service.Status())
	}
	if !service.CancelAuto(auto.ID) {
		t.Fatalf("处理中的自动项应可取消")
	}
	final := waitForProxyIdle(t, service)
	if got := proxyCodesFor(final, auto.ID); got != PlaybackProxyCodeCancelled {
		t.Fatalf("自动项应记为取消: %q", got)
	}
	if got := proxyCodesFor(final, manual.ID); got != PlaybackProxyCodeCreated {
		t.Fatalf("排在后面的手动项应照常完成: %q", got)
	}
	if final.Cancelled || !final.Completed {
		t.Fatalf("取消一项自动代理不该把整轮标成取消: %#v", final)
	}
	if row, _ := loadProxyRow(auto.ID); row != nil {
		t.Fatalf("取消的自动项不该留下失败记录: %#v", row)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// 手动入口从不被预览取消：已在手动队列里的视频请求自动代理时不标成自动项；
// 自动项被手动入口再点一次就转成手动项。
func TestPlaybackProxyManualRequestsNeverCancelledByPreview(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	holder := createProxyTestVideo(t, "holder.mkv", 10)
	queuedManual := createProxyTestVideo(t, "queued.mkv", 10)
	promoted := createProxyTestVideo(t, "promoted.mkv", 10)
	for _, video := range []models.Video{holder, queuedManual, promoted} {
		writeProxySnapshot(t, video, "h264", "aac", 0, 0)
	}
	slot := holdProxyWorker(t, service, holder.ID)
	if _, err := service.CreatePlaybackProxy(context.Background(), queuedManual.ID); err != nil {
		t.Fatal(err)
	}

	before := service.Status().Total
	status, err := service.EnqueueAuto(context.Background(), queuedManual.ID)
	if err != nil || status.Total != before || autoVideoIDOf(service) != 0 {
		t.Fatalf("已手动排队的视频不该另记一项、也不该标成自动项: %#v err=%v", status, err)
	}
	if service.CancelAuto(queuedManual.ID) {
		t.Fatalf("手动排队的项不能被预览取消")
	}

	if _, err := service.EnqueueAuto(context.Background(), promoted.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), promoted.ID); err != nil {
		t.Fatal(err)
	}
	if autoVideoIDOf(service) != 0 || service.CancelAuto(promoted.ID) {
		t.Fatalf("手动入口接管后预览关闭不该取消它")
	}

	slot.Release()
	final := waitForProxyIdle(t, service)
	for _, video := range []models.Video{holder, queuedManual} {
		if got := proxyCodesFor(final, video.ID); got != PlaybackProxyCodeCreated {
			t.Fatalf("%s 应照常完成: %q", video.Name, got)
		}
	}
	if got := proxyCodesFor(final, promoted.ID); got != PlaybackProxyCodeInProgress+","+PlaybackProxyCodeCreated {
		t.Fatalf("转成手动的项应照常完成: %q", got)
	}
}

// 自动项做完之后关预览：什么都不取消，做好的代理留着（LRU 管）。
func TestPlaybackProxyFinishedAutoProxyIsKept(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	video := createProxyTestVideo(t, "kept.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)
	if _, err := service.EnqueueAuto(context.Background(), video.ID); err != nil {
		t.Fatal(err)
	}
	final := waitForProxyIdle(t, service)
	if got := proxyCodesFor(final, video.ID); got != PlaybackProxyCodeCreated {
		t.Fatalf("自动项应生成成功: %q", got)
	}
	if service.CancelAuto(video.ID) {
		t.Fatalf("做完的自动项不该再被取消")
	}
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	proxy := service.resolveValidProxy(video.ID, playbackProxyFingerprintOf(info), false)
	if proxy == nil {
		t.Fatalf("做好的代理应当保留")
	}
	if _, err := os.Stat(proxy.Path); err != nil {
		t.Fatalf("代理产物应当还在: %v", err)
	}
}

// 自动项做完而整轮还在跑（后面还有手动项）：它不再是可取消的自动项，再次请求会重新排一项。
func TestPlaybackProxyAutoItemReleasedWhenItFinishesMidRound(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	auto := createProxyTestVideo(t, "auto-first.mkv", 10)
	manual := createProxyTestVideo(t, "manual-after.mkv", 10)
	writeProxySnapshot(t, auto, "mpeg4", "aac", 0, 0)
	writeProxySnapshot(t, manual, "h264", "aac", 0, 0)
	release := make(chan struct{})
	service.runFFmpeg = func(ctx context.Context, args []string) (string, error) {
		if containsArg(args, manual.Path) {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return stub.run(ctx, args)
	}
	if _, err := service.EnqueueAuto(context.Background(), auto.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), manual.ID); err != nil {
		t.Fatal(err)
	}
	waitForProxyCurrent(t, service, manual.ID)
	if got := proxyCodesFor(service.Status(), auto.ID); got != PlaybackProxyCodeCreated {
		t.Fatalf("自动项应已做完: %q", got)
	}
	if service.CancelAuto(auto.ID) || autoVideoIDOf(service) != 0 {
		t.Fatalf("做完的自动项不该再算自动项: auto=%d", autoVideoIDOf(service))
	}
	close(release)
	waitForProxyIdle(t, service)
}
