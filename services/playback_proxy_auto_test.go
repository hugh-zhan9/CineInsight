package services

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/models"
)

// 自动代理（2026-10-13「直接播 + 自动代理」评审修订）：按 owner 记账、最后一个 owner 走了才取消、
// 最多 2 部同时在等、手动项从不被预览取消、只有自动项的一轮不通知。

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

func autoOwnerCount(service *PlaybackProxyService, videoID uint) int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return len(service.autoOwners[videoID])
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// holdProxyWorker：手动项占住 worker（卡在重媒体槽上），后面的项留在队列里观察。
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

// gateFFmpeg：path 的第一次编码开始时关 started，然后等 ctx 取消、再等 proceed 才返回——
// 这样「已取消、结果还没记下」的窗口由测试掌握。之后的编码照常走替身。
type gateFFmpeg struct {
	started, proceed chan struct{}
	once             sync.Once
}

func installGateFFmpeg(service *PlaybackProxyService, stub *proxyFFmpegStub, path string) *gateFFmpeg {
	gate := &gateFFmpeg{started: make(chan struct{}), proceed: make(chan struct{})}
	service.runFFmpeg = func(ctx context.Context, args []string) (string, error) {
		first := false
		if containsArg(args, path) {
			gate.once.Do(func() { first = true })
		}
		if !first {
			return stub.run(ctx, args)
		}
		close(gate.started)
		<-ctx.Done()
		<-gate.proceed
		return "", ctx.Err()
	}
	return gate
}

func waitClosed(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待超时: %s", what)
	}
}

func proxyVideosWithSnapshots(t *testing.T, codec string, names ...string) []models.Video {
	t.Helper()
	videos := make([]models.Video, 0, len(names))
	for _, name := range names {
		video := createProxyTestVideo(t, name, 10)
		writeProxySnapshot(t, video, codec, "aac", 0, 0)
		videos = append(videos, video)
	}
	return videos
}

// 两个宿主看同一部：只排一项；先关的那个不取消，最后一个关了才取消。
func TestPlaybackProxyAutoTwoOwnersSameVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	videos := proxyVideosWithSnapshots(t, "h264", "holder.mkv", "shared.mkv")
	holder, shared := videos[0], videos[1]
	slot := holdProxyWorker(t, service, holder.ID)

	for _, owner := range []string{"drawer", "detail"} {
		if _, err := service.EnqueueAuto(context.Background(), shared.ID, owner); err != nil {
			t.Fatal(err)
		}
	}
	if status := service.Status(); status.Total != 2 || status.Queued != 1 || autoOwnerCount(service, shared.ID) != 2 {
		t.Fatalf("同一部只该排一项、记两个 owner: %#v owners=%d", status, autoOwnerCount(service, shared.ID))
	}
	if service.CancelAuto(shared.ID, "drawer") || service.Status().Queued != 1 {
		t.Fatalf("还有 owner 在等时不该取消: %#v", service.Status())
	}
	if service.CancelAuto(shared.ID, "stranger") {
		t.Fatalf("没请求过的 owner 不该取消任何东西")
	}
	if !service.CancelAuto(shared.ID, "detail") || service.Status().Queued != 0 {
		t.Fatalf("最后一个 owner 走了应当取消: %#v", service.Status())
	}
	slot.Release()
	final := waitForProxyIdle(t, service)
	if got := proxyCodesFor(final, shared.ID); got != PlaybackProxyCodeCancelled {
		t.Fatalf("应记一次取消: %q", got)
	}
	if got := proxyCodesFor(final, holder.ID); got != PlaybackProxyCodeCreated {
		t.Fatalf("手动项照常完成: %q", got)
	}
}

