package services

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestIdleGate 造一个不碰 ioreg/pmset、也不碰数据库的门：探测与设置都注入。
// 轮询间隔压到毫秒级，等待用例才不会真的睡 30 秒。
func newTestIdleGate(settings IdleSchedulerSettings, probe func(context.Context) (IdleSample, error)) *IdleGate {
	gate := NewIdleGate()
	gate.SetProbe(probe)
	gate.loadSettings = func() (IdleSchedulerSettings, error) { return settings, nil }
	gate.SetProbeInterval(5 * time.Millisecond)
	return gate
}

func staticIdleProbe(idle time.Duration, onAC bool) func(context.Context) (IdleSample, error) {
	return func(context.Context) (IdleSample, error) {
		return IdleSample{Idle: idle, OnACPower: onAC}, nil
	}
}

// busyGate 是"用户正在用电脑"的门：任何自动任务都会被挡住。
func busyGate() *IdleGate {
	return newTestIdleGate(
		IdleSchedulerSettings{Enabled: true, ThresholdMinutes: 5},
		staticIdleProbe(time.Second, true),
	)
}

// 用户活跃时自动任务在门口等；机器空闲下来之后放行。
func TestIdleGateWaitsWhileUserActiveThenReleases(t *testing.T) {
	var mu sync.Mutex
	idle := 10 * time.Second
	gate := newTestIdleGate(
		IdleSchedulerSettings{Enabled: true, ThresholdMinutes: 5},
		func(context.Context) (IdleSample, error) {
			mu.Lock()
			defer mu.Unlock()
			return IdleSample{Idle: idle, OnACPower: true}, nil
		},
	)

	started := make(chan struct{})
	go func() {
		_ = gate.Run(context.Background(), string(BackgroundTaskPerceptualHash), func(context.Context) error {
			close(started)
			return nil
		})
	}()

	select {
	case <-started:
		t.Fatal("用户活跃时不该放行自动任务")
	case <-time.After(50 * time.Millisecond):
	}

	status := gate.GetIdleSchedulerStatus()
	if len(status.Waiting) != 1 || status.Waiting[0].TaskKey != "phash" || status.Waiting[0].Reason != IdleWaitReasonUserActive {
		t.Fatalf("等待清单错误: %+v", status.Waiting)
	}

	mu.Lock()
	idle = 30 * time.Minute
	mu.Unlock()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("空闲之后应放行")
	}
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 0 {
		t.Fatalf("放行后不该还留在等待清单: %+v", waiting)
	}
}

// 「忽略空闲立即运行」：bypass 一设上，等待中的任务立刻放行。
func TestIdleGateBypassReleasesWaitingTask(t *testing.T) {
	gate := busyGate()
	started := make(chan struct{})
	go func() {
		_ = gate.Run(context.Background(), string(BackgroundTaskTechnical), func(context.Context) error {
			close(started)
			return nil
		})
	}()
	waitForGateWaiting(t, gate, "technical")

	if err := gate.RunGatedTaskNow("technical"); err != nil {
		t.Fatalf("设置 bypass 失败: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("bypass 之后应立即放行")
	}
}

// 没有任务在等这把门时点"立即运行"直接报错：留下一个没人消费的标记，
// 下一轮自动任务就会在用户不知情的情况下绕过空闲门。
func TestIdleGateRunGatedTaskNowRejectsWhenNothingIsWaiting(t *testing.T) {
	gate := busyGate()
	err := gate.RunGatedTaskNow("technical")
	if !errors.Is(err, ErrIdleGateTaskNotWaiting) {
		t.Fatalf("没有等待者时应报 ErrIdleGateTaskNotWaiting，实际 %v", err)
	}
	if bypass := gate.GetIdleSchedulerStatus().BypassTasks; len(bypass) != 0 {
		t.Fatalf("报错的请求不该留下 bypass: %v", bypass)
	}
	// 下一轮自动任务照常被挡住。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := gate.Run(ctx, "technical", func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("下一轮仍应被挡住，实际 %v", err)
	}
}

