package services

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// ===== P-023 测试夹具 =====

// randomCommitClock 替换未决随机提交的定时器：记录每次登记的时长与回调，由测试决定何时「过了 30 秒」。
// 不靠真实 sleep，也不会把真定时器泄漏到后面的测试里（那会往别的测试库写统计）。
type randomCommitClock struct {
	mu        sync.Mutex
	durations []time.Duration
	callbacks []func()
}

type noopStoppableTimer struct{}

func (noopStoppableTimer) Stop() bool { return true }

func useManualRandomCommitClock(t *testing.T) *randomCommitClock {
	t.Helper()
	clock := &randomCommitClock{}
	slot := newRandomCommitSlot()
	slot.afterFunc = func(d time.Duration, f func()) stoppableTimer {
		clock.mu.Lock()
		defer clock.mu.Unlock()
		clock.durations = append(clock.durations, d)
		clock.callbacks = append(clock.callbacks, f)
		return noopStoppableTimer{}
	}
	previous := randomCommits
	randomCommits = slot
	t.Cleanup(func() { randomCommits = previous })
	return clock
}

// elapse 触发迄今登记过的全部回调，包括已被提交或丢弃的旧项：
// 迟到的回调必须是空操作，这比「只触发最新一个」更严格。
func (c *randomCommitClock) elapse() {
	c.mu.Lock()
	callbacks := append([]func(){}, c.callbacks...)
	c.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

// fire 只触发第 index 次登记的回调（从 0 开始）。
func (c *randomCommitClock) fire(index int) {
	c.mu.Lock()
	callback := c.callbacks[index]
	c.mu.Unlock()
	callback()
}

func useFreshViewEventDedup(t *testing.T, capacity int) {
	t.Helper()
	previous := viewEvents
	viewEvents = newViewEventDedup(capacity)
	t.Cleanup(func() { viewEvents = previous })
}

func p023Events(t *testing.T, videoID uint) []models.PlayEvent {
	t.Helper()
	var events []models.PlayEvent
	if err := database.DB.Where("video_id = ?", videoID).Order("id ASC").Find(&events).Error; err != nil {
		t.Fatalf("读取播放事件失败: %v", err)
	}
	return events
}

func p023OtherVideo(t *testing.T, a, b models.Video, id uint) models.Video {
	t.Helper()
	switch id {
	case a.ID:
		return b
	case b.ID:
		return a
	}
	t.Fatalf("结果不在夹具里: %d", id)
	return models.Video{}
}

var p023TokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ===== 随机延迟提交（PLAY-07 / PLAY-08，R10） =====

// 30 秒内「换一个」：被换掉的那一部计数与事件都不写；换来的那一部到期后写一次。
func TestPLAY07PLAY08RerollWithinWindowWritesNoCountOrEvent(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	first := createPlayEventFixtureVideo(t, root, "a.mp4")
	second := createPlayEventFixtureVideo(t, root, "b.mp4")
	stubSuccessfulPlaybackLaunch(t)
	clock := useManualRandomCommitClock(t)
	svc := &VideoService{}

	started, err := svc.PlayRandomVideoWithFilter(RandomPlayRequest{})
	if err != nil || started == nil || !started.DispatchSucceeded || started.Video == nil {
		t.Fatalf("随机播放应成功: %+v err=%v", started, err)
	}
	if !p023TokenPattern.MatchString(started.RerollToken) {
		t.Fatalf("reroll_token 必须是 32 位十六进制: %q", started.RerollToken)
	}
	if len(clock.durations) != 1 || clock.durations[0] != 30*time.Second {
		t.Fatalf("未决提交应在 30 秒后到期: %v", clock.durations)
	}
	if events := playEventRows(t); len(events) != 0 {
		t.Fatalf("启动成功后 30 秒内不应写事件: %+v", events)
	}
	discarded := started.Video.ID
	if snapshot := previewStatsSnapshot(t, discarded); snapshot.RandomPlayCount != 0 || snapshot.LastPlayedAt != nil {
		t.Fatalf("启动成功后 30 秒内不应写计数: %+v", snapshot)
	}

	rerolled, err := svc.RerollRandom(started.RerollToken)
	if err != nil || rerolled == nil || !rerolled.DispatchSucceeded || rerolled.Video == nil {
		t.Fatalf("换一个应成功: %+v err=%v", rerolled, err)
	}
	want := p023OtherVideo(t, first, second, discarded)
	if rerolled.Video.ID != want.ID {
		t.Fatalf("换一个不应再抽到被换掉的那一部: got=%d discarded=%d", rerolled.Video.ID, discarded)
	}
	if !p023TokenPattern.MatchString(rerolled.RerollToken) || rerolled.RerollToken == started.RerollToken {
		t.Fatalf("换来的结果应带新的令牌: old=%q new=%q", started.RerollToken, rerolled.RerollToken)
	}

	clock.elapse()
	if snapshot := previewStatsSnapshot(t, discarded); snapshot.RandomPlayCount != 0 || snapshot.LastPlayedAt != nil {
		t.Fatalf("被换掉的那一部不应计数: %+v", snapshot)
	}
	if events := p023Events(t, discarded); len(events) != 0 {
		t.Fatalf("被换掉的那一部不应写事件: %+v", events)
	}
	kept := previewStatsSnapshot(t, want.ID)
	if kept.RandomPlayCount != 1 || kept.PlayCount != 0 || kept.LastPlayedAt == nil {
		t.Fatalf("到期后应只计一次随机播放: %+v", kept)
	}
	events := p023Events(t, want.ID)
	if len(events) != 1 || events[0].Source != models.PlayEventSourceDesktopRandom {
		t.Fatalf("到期后应写一条 desktop_random: %+v", events)
	}
	if diff := events[0].PlayedAt.Sub(*kept.LastPlayedAt); diff > time.Second || diff < -time.Second {
		t.Fatalf("事件时间与 last_played_at 应是同一次启动: %v", diff)
	}

	// 已提交的令牌不能再撤回：不抽取、不写任何东西。
	expired, err := svc.RerollRandom(rerolled.RerollToken)
	if err != nil || expired == nil || expired.DispatchSucceeded || expired.ReasonCode != "reroll_expired" || expired.RerollToken != "" {
		t.Fatalf("到期后的换一个应返回 reroll_expired: %+v err=%v", expired, err)
	}
	FlushPendingRandomCommit()
	if all := playEventRows(t); len(all) != 1 {
		t.Fatalf("过期的换一个不应产生新事件: %+v", all)
	}
}

// 超时写一次；应用关闭（FlushPendingRandomCommit）写一次；迟到的定时器回调不会重复写。
func TestPLAY07DeferredRandomCommitsExactlyOnceOnTimeoutOrShutdown(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "only.mp4")
	stubSuccessfulPlaybackLaunch(t)
	clock := useManualRandomCommitClock(t)
	svc := &VideoService{}

	if result, err := svc.PlayRandomVideoWithFilter(RandomPlayRequest{}); err != nil || !result.DispatchSucceeded {
		t.Fatalf("随机播放应成功: %+v err=%v", result, err)
	}
	clock.elapse()
	clock.elapse()
	FlushPendingRandomCommit()
	if events := p023Events(t, video.ID); len(events) != 1 {
		t.Fatalf("超时应恰好写一次: %+v", events)
	}

	// 29 秒时退出应用：关闭流程立即提交，之后到期的回调是空操作。
	if result, err := svc.PlayRandomVideoWithFilter(RandomPlayRequest{}); err != nil || !result.DispatchSucceeded {
		t.Fatalf("第二次随机播放应成功: %+v err=%v", result, err)
	}
	if events := p023Events(t, video.ID); len(events) != 1 {
		t.Fatalf("第二次在提交前不应写事件: %+v", events)
	}
	FlushPendingRandomCommit()
	clock.elapse()
	events := p023Events(t, video.ID)
	if len(events) != 2 || events[1].Source != models.PlayEventSourceDesktopRandom {
		t.Fatalf("关闭时应恰好补写一次: %+v", events)
	}
	if snapshot := previewStatsSnapshot(t, video.ID); snapshot.RandomPlayCount != 2 {
		t.Fatalf("计数应与事件一致: %+v", snapshot)
	}
}