// 两个宿主看不同的两部：互不影响；第三部被拒（不取消别人在等的），腾出位置后再请求就能排上。
func TestPlaybackProxyAutoDifferentVideosAndLimit(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	videos := proxyVideosWithSnapshots(t, "h264", "holder.mkv", "a.mkv", "b.mkv", "c.mkv")
	holder, a, b, c := videos[0], videos[1], videos[2], videos[3]
	slot := holdProxyWorker(t, service, holder.ID)

	if _, err := service.EnqueueAuto(context.Background(), a.ID, "drawer"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnqueueAuto(context.Background(), b.ID, "detail"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnqueueAuto(context.Background(), c.ID, "compare-left"); !errors.Is(err, ErrPlaybackProxyAutoLimit) {
		t.Fatalf("第三部应被拒: %v", err)
	}
	if status := service.Status(); status.Queued != 2 || proxyCodesFor(status, a.ID) != "" || proxyCodesFor(status, b.ID) != "" {
		t.Fatalf("被拒不该动已在等的两部: %#v", status)
	}
	if !service.CancelAuto(a.ID, "drawer") || service.Status().Queued != 1 {
		t.Fatalf("关掉 a 只取消 a: %#v", service.Status())
	}
	if _, err := service.EnqueueAuto(context.Background(), c.ID, "compare-left"); err != nil {
		t.Fatalf("腾出位置后应能排上: %v", err)
	}
	slot.Release()
	final := waitForProxyIdle(t, service)
	if proxyCodesFor(final, b.ID) != PlaybackProxyCodeCreated || proxyCodesFor(final, c.ID) != PlaybackProxyCodeCreated {
		t.Fatalf("b 与 c 应生成: %#v", final.Results)
	}
}

// 请求与取消乱序到达：先到的取消（owner 还没登记）什么都不做；另一个宿主迟到的取消不影响仍在等的宿主。
func TestPlaybackProxyAutoOutOfOrderRequestCancel(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	videos := proxyVideosWithSnapshots(t, "h264", "holder.mkv", "a.mkv")
	holder, a := videos[0], videos[1]
	slot := holdProxyWorker(t, service, holder.ID)

	if service.CancelAuto(a.ID, "detail") {
		t.Fatalf("还没请求就到的取消不该生效")
	}
	if _, err := service.EnqueueAuto(context.Background(), a.ID, "drawer"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnqueueAuto(context.Background(), a.ID, "detail"); err != nil {
		t.Fatal(err)
	}
	// 抽屉先关（取消在详情页请求之后才到）：详情页仍在等，不取消。
	if service.CancelAuto(a.ID, "drawer") {
		t.Fatalf("另一个宿主还在等时不该取消")
	}
	slot.Release()
	final := waitForProxyIdle(t, service)
	if got := proxyCodesFor(final, a.ID); got != PlaybackProxyCodeCreated {
		t.Fatalf("仍有宿主在等的一项应完成: %q", got)
	}
	if service.CancelAuto(a.ID, "detail") {
		t.Fatalf("做完之后的取消不该生效")
	}
	if _, err := os.Stat(mustLoadProxyPath(t, service, a)); err != nil {
		t.Fatalf("做好的代理应当保留: %v", err)
	}
}

func mustLoadProxyPath(t *testing.T, service *PlaybackProxyService, video models.Video) string {
	t.Helper()
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	proxy := service.resolveValidProxy(video.ID, playbackProxyFingerprintOf(info), false)
	if proxy == nil {
		t.Fatalf("video_id=%d 没有有效代理", video.ID)
	}
	return proxy.Path
}

// 快速 A→B→A：A 处理中被取消、取消结果还没记下时又回到 A——结果落下后 A 重排并做完；B 被取消。
func TestPlaybackProxyAutoQuickSwitchBackRequeues(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	videos := proxyVideosWithSnapshots(t, "mpeg4", "a.avi", "b.avi")
	a, b := videos[0], videos[1]
	gate := installGateFFmpeg(service, stub, a.Path)

	if _, err := service.EnqueueAuto(context.Background(), a.ID, "player-1"); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, gate.started, "a 开始编码")
	if !service.CancelAuto(a.ID, "player-1") {
		t.Fatalf("换片应取消 a")
	}
	if _, err := service.EnqueueAuto(context.Background(), b.ID, "player-1"); err != nil {
		t.Fatal(err)
	}
	if !service.CancelAuto(b.ID, "player-1") {
		t.Fatalf("再换片应取消排队中的 b")
	}
	// 窗口内：a 的 ffmpeg 已收到取消，但结果还没记下。
	if _, err := service.EnqueueAuto(context.Background(), a.ID, "player-1"); err != nil {
		t.Fatal(err)
	}
	close(gate.proceed)
	final := waitForProxyIdle(t, service)
	if got := proxyCodesFor(final, a.ID); got != PlaybackProxyCodeCancelled+","+PlaybackProxyCodeCreated {
		t.Fatalf("a 应先记取消、再重排做完: %q", got)
	}
	if got := proxyCodesFor(final, b.ID); got != PlaybackProxyCodeCancelled {
		t.Fatalf("b 应被取消: %q", got)
	}
	if final.Processed != final.Total || final.Cancelled {
		t.Fatalf("计数应闭合、整轮不算取消: %#v", final)
	}
}

// 处理中的自动项被最后一个 owner 取消：只停这一项（ffmpeg 收到取消），后面的手动项照跑，取消的不留失败记录。
func TestPlaybackProxyAutoCancelStopsRunningItemOnly(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	auto := proxyVideosWithSnapshots(t, "mpeg4", "auto.avi")[0]
	manual := proxyVideosWithSnapshots(t, "h264", "after.mkv")[0]
	gate := installGateFFmpeg(service, stub, auto.Path)
	close(gate.proceed)

	if _, err := service.EnqueueAuto(context.Background(), auto.ID, "player-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), manual.ID); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, gate.started, "自动项开始编码")
	if !service.CancelAuto(auto.ID, "player-1") {
		t.Fatalf("处理中的自动项应可取消")
	}
	final := waitForProxyIdle(t, service)
	if proxyCodesFor(final, auto.ID) != PlaybackProxyCodeCancelled || proxyCodesFor(final, manual.ID) != PlaybackProxyCodeCreated {
		t.Fatalf("只停自动项，手动项照常: %#v", final.Results)
	}
	if final.Cancelled || !final.Completed {
		t.Fatalf("不该把整轮标成取消: %#v", final)
	}
	if row, _ := loadProxyRow(auto.ID); row != nil {
		t.Fatalf("取消的自动项不该留下记录: %#v", row)
	}
}

// 手动入口从不被预览取消：已手动排队的不记 owner；自动项被手动接管；处理中被取消的自动项在窗口内被手动要了也会重排。
func TestPlaybackProxyAutoNeverCancelsManualRequests(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	videos := proxyVideosWithSnapshots(t, "h264", "running.mkv", "queued.mkv", "promoted.mkv")
	running, queuedManual, promoted := videos[0], videos[1], videos[2]
	gate := installGateFFmpeg(service, stub, running.Path)

	if _, err := service.EnqueueAuto(context.Background(), running.ID, "player-1"); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, gate.started, "自动项开始编码")
	if _, err := service.CreatePlaybackProxy(context.Background(), queuedManual.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnqueueAuto(context.Background(), queuedManual.ID, "player-2"); err != nil {
		t.Fatal(err)
	}
	if autoOwnerCount(service, queuedManual.ID) != 0 || service.CancelAuto(queuedManual.ID, "player-2") {
		t.Fatalf("手动排队的项不记 owner、不能被预览取消")
	}
	if _, err := service.EnqueueAuto(context.Background(), promoted.ID, "player-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), promoted.ID); err != nil {
		t.Fatal(err)
	}
	if service.CancelAuto(promoted.ID, "player-3") {
		t.Fatalf("手动接管后预览关闭不该取消它")
	}
	// 处理中的自动项被取消，取消结果落下之前手动入口又要了它：按手动重排。
	if !service.CancelAuto(running.ID, "player-1") {
		t.Fatalf("处理中的自动项应可取消")
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), running.ID); err != nil {
		t.Fatal(err)
	}
	close(gate.proceed)
	final := waitForProxyIdle(t, service)
	if got := proxyCodesFor(final, running.ID); !strings.HasSuffix(got, PlaybackProxyCodeCancelled+","+PlaybackProxyCodeCreated) {
		t.Fatalf("窗口内的手动请求应在取消后重排做完: %q", got)
	}
	for _, video := range []models.Video{queuedManual, promoted} {
		if got := proxyCodesFor(final, video.ID); !strings.HasSuffix(got, PlaybackProxyCodeCreated) {
			t.Fatalf("%s 应照常完成: %q", video.Name, got)
		}
	}
	if final.AutoOnly {
		t.Fatalf("有手动请求的一轮不是只有自动项")
	}
}

// 只有预览自动代理的一轮：不发桌面通知，状态标 auto_only；带手动请求的一轮照旧通知。
func TestPlaybackProxyAutoOnlyRoundDoesNotNotify(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	notifier := &stubDesktopNotifier{}
	service.SetDesktopNotifier(notifier)
	videos := proxyVideosWithSnapshots(t, "h264", "auto.mkv", "manual.mkv")

	if _, err := service.EnqueueAuto(context.Background(), videos[0].ID, "player-1"); err != nil {
		t.Fatal(err)
	}
	final := waitForProxyIdle(t, service)
	if !final.AutoOnly || proxyCodesFor(final, videos[0].ID) != PlaybackProxyCodeCreated {
		t.Fatalf("只有自动项的一轮应标 auto_only: %#v", final)
	}
	if got := notifier.Notifications(); len(got) != 0 {
		t.Fatalf("只有自动项的一轮不该发通知: %#v", got)
	}

	if _, err := service.CreatePlaybackProxy(context.Background(), videos[1].ID); err != nil {
		t.Fatal(err)
	}
	final = waitForProxyIdle(t, service)
	if final.AutoOnly {
		t.Fatalf("手动的一轮不该标 auto_only")
	}
	if got := notifier.Notifications(); len(got) != 1 || got[0].Title != "播放代理生成完成" {
		t.Fatalf("手动的一轮应照旧通知一次: %#v", got)
	}
}

// 自动项与手动项混在同一轮：手动请求一进来这一轮就不再是 auto_only。
func TestPlaybackProxyMixedRoundIsNotAutoOnly(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	stubPreviewFFmpeg(t, true)
	videos := proxyVideosWithSnapshots(t, "h264", "holder.mkv", "auto.mkv")
	slot := NewMediaWorkSlot()
	service.SetMediaWorkSlot(slot)
	if err := slot.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := service.EnqueueAuto(context.Background(), videos[1].ID, "player-1")
	if err != nil || !status.AutoOnly {
		t.Fatalf("自动项开的一轮应先是 auto_only: %#v err=%v", status, err)
	}
	status, err = service.CreatePlaybackProxy(context.Background(), videos[0].ID)
	if err != nil || status.AutoOnly {
		t.Fatalf("手动请求加入后不再是 auto_only: %#v err=%v", status, err)
	}
	slot.Release()
	waitForProxyIdle(t, service)
}