// bypass 是"这一轮"的豁免：任务跑完（登记表 running→absent）之后标记就该清掉。
func TestIdleGateBypassIsClearedAfterTheRunFinishes(t *testing.T) {
	gate := busyGate()
	started := make(chan struct{})
	go func() {
		_ = gate.Run(context.Background(), string(BackgroundTaskTechnical), func(context.Context) error {
			close(started)
			// 放行之后任务真的进了登记表：清除交给 SyncRunningTasks。
			gate.SyncRunningTasks([]string{"technical"})
			return nil
		})
	}()
	waitForGateWaiting(t, gate, "technical")
	if err := gate.RunGatedTaskNow("technical"); err != nil {
		t.Fatalf("设置 bypass 失败: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("bypass 之后应立即放行")
	}
	waitForBypassCleared(t, gate, "technical", func() {
		gate.SyncRunningTasks(nil)
	})

	// 下一轮自动触发重新被挡住。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := gate.Run(ctx, "technical", func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("下一轮应重新被挡住，实际 %v", err)
	}
}

// 放行之后任务压根没进登记表（无活可干、或服务已经在跑而提前返回）：
// Run 一返回就得把 bypass 清掉，否则它会一直挂在门里，下一轮自动任务静默直通。
func TestIdleGateBypassIsClearedWhenTaskNeverStarts(t *testing.T) {
	gate := busyGate()
	released := make(chan struct{})
	go func() {
		_ = gate.Run(context.Background(), string(BackgroundTaskAITagging), func(context.Context) error {
			// 典型场景：AI 打标唤醒时没有待打标的视频，登记表里从没出现过这个 key。
			close(released)
			return nil
		})
	}()
	waitForGateWaiting(t, gate, "ai_tagging")
	if err := gate.RunGatedTaskNow("ai_tagging"); err != nil {
		t.Fatalf("设置 bypass 失败: %v", err)
	}
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("bypass 之后应立即放行")
	}
	waitForBypassCleared(t, gate, "ai_tagging", nil)
}

// 兜底：任何路径漏清的 bypass 都不能永久留着。
//
// 直接摆一个已消费的标记进去，而不是走 RunGatedTaskNow：真实路径上只要还有
// 等待者，标记立刻就被消费并按正常路径清掉，根本走不到兜底这一支。
func TestIdleGateBypassExpiresAfterMaxAge(t *testing.T) {
	gate := busyGate()
	now := time.Now()
	gate.now = func() time.Time { return now }
	gate.mu.Lock()
	gate.bypass["technical"] = &idleBypass{grantedAt: now, consumed: true, entered: true}
	gate.mu.Unlock()

	if bypass := gate.GetIdleSchedulerStatus().BypassTasks; len(bypass) != 1 {
		t.Fatalf("应记下一个 bypass: %v", bypass)
	}
	now = now.Add(idleBypassMaxAge + time.Minute)
	if bypass := gate.GetIdleSchedulerStatus().BypassTasks; len(bypass) != 0 {
		t.Fatalf("超过兜底寿命的 bypass 应被清掉: %v", bypass)
	}
}

// 同一个 key 只留一个等待者：扫描与实时监听一天触发很多次自动唤醒，
// 各堵一个 goroutine 在门后，放行时会一起惊醒。
func TestIdleGateMergesConcurrentRunWaitersForSameKey(t *testing.T) {
	gate := busyGate()
	stop := runGateInBackground(t, gate, "technical")
	defer stop()
	waitForGateWaiting(t, gate, "technical")

	for round := 0; round < 3; round++ {
		err := gate.Run(context.Background(), "technical", func(context.Context) error {
			t.Error("被合并的唤醒不该执行任务")
			return nil
		})
		if !errors.Is(err, ErrIdleGateTaskAlreadyWaiting) {
			t.Fatalf("后来的唤醒应被合并，实际 %v", err)
		}
	}
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 1 {
		t.Fatalf("等待清单里应只有一条: %+v", waiting)
	}
}

// 项间检查点不受合并影响：worker 必须真的停下来（它和自动唤醒是两回事）。
func TestIdleGatePauseHookStillWaitsWhileRunWaiterExists(t *testing.T) {
	gate := busyGate()
	stop := runGateInBackground(t, gate, "phash")
	defer stop()
	waitForGateWaiting(t, gate, "phash")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	err := gate.PauseHook("phash").Wait(ctx, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("钩子应照常阻塞，实际 %v", err)
	}
}