// 没有「换一个」就再次随机：上一条先提交（它就是真的播了），旧令牌随之失效。
func TestPLAY07NewRandomCommitsPreviousPendingFirst(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "again.mp4")
	stubSuccessfulPlaybackLaunch(t)
	clock := useManualRandomCommitClock(t)
	svc := &VideoService{}

	first, err := svc.PlayRandomVideoWithFilter(RandomPlayRequest{})
	if err != nil || !first.DispatchSucceeded {
		t.Fatalf("随机播放应成功: %+v err=%v", first, err)
	}
	second, err := svc.PlayRandomVideoWithFilter(RandomPlayRequest{})
	if err != nil || !second.DispatchSucceeded {
		t.Fatalf("再次随机应成功: %+v err=%v", second, err)
	}
	if events := p023Events(t, video.ID); len(events) != 1 {
		t.Fatalf("再次随机前应先提交上一条: %+v", events)
	}
	stale, err := svc.RerollRandom(first.RerollToken)
	if err != nil || stale.ReasonCode != "reroll_expired" {
		t.Fatalf("已提交的旧令牌应失效: %+v err=%v", stale, err)
	}
	// 旧令牌不能误伤当前未决项：第一条的定时器迟到触发，也不能把第二条提前提交。
	clock.fire(0)
	if events := p023Events(t, video.ID); len(events) != 1 {
		t.Fatalf("迟到的旧定时器不应提交当前未决项: %+v", events)
	}
	clock.elapse()
	if events := p023Events(t, video.ID); len(events) != 2 {
		t.Fatalf("当前未决项到期后应照常提交: %+v", events)
	}
}

