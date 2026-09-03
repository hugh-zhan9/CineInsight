package services

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func playEventRows(t *testing.T) []models.PlayEvent {
	t.Helper()
	var events []models.PlayEvent
	if err := database.DB.Order("id ASC").Find(&events).Error; err != nil {
		t.Fatalf("读取播放事件失败: %v", err)
	}
	return events
}

func createPlayEventFixtureVideo(t *testing.T, root, name string) models.Video {
	t.Helper()
	path := filepath.Join(root, name)
	mustCreateFile(t, path)
	video := models.Video{Name: name, Path: path, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}

func stubSuccessfulPlaybackLaunch(t *testing.T) {
	t.Helper()
	previous := openWithDefaultFn
	openWithDefaultFn = func(path string, isDir bool) error { return nil }
	t.Cleanup(func() { openWithDefaultFn = previous })
}

// 桌面正式播放写一条 desktop_play。
func TestPlayVideoWritesDesktopPlayEvent(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "formal.mp4")
	stubSuccessfulPlaybackLaunch(t)

	result, err := (&VideoService{}).PlayVideo(video.ID)
	if err != nil || result == nil || !result.DispatchSucceeded {
		t.Fatalf("正式播放应成功: result=%+v err=%v", result, err)
	}

	events := playEventRows(t)
	if len(events) != 1 {
		t.Fatalf("应写入一条播放事件，实际 %d 条", len(events))
	}
	if events[0].VideoID != video.ID || events[0].Source != models.PlayEventSourceDesktopPlay {
		t.Fatalf("事件内容错误: %+v", events[0])
	}
	// 事件时间与计数列更新的时间必须是同一次播放，不能各写各的。
	after := previewStatsSnapshot(t, video.ID)
	if after.PlayCount != 1 || after.LastPlayedAt == nil {
		t.Fatalf("计数列未同步: %+v", after)
	}
	if diff := events[0].PlayedAt.Sub(*after.LastPlayedAt); diff > time.Second || diff < -time.Second {
		t.Fatalf("事件时间与 last_played_at 偏差过大: %v", diff)
	}
}

// 随机播放写 desktop_random，而不是复用桌面播放的来源。
func TestPlayRandomVideoWritesDesktopRandomEvent(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "random.mp4")
	stubSuccessfulPlaybackLaunch(t)

	result, err := (&VideoService{}).PlayRandomVideo()
	if err != nil || result == nil || !result.DispatchSucceeded {
		t.Fatalf("随机播放应成功: result=%+v err=%v", result, err)
	}

	events := playEventRows(t)
	if len(events) != 1 || events[0].VideoID != video.ID || events[0].Source != models.PlayEventSourceDesktopRandom {
		t.Fatalf("随机播放事件错误: %+v", events)
	}
	if after := previewStatsSnapshot(t, video.ID); after.RandomPlayCount != 1 {
		t.Fatalf("随机计数未同步: %+v", after)
	}
}

// 手机端信息流播放写 mobile_feed。
func TestShortFeedRecordPlaybackWritesMobileFeedEvent(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createShortFeedVideo(t, root, "feed.mp4", 120, false)

	if _, err := NewShortFeedService(&VideoService{}).RecordPlayback(videoRef(video.ID)); err != nil {
		t.Fatalf("记录手机端播放失败: %v", err)
	}

	events := playEventRows(t)
	if len(events) != 1 || events[0].VideoID != video.ID || events[0].Source != models.PlayEventSourceMobileFeed {
		t.Fatalf("手机端播放事件错误: %+v", events)
	}
	if after := previewStatsSnapshot(t, video.ID); after.RandomPlayCount != 1 {
		t.Fatalf("手机端计数未同步: %+v", after)
	}
}

// 内嵌预览播到结尾只更新观看进度，不是一次「播放」，不写账本。
func TestInlinePreviewCompletionWritesNoPlayEvent(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createPlayEventFixtureVideo(t, root, "preview.mp4")
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("duration", 120.0).Error; err != nil {
		t.Fatalf("设置时长失败: %v", err)
	}

	svc := &VideoService{}
	if _, err := svc.UpdateVideoWatchProgress(video.ID, 60, false); err != nil {
		t.Fatalf("更新观看进度失败: %v", err)
	}
	if _, err := svc.UpdateVideoWatchProgress(video.ID, 120, true); err != nil {
		t.Fatalf("标记看完失败: %v", err)
	}

	if events := playEventRows(t); len(events) != 0 {
		t.Fatalf("内嵌预览不应写播放事件，实际 %+v", events)
	}
	after := previewStatsSnapshot(t, video.ID)
	if after.PlayCount != 0 || after.RandomPlayCount != 0 || after.LastPlayedAt != nil {
		t.Fatalf("内嵌预览不应改播放计数: %+v", after)
	}
	if !after.IsWatched {
		t.Fatalf("看完标记应保留")
	}
}

// 播放器没起来时计数与事件都不写：账本不能记下没发生过的播放。
func TestPlaybackDispatchFailureWritesNoPlayEvent(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "dispatch-fail.mp4")

	previous := openWithDefaultFn
	openWithDefaultFn = func(path string, isDir bool) error { return errors.New("播放器启动失败") }
	t.Cleanup(func() { openWithDefaultFn = previous })

	result, err := (&VideoService{}).PlayVideo(video.ID)
	if err != nil {
		t.Fatalf("领域失败应走返回值: %v", err)
	}
	if result == nil || result.DispatchSucceeded || result.ReasonCode != "dispatch_failed" {
		t.Fatalf("期望 dispatch_failed: %+v", result)
	}
	if events := playEventRows(t); len(events) != 0 {
		t.Fatalf("分发失败不应写播放事件，实际 %+v", events)
	}
	after := previewStatsSnapshot(t, video.ID)
	if after.PlayCount != 0 || after.LastPlayedAt != nil {
		t.Fatalf("分发失败不应写计数: %+v", after)
	}
}