// 摘钩子（用户显式启动了这个任务）必须把已经阻塞在里面的 worker 立刻放出来。
func TestIdleGateReleaseUnblocksPauseHook(t *testing.T) {
	gate := busyGate()
	hook := gate.PauseHook("phash")
	done := make(chan error, 1)
	go func() { done <- hook.Wait(context.Background(), nil) }()
	waitForGateWaiting(t, gate, "phash")

	hook.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("摘钩子后应放行且不报错，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("摘钩子后阻塞中的 worker 应立即继续")
	}
}

// 摘钩子恰好落在"worker 已进 wait、还没登记"的窗口里（evaluate 正卡在探测上）：
// Release 遍历不到它，必须靠 release 世代把它放行，不能让它停在一个已摘的钩子上。
func TestIdleGateReleaseDuringEvaluateWindowStillReleasesHook(t *testing.T) {
	probeEntered := make(chan struct{})
	probeRelease := make(chan struct{})
	var once sync.Once
	gate := newTestIdleGate(
		IdleSchedulerSettings{Enabled: true, ThresholdMinutes: 5},
		func(context.Context) (IdleSample, error) {
			once.Do(func() { close(probeEntered) })
			<-probeRelease
			return IdleSample{Idle: time.Second, OnACPower: true}, nil
		},
	)
	hook := gate.PauseHook(string(BackgroundTaskPerceptualHash))
	done := make(chan error, 1)
	go func() { done <- hook.Wait(context.Background(), nil) }()

	// worker 已经进了 wait，卡在探测里：此刻等待集还是空的。
	<-probeEntered
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 0 {
		t.Fatalf("此刻还不该有登记好的等待者: %+v", waiting)
	}
	hook.Release()
	close(probeRelease)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("落在窗口里的摘钩子也应放行且不报错，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("摘钩子落在登记窗口里时 worker 被永久挂住了")
	}
}

// 摘钩子只放行停在检查点上的 worker：排队等空闲的自动唤醒不受影响
// （用户显式跑了一轮，不代表下一轮自动任务也该无视空闲门）。
func TestIdleGateReleaseDoesNotReleaseQueuedAutoWake(t *testing.T) {
	gate := busyGate()
	stop := runGateInBackground(t, gate, "phash")
	defer stop()
	waitForGateWaiting(t, gate, "phash")

	gate.Release("phash")

	// 自动唤醒仍在等：等待清单里还留着它，且 start 没被执行（由 stop 断言）。
	time.Sleep(30 * time.Millisecond)
	waiting := gate.GetIdleSchedulerStatus().Waiting
	if len(waiting) != 1 || waiting[0].TaskKey != "phash" {
		t.Fatalf("排队中的自动唤醒不该被摘钩子放行: %+v", waiting)
	}
}

// 应用退出：ctx 取消，等待者退出，start 一次也不执行。
func TestIdleGateRunHonorsCancellation(t *testing.T) {
	gate := busyGate()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	executed := false
	go func() {
		result <- gate.Run(ctx, "phash", func(context.Context) error {
			executed = true
			return nil
		})
	}()
	waitForGateWaiting(t, gate, "phash")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后应返回 context.Canceled，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后等待者应立即退出")
	}
	if executed {
		t.Fatal("被取消的任务不该执行")
	}
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 0 {
		t.Fatalf("退出后不该留在等待清单: %+v", waiting)
	}
}