// 「换一个」沿用原来的筛选与模式，并把被换掉的都排除掉；范围抽空时明确失败，不再登记任何东西。
func TestPLAY08RerollKeepsFilterModeAndExcludesDiscarded(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	favoriteA := createPlayEventFixtureVideo(t, root, "fav-a.mp4")
	favoriteB := createPlayEventFixtureVideo(t, root, "fav-b.mp4")
	createPlayEventFixtureVideo(t, root, "plain.mp4")
	if err := database.DB.Model(&models.Video{}).Where("id IN ?", []uint{favoriteA.ID, favoriteB.ID}).Update("is_favorite", true).Error; err != nil {
		t.Fatalf("设置收藏失败: %v", err)
	}
	stubSuccessfulPlaybackLaunch(t)
	clock := useManualRandomCommitClock(t)
	svc := &VideoService{}

	started, err := svc.PlayRandomVideoWithFilter(RandomPlayRequest{Mode: RandomPlayModeFavorites})
	if err != nil || !started.DispatchSucceeded {
		t.Fatalf("收藏随机应成功: %+v err=%v", started, err)
	}
	next, err := svc.RerollRandom(started.RerollToken)
	if err != nil || !next.DispatchSucceeded {
		t.Fatalf("换一个应成功: %+v err=%v", next, err)
	}
	if want := p023OtherVideo(t, favoriteA, favoriteB, started.Video.ID); next.Video.ID != want.ID {
		t.Fatalf("换一个应留在收藏范围内且排除被换掉的: got=%d want=%d", next.Video.ID, want.ID)
	}
	if next.SelectionReason != randomModeReason(RandomPlayModeFavorites) {
		t.Fatalf("换一个应沿用原模式: %q", next.SelectionReason)
	}
	empty, err := svc.RerollRandom(next.RerollToken)
	if err != nil || empty.DispatchSucceeded || empty.ReasonCode != "no_filtered_videos" || empty.RerollToken != "" {
		t.Fatalf("两部收藏都被换掉后应明确空集: %+v err=%v", empty, err)
	}
	clock.elapse()
	FlushPendingRandomCommit()
	if events := playEventRows(t); len(events) != 0 {
		t.Fatalf("被换掉的都不应计入账本: %+v", events)
	}
}

// 播放器没起来：不登记未决项，也就没有令牌；之后到期或关闭都不写任何东西。
func TestPLAY08RandomDispatchFailureRegistersNothing(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "broken.mp4")
	previous := openWithDefaultFn
	openWithDefaultFn = func(path string, isDir bool) error { return errors.New("播放器启动失败") }
	t.Cleanup(func() { openWithDefaultFn = previous })
	clock := useManualRandomCommitClock(t)

	result, err := (&VideoService{}).PlayRandomVideoWithFilter(RandomPlayRequest{})
	if err != nil || result.DispatchSucceeded || result.ReasonCode != "dispatch_failed" || result.RerollToken != "" {
		t.Fatalf("启动失败应返回 dispatch_failed 且没有令牌: %+v err=%v", result, err)
	}
	if len(clock.durations) != 0 {
		t.Fatalf("启动失败不应登记未决提交: %v", clock.durations)
	}
	FlushPendingRandomCommit()
	if events := playEventRows(t); len(events) != 0 {
		t.Fatalf("启动失败不应写事件: %+v", events)
	}
	if snapshot := previewStatsSnapshot(t, video.ID); snapshot.RandomPlayCount != 0 || snapshot.LastPlayedAt != nil {
		t.Fatalf("启动失败不应写计数: %+v", snapshot)
	}
}

