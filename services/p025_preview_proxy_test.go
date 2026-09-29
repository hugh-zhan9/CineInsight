package services

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// P-025：播放代理的队列排位、预览优先用代理与失效记录的排除。

// PLAY-04：状态按处理顺序带出排队中的视频，抽屉据此显示「排队中（第 N 个）」；正在处理的那一项
// 是 current_video_id，不占排位。
func TestPlaybackProxyStatusReportsQueuePositionsPLAY04(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	slot := NewMediaWorkSlot()
	service.SetMediaWorkSlot(slot)
	// 测试先占住重媒体槽：worker 取出第一项后卡在 Acquire 上，其余项留在队列里。
	if err := slot.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := createProxyTestVideo(t, "first.mkv", 10)
	second := createProxyTestVideo(t, "second.mkv", 10)
	third := createProxyTestVideo(t, "third.mkv", 10)
	if _, err := service.BatchCreatePlaybackProxies(context.Background(), []uint{first.ID, second.ID, third.ID}); err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var status PlaybackProxyStatus
	for time.Now().Before(deadline) {
		status = service.Status()
		if status.CurrentVideoID == first.ID {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status.CurrentVideoID != first.ID {
		t.Fatalf("第一项应当已被取出处理: %#v", status)
	}
	if !reflect.DeepEqual(status.QueuedVideoIDs, []uint{second.ID, third.ID}) || status.Queued != 2 {
		t.Fatalf("排队项应按处理顺序带出（第 1 个是 second）: ids=%v queued=%d", status.QueuedVideoIDs, status.Queued)
	}

	slot.Release()
	final := waitForProxyIdle(t, service)
	if final.QueuedVideoIDs == nil || len(final.QueuedVideoIDs) != 0 {
		t.Fatalf("跑完之后排位应为空数组（不是 null）: %#v", final.QueuedVideoIDs)
	}
}

// PLAY-04：排位只带前 playbackProxyQueuePositionLimit 项，Queued 仍是真实总数。
func TestPlaybackProxyQueuePositionsAreBoundedPLAY04(t *testing.T) {
	service := &PlaybackProxyService{}
	for index := 0; index < playbackProxyQueuePositionLimit+50; index++ {
		service.queue = append(service.queue, uint(index+1))
	}
	service.mu.Lock()
	status := service.snapshotLocked()
	service.mu.Unlock()
	if status.Queued != playbackProxyQueuePositionLimit+50 || len(status.QueuedVideoIDs) != playbackProxyQueuePositionLimit {
		t.Fatalf("排位条数应封顶: queued=%d ids=%d", status.Queued, len(status.QueuedVideoIDs))
	}
	if status.QueuedVideoIDs[0] != 1 || status.QueuedVideoIDs[playbackProxyQueuePositionLimit-1] != playbackProxyQueuePositionLimit {
		t.Fatalf("排位应保持队首顺序: %v", status.QueuedVideoIDs[:3])
	}
	// 快照是拷贝：改快照不影响队列。
	status.QueuedVideoIDs[0] = 999
	if service.queue[0] != 1 {
		t.Fatalf("快照不该与队列共用底层数组")
	}
}

// PLAY-05 的受保护面：白名单命中的 mp4 即便有代理，正式播放仍打开源文件。
func TestPlayVideoOpensSourceForWhitelistedSourceWithProxyPLAY05(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	video := createProxyTestVideo(t, "formal.mp4", 10)
	seedReadyProxy(t, service, video, 1024, time.Now())

	original := openWithDefaultFn
	t.Cleanup(func() { openWithDefaultFn = original })
	opened := make([]string, 0, 1)
	openWithDefaultFn = func(path string, isDir bool) error {
		opened = append(opened, path)
		return nil
	}
	if session, err := videoService.GetPreviewSession(video.ID); err != nil || session.Proxy == nil {
		t.Fatalf("预览应走代理: %#v err=%v", session, err)
	}
	if _, err := videoService.PlayVideo(video.ID); err != nil {
		t.Fatalf("正式播放失败: %v", err)
	}
	if len(opened) != 1 || opened[0] != video.Path {
		t.Fatalf("正式播放必须打开源文件: %v", opened)
	}
}

// 源文件不见了时代理任务把视频标为失效，并写原因 missing_file（D-PC06、LIB-10 守卫的待修项）。
func TestPlaybackProxyMissingSourceMarksStaleWithReasonLIB10(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	video := createProxyTestVideo(t, "gone.mkv", 10)
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeFileMissing {
		t.Fatalf("源文件缺失应报 file_missing: %#v", result)
	}
	var reloaded models.Video
	if err := database.DB.First(&reloaded, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsStale || reloaded.StaleReason != models.StaleReasonMissingFile {
		t.Fatalf("应标失效并写原因 missing_file: stale=%v reason=%q", reloaded.IsStale, reloaded.StaleReason)
	}
}

// 「路径失效」视图不裁剪扫描根；在这个视图里「为当前筛选生成代理」不能把失效记录塞进队列，
// 扫描后的自动候选同样只看活跃视频（P-012/13 评审 Minor 3）。
func TestPlaybackProxyEnqueueExcludesStaleVideos(t *testing.T) {
	setupVideoServiceTestDB(t)
	active := createProxyTestVideo(t, "active.mkv", 10)
	stale := createProxyTestVideo(t, "stale.mkv", 10)
	if err := markVideoStale(stale.ID, models.StaleReasonMissingFile).Error; err != nil {
		t.Fatal(err)
	}

	staleView, err := collectPlaybackProxyFilterIDs(LibraryFilter{SmartView: LibraryViewStale})
	if err != nil {
		t.Fatalf("解析失效视图失败: %v", err)
	}
	if len(staleView) != 0 {
		t.Fatalf("失效视图不该产生代理候选: %v", staleView)
	}
	all, err := collectPlaybackProxyFilterIDs(LibraryFilter{})
	if err != nil {
		t.Fatalf("解析默认视图失败: %v", err)
	}
	if !reflect.DeepEqual(all, []uint{active.ID}) {
		t.Fatalf("默认视图只应包含活跃视频: %v", all)
	}
	candidates, err := filterPlaybackProxyCandidates([]uint{active.ID, stale.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(candidates, []uint{active.ID}) {
		t.Fatalf("自动候选应排除失效视频: %v", candidates)
	}
}