// 一个任务的 ctx 被取消，不能把 context.Canceled 缓存成所有人共享的 probe_failed。
func TestIdleGateCancellationDoesNotPoisonTheProbeCache(t *testing.T) {
	// 探测跑在等待者的 goroutine 里，读写要跨 goroutine：用 atomic.Value 存。
	var probeCtxErr atomic.Value
	gate := newTestIdleGate(
		IdleSchedulerSettings{Enabled: true, ThresholdMinutes: 5},
		func(ctx context.Context) (IdleSample, error) {
			// 探测必须拿到一个没被任何调用方取消的 ctx。
			if err := ctx.Err(); err != nil {
				probeCtxErr.Store(err)
			}
			return IdleSample{Idle: time.Second, OnACPower: true}, nil
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = gate.Run(ctx, "technical", func(context.Context) error { return nil }) }()
	waitForGateWaiting(t, gate, "technical")
	cancel()
	time.Sleep(20 * time.Millisecond)

	status := gate.GetIdleSchedulerStatus()
	if status.ProbeError != "" {
		t.Fatalf("取消不该被记成探测失败: %q", status.ProbeError)
	}
	if err := probeCtxErr.Load(); err != nil {
		t.Fatalf("探测拿到了已取消的 ctx: %v", err)
	}
}

// 探测命令失败一律视为不空闲，并如实标 probe_failed——绝不猜一个"大概空闲"。
func TestIdleGateProbeFailureCountsAsBusy(t *testing.T) {
	gate := newTestIdleGate(
		IdleSchedulerSettings{Enabled: true, ThresholdMinutes: 5},
		func(context.Context) (IdleSample, error) { return IdleSample{}, errors.New("ioreg 挂了") },
	)
	stop := runGateInBackground(t, gate, "exif")
	defer stop()
	assertGateWaitReason(t, gate, "exif", IdleWaitReasonProbeFailed)
	if status := gate.GetIdleSchedulerStatus(); status.ProbeError == "" {
		t.Fatal("状态里应带上探测错误")
	}
}

// 要求接电源时，靠电池就等着。
func TestIdleGateRequiresACPowerWhenConfigured(t *testing.T) {
	gate := newTestIdleGate(
		IdleSchedulerSettings{Enabled: true, ThresholdMinutes: 1, RequireACPower: true},
		staticIdleProbe(time.Hour, false),
	)
	stop := runGateInBackground(t, gate, "technical")
	defer stop()
	assertGateWaitReason(t, gate, "technical", IdleWaitReasonOnBattery)
}

// 开关关掉：Run 与钩子都直通，行为与没有空闲门时完全一致。
func TestIdleGateDisabledPassesEverythingThrough(t *testing.T) {
	gate := newTestIdleGate(
		IdleSchedulerSettings{Enabled: false, ThresholdMinutes: 5},
		staticIdleProbe(0, false),
	)
	executed := false
	if err := gate.Run(context.Background(), "technical", func(context.Context) error {
		executed = true
		return nil
	}); err != nil {
		t.Fatalf("开关关闭时应直通: %v", err)
	}
	if !executed {
		t.Fatal("开关关闭时任务应立即执行")
	}
	notified := false
	if err := gate.PauseHook("technical").Wait(context.Background(), func(TaskGateState) { notified = true }); err != nil {
		t.Fatalf("开关关闭时钩子应直通: %v", err)
	}
	if notified {
		t.Fatal("直通时不该回报 waiting_idle")
	}
}

// 保存设置后立刻生效：关掉开关不用等门自己 30 秒刷一次快照。
func TestIdleGateInvalidateSettingsReleasesWaitersImmediately(t *testing.T) {
	var mu sync.Mutex
	settings := IdleSchedulerSettings{Enabled: true, ThresholdMinutes: 5}
	gate := NewIdleGate()
	gate.SetProbe(staticIdleProbe(time.Second, true))
	gate.loadSettings = func() (IdleSchedulerSettings, error) {
		mu.Lock()
		defer mu.Unlock()
		return settings, nil
	}
	// 故意保留默认的 30 秒缓存：只有 InvalidateSettings 才能让改动立刻生效。
	started := make(chan struct{})
	go func() {
		_ = gate.Run(context.Background(), "technical", func(context.Context) error {
			close(started)
			return nil
		})
	}()
	waitForGateWaiting(t, gate, "technical")

	mu.Lock()
	settings.Enabled = false
	mu.Unlock()
	gate.InvalidateSettings()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("关掉开关后应立即放行，而不是等 30 秒缓存过期")
	}
}

// 项间检查点：阻塞时把 waiting_idle 与原因回报给服务的 Status，放行后清回零值。
func TestIdleGatePauseHookReportsWaitingState(t *testing.T) {
	var mu sync.Mutex
	idle := time.Second
	gate := newTestIdleGate(
		IdleSchedulerSettings{Enabled: true, ThresholdMinutes: 5},
		func(context.Context) (IdleSample, error) {
			mu.Lock()
			defer mu.Unlock()
			return IdleSample{Idle: idle, OnACPower: true}, nil
		},
	)
	var stateMu sync.Mutex
	var states []TaskGateState
	done := make(chan error, 1)
	go func() {
		done <- gate.PauseHook("phash").Wait(context.Background(), func(state TaskGateState) {
			stateMu.Lock()
			states = append(states, state)
			stateMu.Unlock()
		})
	}()
	waitForGateWaiting(t, gate, "phash")

	mu.Lock()
	idle = time.Hour
	mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("空闲后钩子应放行: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("空闲后钩子应放行")
	}

	stateMu.Lock()
	defer stateMu.Unlock()
	if len(states) < 2 {
		t.Fatalf("应先回报等待、再回报放行，实际 %+v", states)
	}
	if !states[0].WaitingIdle || states[0].Reason != IdleWaitReasonUserActive {
		t.Fatalf("第一次回报应是 waiting_idle/user_active: %+v", states[0])
	}
	last := states[len(states)-1]
	if last.WaitingIdle || last.Reason != "" {
		t.Fatalf("放行后应清回零值: %+v", last)
	}
}