// 全库随机之外的唯一随机入口 PlayRandomVideoWithFilter 延迟记账，结果带令牌。
func TestPLAY07FilterEntryAlsoDefersStats(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "legacy-entry.mp4")
	stubSuccessfulPlaybackLaunch(t)
	useManualRandomCommitClock(t)

	result, err := (&VideoService{}).PlayRandomVideoWithFilter(RandomPlayRequest{})
	if err != nil || result == nil || !result.DispatchSucceeded || result.Video == nil || result.Video.ID != video.ID || len(result.RerollToken) != 32 {
		t.Fatalf("随机播放应成功并带 32 位令牌: %+v err=%v", result, err)
	}
	if events := playEventRows(t); len(events) != 0 {
		t.Fatalf("启动时不应写事件: %+v", events)
	}
	FlushPendingRandomCommit()
	if snapshot := previewStatsSnapshot(t, video.ID); snapshot.RandomPlayCount != 1 {
		t.Fatalf("提交后应计数: %+v", snapshot)
	}
}

// 窗口内同一部片又被正式播放过：延迟提交只加随机计数，不把更晚的 last_played_at 拉回启动时刻。
func TestPLAY07DeferredCommitDoesNotMoveLastPlayedAtBackwards(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "both.mp4")
	stubSuccessfulPlaybackLaunch(t)
	clock := useManualRandomCommitClock(t)
	svc := &VideoService{}

	if result, err := svc.PlayRandomVideoWithFilter(RandomPlayRequest{}); err != nil || !result.DispatchSucceeded {
		t.Fatalf("随机播放应成功: %+v err=%v", result, err)
	}
	later := time.Now().Add(10 * time.Second)
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("last_played_at", later).Error; err != nil {
		t.Fatalf("模拟窗口内的正式播放失败: %v", err)
	}
	clock.elapse()
	snapshot := previewStatsSnapshot(t, video.ID)
	if snapshot.RandomPlayCount != 1 || snapshot.LastPlayedAt == nil {
		t.Fatalf("延迟提交应计数: %+v", snapshot)
	}
	if diff := snapshot.LastPlayedAt.Sub(later); diff > time.Millisecond || diff < -time.Millisecond {
		t.Fatalf("last_played_at 不应被较早的随机启动覆盖: got=%v want=%v", snapshot.LastPlayedAt, later)
	}
	events := p023Events(t, video.ID)
	if len(events) != 1 || !events[0].PlayedAt.Before(later) {
		t.Fatalf("事件时间应是随机启动的时刻: %+v", events)
	}
}

// 延迟提交与正式播放同一个保护：事务失败时计数与事件同时缺失。
func TestPLAY07DeferredCommitTransactionFailureLeavesCountsAndEventsBothMissing(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "tx-fail-random.mp4")
	stubSuccessfulPlaybackLaunch(t)
	clock := useManualRandomCommitClock(t)

	if result, err := (&VideoService{}).PlayRandomVideoWithFilter(RandomPlayRequest{}); err != nil || !result.DispatchSucceeded {
		t.Fatalf("随机播放应成功: %+v err=%v", result, err)
	}
	if err := database.DB.Migrator().DropTable(&models.PlayEvent{}); err != nil {
		t.Fatalf("删除播放事件表失败: %v", err)
	}
	clock.elapse()
	if snapshot := previewStatsSnapshot(t, video.ID); snapshot.RandomPlayCount != 0 || snapshot.LastPlayedAt != nil {
		t.Fatalf("事务回滚后计数列不应变化: %+v", snapshot)
	}
	if err := database.DB.Migrator().CreateTable(&models.PlayEvent{}); err != nil {
		t.Fatalf("恢复播放事件表失败: %v", err)
	}
	if events := playEventRows(t); len(events) != 0 {
		t.Fatalf("事务回滚后不应留下事件: %+v", events)
	}
}