// 统计事务失败时，计数与事件必须同时缺失——两者写在同一个事务里就是为了这个。
// 播放本身已经成功，结果仍然是成功。
//
// 制造失败的办法是把账本表拿掉，让事务里的 INSERT 真的报错；这样跑的是生产代码
// 自己的事务边界，而不是一个替身。
func TestPlaybackStatsTransactionFailureLeavesCountsAndEventsBothMissing(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "tx-fail.mp4")
	stubSuccessfulPlaybackLaunch(t)

	if err := database.DB.Migrator().DropTable(&models.PlayEvent{}); err != nil {
		t.Fatalf("删除播放事件表失败: %v", err)
	}

	result, err := (&VideoService{}).PlayVideo(video.ID)
	if err != nil {
		t.Fatalf("统计写失败不应变成 error: %v", err)
	}
	if result == nil || !result.DispatchSucceeded {
		t.Fatalf("统计写失败后播放结果仍应为成功: %+v", result)
	}

	after := previewStatsSnapshot(t, video.ID)
	if after.PlayCount != 0 || after.LastPlayedAt != nil || after.IsStale {
		t.Fatalf("事务回滚后计数列不应有任何变化: %+v", after)
	}
	if err := database.DB.Migrator().CreateTable(&models.PlayEvent{}); err != nil {
		t.Fatalf("恢复播放事件表失败: %v", err)
	}
	if events := playEventRows(t); len(events) != 0 {
		t.Fatalf("事务回滚后不应留下事件: %+v", events)
	}
}

// 同一部片子分两天各播一次，热力图两天各计一次——这正是改读账本要拿回来的东西：
// 读 last_played_at 时前一天会被后一天覆盖掉。
func TestWatchHeatmapCountsSameVideoOnEachPlayedDay(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "twice.mp4")
	now := time.Now()
	dayAt := func(offsetDays, hour int) time.Time {
		base := now.AddDate(0, 0, -offsetDays)
		return time.Date(base.Year(), base.Month(), base.Day(), hour, 15, 0, 0, time.Local)
	}
	firstDay := dayAt(3, 9)
	secondDay := dayAt(1, 21)
	for _, playedAt := range []time.Time{firstDay, secondDay} {
		if err := database.DB.Create(&models.PlayEvent{
			VideoID: video.ID, PlayedAt: playedAt, Source: models.PlayEventSourceDesktopPlay,
		}).Error; err != nil {
			t.Fatalf("写入播放事件失败: %v", err)
		}
	}

	heatmap, err := libraryWatchHeatmap(now)
	if err != nil {
		t.Fatalf("统计热力图失败: %v", err)
	}
	counts := map[string]int64{}
	for _, day := range heatmap {
		counts[day.Date] = day.Count
	}
	if counts[firstDay.Format("2006-01-02")] != 1 || counts[secondDay.Format("2006-01-02")] != 1 {
		t.Fatalf("同一视频两天各播一次应两日各计一次: %#v", counts)
	}
	if len(heatmap) != 2 {
		t.Fatalf("应只有两天有记录: %#v", heatmap)
	}
}

// 洞察页新增的两项：总条数与按来源拆分。
func TestLibraryStatsReportsPlayEventTotalsBySource(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createPlayEventFixtureVideo(t, root, "totals.mp4")
	now := time.Now()
	sources := []string{
		models.PlayEventSourceDesktopPlay,
		models.PlayEventSourceDesktopPlay,
		models.PlayEventSourceDesktopRandom,
		models.PlayEventSourceMobileFeed,
		// 一年窗口之外的历史事件仍进总数，只是不进热力图。
		models.PlayEventSourceLegacy,
	}
	for index, source := range sources {
		playedAt := now.Add(-time.Duration(index) * time.Hour)
		if source == models.PlayEventSourceLegacy {
			playedAt = now.AddDate(-3, 0, 0)
		}
		if err := database.DB.Create(&models.PlayEvent{
			VideoID: video.ID, PlayedAt: playedAt, Source: source,
		}).Error; err != nil {
			t.Fatalf("写入播放事件失败: %v", err)
		}
	}

	stats, err := NewLibraryStatsService().GetStats()
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if stats.TotalPlayEvents != 5 {
		t.Fatalf("总播放事件数应为 5，实际 %d", stats.TotalPlayEvents)
	}
	want := map[string]int64{
		models.PlayEventSourceDesktopPlay:   2,
		models.PlayEventSourceDesktopRandom: 1,
		models.PlayEventSourceMobileFeed:    1,
		models.PlayEventSourceLegacy:        1,
	}
	if len(stats.PlaysBySource) != len(want) {
		t.Fatalf("来源拆分条目数错误: %#v", stats.PlaysBySource)
	}
	for source, count := range want {
		if stats.PlaysBySource[source] != count {
			t.Fatalf("来源 %s 计数错误 got=%d want=%d (%#v)", source, stats.PlaysBySource[source], count, stats.PlaysBySource)
		}
	}
	// 空库也要给出空 map 而不是 nil，前端不必为 null 做分支。
	if err := database.DB.Where("1 = 1").Delete(&models.PlayEvent{}).Error; err != nil {
		t.Fatalf("清空播放事件失败: %v", err)
	}
	empty, err := NewLibraryStatsService().GetStats()
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if empty.TotalPlayEvents != 0 || empty.PlaysBySource == nil || len(empty.PlaysBySource) != 0 {
		t.Fatalf("空账本应给出 0 与空 map: total=%d bySource=%#v", empty.TotalPlayEvents, empty.PlaysBySource)
	}
}