// 时间窗支持跨午夜写法（22:00–06:00）。
func TestWithinIdleWindowHandlesMidnightCrossing(t *testing.T) {
	day := func(hour, minute int) time.Time {
		return time.Date(2026, 9, 2, hour, minute, 0, 0, time.Local)
	}
	cases := []struct {
		name        string
		now         time.Time
		start, end  string
		wantAllowed bool
	}{
		{"跨午夜-深夜在窗内", day(23, 30), "22:00", "06:00", true},
		{"跨午夜-凌晨在窗内", day(5, 59), "22:00", "06:00", true},
		{"跨午夜-窗口末端已排除", day(6, 0), "22:00", "06:00", false},
		{"跨午夜-白天在窗外", day(12, 0), "22:00", "06:00", false},
		{"同日窗内", day(10, 0), "09:00", "17:00", true},
		{"同日窗外", day(18, 0), "09:00", "17:00", false},
		{"窗口为空不限时段", day(3, 0), "", "", true},
		{"只填一端不限时段", day(3, 0), "22:00", "", true},
		{"两端相同不限时段", day(3, 0), "09:00", "09:00", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := withinIdleWindow(testCase.now, testCase.start, testCase.end); got != testCase.wantAllowed {
				t.Fatalf("窗口判定错误: got=%v want=%v", got, testCase.wantAllowed)
			}
		})
	}
}

// 时间窗之外要等着，并给出 outside_window 的原因。
func TestIdleGateWaitsOutsideConfiguredWindow(t *testing.T) {
	gate := newTestIdleGate(
		IdleSchedulerSettings{Enabled: true, ThresholdMinutes: 1, WindowStart: "22:00", WindowEnd: "06:00"},
		staticIdleProbe(time.Hour, true),
	)
	gate.now = func() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.Local) }
	stop := runGateInBackground(t, gate, "technical")
	defer stop()
	assertGateWaitReason(t, gate, "technical", IdleWaitReasonOutsideWindow)
}

// 设置读不出来时按"门关着"处理，但要如实写进状态，不静默。
func TestIdleGateReportsSettingsError(t *testing.T) {
	gate := NewIdleGate()
	gate.SetProbe(staticIdleProbe(time.Second, true))
	gate.SetProbeInterval(5 * time.Millisecond)
	gate.loadSettings = func() (IdleSchedulerSettings, error) {
		return IdleSchedulerSettings{}, errors.New("数据库未初始化")
	}

	if err := gate.Run(context.Background(), "technical", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("设置读不出来时应直通: %v", err)
	}
	if status := gate.GetIdleSchedulerStatus(); status.SettingsError == "" {
		t.Fatal("状态里应带上设置读取错误")
	}
}

// 未知 taskKey 一律当场炸：写错一个字符不能变成一个永远没人清的等待项。
func TestIdleGateRejectsUnknownTaskKey(t *testing.T) {
	gate := busyGate()
	if err := gate.RunGatedTaskNow("not_a_task"); err == nil {
		t.Fatal("未知任务标识应报错")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("未知任务标识应当 panic")
		}
	}()
	_ = gate.Run(context.Background(), "not_a_task", func(context.Context) error { return nil })
}

func TestIdleGatePauseHookRejectsUnknownTaskKey(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("未知任务标识应当 panic")
		}
	}()
	NewIdleGate().PauseHook("not_a_task")
}

// 阈值归一化：非正取默认 5，超过 120 收到 120。
func TestNormalizeIdleThresholdMinutes(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 5}, {-3, 5}, {1, 1}, {5, 5}, {120, 120}, {121, 120}, {100000, 120},
	}
	for _, testCase := range cases {
		if got := NormalizeIdleThresholdMinutes(testCase.in); got != testCase.want {
			t.Fatalf("归一化错误 in=%d got=%d want=%d", testCase.in, got, testCase.want)
		}
	}
}