// 到期、换一个、关闭三条路径并发：每条未决项恰好被提交或丢弃一次（配合 -race 运行）。
func TestPLAY08RandomCommitSlotResolvesEachPendingExactlyOnce(t *testing.T) {
	slot := newRandomCommitSlot()
	var commits atomic.Int64
	slot.commit = func(uint, time.Time) error {
		commits.Add(1)
		return nil
	}
	var callbacksMu sync.Mutex
	var callbacks []func()
	slot.afterFunc = func(_ time.Duration, f func()) stoppableTimer {
		callbacksMu.Lock()
		defer callbacksMu.Unlock()
		callbacks = append(callbacks, f)
		return noopStoppableTimer{}
	}

	discards := int64(0)
	const rounds = 200
	for round := 0; round < rounds; round++ {
		token := fmt.Sprintf("%032x", round)
		slot.register(&pendingRandomCommit{token: token, videoID: 1, at: time.Now()})
		callbacksMu.Lock()
		expire := callbacks[len(callbacks)-1]
		callbacksMu.Unlock()

		var discarded atomic.Int64
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			if _, ok := slot.discard(token); ok {
				discarded.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			expire()
		}()
		go func() {
			defer wg.Done()
			slot.flush()
		}()
		wg.Wait()
		discards += discarded.Load()
		if resolved := commits.Load() + discards; resolved != int64(round+1) {
			t.Fatalf("第 %d 轮：每条未决项必须恰好被提交或丢弃一次，commits=%d discards=%d", round, commits.Load(), discards)
		}
	}
}

// 文案：该模式是硬过滤，「优先」改为「仅」（§8.5）。
func TestPLAY08UnwatchedModeReasonSaysOnlyUnwatched(t *testing.T) {
	reason := randomModeReason(RandomPlayModeUnwatched)
	if !strings.Contains(reason, "仅") || strings.Contains(reason, "优先") {
		t.Fatalf("未看模式的文案应为「仅未看」: %q", reason)
	}
}

// ===== 有效观看事件（PLAY-07，R10） =====

func TestPLAY07ViewThresholdIsMinOfSixtySecondsAndHalfDuration(t *testing.T) {
	cases := []struct {
		duration float64
		want     float64
	}{
		{0, 60},
		{-5, 60},
		{math.NaN(), 60},
		{math.Inf(1), 60},
		{30, 15},
		{119, 59.5},
		{120, 60},
		{7200, 60},
	}
	for _, c := range cases {
		if got := viewThreshold(c.duration); got != c.want {
			t.Fatalf("viewThreshold(%v)=%v want %v", c.duration, got, c.want)
		}
	}
}

// 同一会话只记一次 inline_view；只写账本，不动计数列。
func TestPLAY07RecordViewEventOncePerSession(t *testing.T) {
	setupVideoServiceTestDB(t)
	useFreshViewEventDedup(t, viewEventDedupCapacity)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "inline.mp4")
	svc := &VideoService{}

	for attempt, want := range []bool{true, false, false} {
		recorded, err := svc.RecordViewEvent(video.ID, PlayEventSourceInlineView, " session-1 ")
		if err != nil || recorded != want {
			t.Fatalf("第 %d 次记录 recorded=%v err=%v want=%v", attempt, recorded, err, want)
		}
	}
	if recorded, err := svc.RecordViewEvent(video.ID, PlayEventSourceInlineView, "session-2"); err != nil || !recorded {
		t.Fatalf("新会话应再记一次: recorded=%v err=%v", recorded, err)
	}
	events := p023Events(t, video.ID)
	if len(events) != 2 || events[0].Source != PlayEventSourceInlineView || events[1].Source != PlayEventSourceInlineView {
		t.Fatalf("两个会话应各写一条 inline_view: %+v", events)
	}
	snapshot := previewStatsSnapshot(t, video.ID)
	if snapshot.PlayCount != 0 || snapshot.RandomPlayCount != 0 || snapshot.LastPlayedAt != nil {
		t.Fatalf("有效观看只写账本，不应改计数列: %+v", snapshot)
	}
}

func TestPLAY07RecordViewEventRejectsInvalidInput(t *testing.T) {
	setupVideoServiceTestDB(t)
	useFreshViewEventDedup(t, viewEventDedupCapacity)
	root := t.TempDir()
	video := createPlayEventFixtureVideo(t, root, "valid.mp4")
	trashed := createPlayEventFixtureVideo(t, root, "trashed.mp4")
	if err := database.DB.Delete(&models.Video{}, trashed.ID).Error; err != nil {
		t.Fatalf("软删除失败: %v", err)
	}
	svc := &VideoService{}

	cases := []struct {
		name      string
		videoID   uint
		source    string
		sessionID string
	}{
		{"手机端来源不走这个入口", video.ID, models.PlayEventSourceMobileFeed, "s"},
		{"桌面来源不走这个入口", video.ID, models.PlayEventSourceDesktopPlay, "s"},
		{"空会话", video.ID, PlayEventSourceInlineView, "  "},
		{"超长会话", video.ID, PlayEventSourceInlineView, strings.Repeat("a", viewEventSessionIDMaxLength+1)},
		{"视频 ID 为 0", 0, PlayEventSourceInlineView, "s"},
		{"视频不存在", video.ID + 1000, PlayEventSourceInlineView, "s"},
		{"视频已进回收站", trashed.ID, PlayEventSourceInlineView, "s"},
	}
	for _, c := range cases {
		if recorded, err := svc.RecordViewEvent(c.videoID, c.source, c.sessionID); err == nil || recorded {
			t.Fatalf("%s：应报错 recorded=%v err=%v", c.name, recorded, err)
		}
	}
	if events := playEventRows(t); len(events) != 0 {
		t.Fatalf("非法输入不应写事件: %+v", events)
	}
	// 被拒绝的调用不占去重表：同一会话标识换成合法视频后照常记录。
	if recorded, err := svc.RecordViewEvent(video.ID, PlayEventSourceInlineView, "s"); err != nil || !recorded {
		t.Fatalf("合法调用应记录: recorded=%v err=%v", recorded, err)
	}
}

// 写库失败不记入去重表，同一会话可以重试。
func TestPLAY07RecordViewEventFailureDoesNotConsumeSession(t *testing.T) {
	setupVideoServiceTestDB(t)
	useFreshViewEventDedup(t, viewEventDedupCapacity)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "retry.mp4")
	svc := &VideoService{}

	if err := database.DB.Migrator().DropTable(&models.PlayEvent{}); err != nil {
		t.Fatalf("删除播放事件表失败: %v", err)
	}
	if recorded, err := svc.RecordViewEvent(video.ID, PlayEventSourceInlineView, "retry"); err == nil || recorded {
		t.Fatalf("写库失败应报错: recorded=%v err=%v", recorded, err)
	}
	if err := database.DB.Migrator().CreateTable(&models.PlayEvent{}); err != nil {
		t.Fatalf("恢复播放事件表失败: %v", err)
	}
	if recorded, err := svc.RecordViewEvent(video.ID, PlayEventSourceInlineView, "retry"); err != nil || !recorded {
		t.Fatalf("重试应记录: recorded=%v err=%v", recorded, err)
	}
}

// 去重表是 LRU：满了淘汰最久没出现的会话，最近出现过的保留。
func TestPLAY07ViewEventDedupEvictsLeastRecentlySeenSession(t *testing.T) {
	setupVideoServiceTestDB(t)
	useFreshViewEventDedup(t, 2)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "lru.mp4")
	svc := &VideoService{}
	record := func(session string) bool {
		t.Helper()
		recorded, err := svc.RecordViewEvent(video.ID, PlayEventSourceInlineView, session)
		if err != nil {
			t.Fatalf("记录 %s 失败: %v", session, err)
		}
		return recorded
	}

	record("s1")
	record("s2")
	if record("s1") {
		t.Fatalf("s1 已记过")
	}
	record("s3") // 容量 2：淘汰最久没出现的 s2
	if record("s1") {
		t.Fatalf("最近出现过的 s1 应保留")
	}
	if !record("s2") {
		t.Fatalf("被淘汰的 s2 应可再次记录")
	}
	if got := len(p023Events(t, video.ID)); got != 4 {
		t.Fatalf("应写入 s1 s2 s3 s2 共 4 条，实际 %d", got)
	}
}