// 时间窗只认 HH:MM，其余归一成空（等于不设时间窗）。
func TestNormalizeIdleWindowBound(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""}, {" 22:00 ", "22:00"}, {"6:5", "06:05"}, {"24:00", ""},
		{"22:60", ""}, {"晚上", ""}, {"22-00", ""}, {"22:00:00", ""},
	}
	for _, testCase := range cases {
		if got := NormalizeIdleWindowBound(testCase.in); got != testCase.want {
			t.Fatalf("时间窗归一化错误 in=%q got=%q want=%q", testCase.in, got, testCase.want)
		}
	}
}

// runGateInBackground 起一个被挡住的自动任务，返回收尾函数（取消并确认没执行）。
func runGateInBackground(t *testing.T, gate *IdleGate, taskKey string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var finished sync.WaitGroup
	finished.Add(1)
	var runErr error
	ran := false
	go func() {
		defer finished.Done()
		runErr = gate.Run(ctx, taskKey, func(context.Context) error {
			ran = true
			return nil
		})
	}()
	return func() {
		cancel()
		finished.Wait()
		if ran {
			t.Errorf("被挡住的任务不该执行")
		}
		if !errors.Is(runErr, context.Canceled) {
			t.Errorf("取消后应返回 context.Canceled，实际 %v", runErr)
		}
	}
}

func assertGateWaitReason(t *testing.T, gate *IdleGate, taskKey, reason string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last []IdleWaitingTask
	for time.Now().Before(deadline) {
		last = gate.GetIdleSchedulerStatus().Waiting
		for _, waiting := range last {
			if waiting.TaskKey == taskKey && waiting.Reason == reason {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待 %s 以原因 %s 进入等待清单超时，实际 %+v", taskKey, reason, last)
}

func waitForGateWaiting(t *testing.T, gate *IdleGate, taskKey string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, waiting := range gate.GetIdleSchedulerStatus().Waiting {
			if waiting.TaskKey == taskKey {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待 %s 进入等待清单超时", taskKey)
}

// waitForBypassCleared 等 bypass 被清掉；nudge 用来推进那些需要外部事件的清除路径。
func waitForBypassCleared(t *testing.T, gate *IdleGate, taskKey string, nudge func()) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if nudge != nil {
			nudge()
		}
		found := false
		for _, key := range gate.GetIdleSchedulerStatus().BypassTasks {
			if key == taskKey {
				found = true
			}
		}
		if !found {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s 的 bypass 没有被清掉", taskKey)
}

// 摘钩子发生在 worker 走进 Wait 之前：Wait 必须当场返回，不能再等空闲。
//
// worker 读钩子指针与显式启动清指针同步在服务的同一把锁上，所以它要么拿到 nil、
// 要么拿到这个即将被摘掉的实例；"已摘除"记在实例上，两种情形都不会漏。
func TestIdleGatePauseHookReleasedBeforeWaitReturnsImmediately(t *testing.T) {
	gate := busyGate()
	hook := gate.PauseHook(string(BackgroundTaskPerceptualHash))
	hook.Release()

	done := make(chan error, 1)
	go func() { done <- hook.Wait(context.Background(), nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("已摘掉的钩子应立即放行，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("已摘掉的钩子仍然把 worker 挡住了")
	}
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 0 {
		t.Fatalf("已摘掉的钩子不该登记等待: %+v", waiting)
	}
}

// Release 与 Wait 同时发起：无论落在入口前、evaluate 中还是登记之后，
// Wait 都必须返回 nil。用 -race -count=20 反复跑这条来撞窗口。
func TestIdleGatePauseHookReleaseRacesWithWait(t *testing.T) {
	gate := busyGate()
	hook := gate.PauseHook(string(BackgroundTaskTechnical))

	done := make(chan error, 1)
	var ready sync.WaitGroup
	ready.Add(2)
	go func() {
		ready.Done()
		ready.Wait()
		done <- hook.Wait(context.Background(), nil)
	}()
	go func() {
		ready.Done()
		ready.Wait()
		hook.Release()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("摘钩子与等待竞争时应放行且不报错，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("摘钩子与等待竞争时 worker 被挂住了")
	}
}