// 同一会话的并发上报只写一条（配合 -race 运行）。
func TestPLAY07RecordViewEventConcurrentSameSessionWritesOnce(t *testing.T) {
	setupVideoServiceTestDB(t)
	useFreshViewEventDedup(t, viewEventDedupCapacity)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "concurrent.mp4")
	svc := &VideoService{}

	var recordedCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorded, err := svc.RecordViewEvent(video.ID, PlayEventSourceInlineView, "same")
			if err != nil {
				t.Errorf("并发记录失败: %v", err)
			}
			if recorded {
				recordedCount.Add(1)
			}
		}()
	}
	wg.Wait()
	if recordedCount.Load() != 1 || len(p023Events(t, video.ID)) != 1 {
		t.Fatalf("同一会话并发上报应只写一条: recorded=%d events=%d", recordedCount.Load(), len(p023Events(t, video.ID)))
	}
}

// ===== 洞察口径（PLAY-07） =====

// 覆盖率不再单凭随机次数算「看过」；账本里的随机启动与有效观看仍算。
func TestPLAY07ViewedCoverageIgnoresRandomCountAlone(t *testing.T) {
	setupVideoServiceTestDB(t)
	randomOnly := models.Video{Name: "random-only", Path: "/random-only", RandomPlayCount: 3}
	randomEvent := models.Video{Name: "random-event", Path: "/random-event", RandomPlayCount: 1}
	inlineView := models.Video{Name: "inline-view", Path: "/inline-view"}
	unseen := models.Video{Name: "unseen", Path: "/unseen"}
	for _, video := range []*models.Video{&randomOnly, &randomEvent, &inlineView, &unseen} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for _, event := range []models.PlayEvent{
		{VideoID: randomEvent.ID, PlayedAt: now, Source: models.PlayEventSourceDesktopRandom},
		{VideoID: inlineView.ID, PlayedAt: now, Source: PlayEventSourceInlineView},
	} {
		if err := database.DB.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}

	stats, err := NewLibraryStatsService().GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summary.VideoCount != 4 || stats.Summary.ViewedCount != 2 || stats.Summary.ViewedPercent != 50 {
		t.Fatalf("只有随机次数的视频不应算看过: %+v", stats.Summary)
	}
}

// 热力图每天按来源拆分，各来源之和等于当天总数。
func TestPLAY07WatchHeatmapSplitsEachDayBySource(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createPlayEventFixtureVideo(t, t.TempDir(), "sources.mp4")
	now := time.Now()
	day := func(offsetDays, hour int) time.Time {
		base := now.AddDate(0, 0, -offsetDays)
		return time.Date(base.Year(), base.Month(), base.Day(), hour, 0, 0, 0, time.Local)
	}
	fixtures := []struct {
		at     time.Time
		source string
	}{
		{day(1, 9), models.PlayEventSourceDesktopPlay},
		{day(1, 10), models.PlayEventSourceDesktopRandom},
		{day(1, 11), PlayEventSourceInlineView},
		{day(1, 12), PlayEventSourceInlineView},
		{day(3, 20), models.PlayEventSourceMobileFeed},
	}
	for _, fixture := range fixtures {
		if err := database.DB.Create(&models.PlayEvent{VideoID: video.ID, PlayedAt: fixture.at, Source: fixture.source}).Error; err != nil {
			t.Fatal(err)
		}
	}

	heatmap, err := libraryWatchHeatmap(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(heatmap) != 2 {
		t.Fatalf("应有两天: %+v", heatmap)
	}
	older, newer := heatmap[0], heatmap[1]
	if older.Date != day(3, 0).Format("2006-01-02") || older.Count != 1 || older.BySource[models.PlayEventSourceMobileFeed] != 1 || len(older.BySource) != 1 {
		t.Fatalf("较早那天的拆分错误: %+v", older)
	}
	want := map[string]int64{
		models.PlayEventSourceDesktopPlay:   1,
		models.PlayEventSourceDesktopRandom: 1,
		PlayEventSourceInlineView:           2,
	}
	if newer.Count != 4 || len(newer.BySource) != len(want) {
		t.Fatalf("较近那天的拆分错误: %+v", newer)
	}
	for source, count := range want {
		if newer.BySource[source] != count {
			t.Fatalf("来源 %s 计数错误: %+v", source, newer.BySource)
		}
	}
}
