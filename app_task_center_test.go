package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"

	"gorm.io/gorm"
)

// P-024 任务中心（D-PC18、APP-03）。

// recordedEvent 是 emitRuntimeEvent 截获的一次事件。
type recordedEvent struct {
	name string
	data []interface{}
}

// stubRuntimeEvents 把 runtime.EventsEmit 换成记录调用。
func stubRuntimeEvents(t *testing.T) func() []recordedEvent {
	t.Helper()
	var mu sync.Mutex
	events := []recordedEvent{}
	previous := emitRuntimeEvent
	emitRuntimeEvent = func(_ context.Context, name string, data ...interface{}) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, recordedEvent{name: name, data: data})
	}
	t.Cleanup(func() { emitRuntimeEvent = previous })
	return func() []recordedEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedEvent(nil), events...)
	}
}

func taskCenterItemByKey(t *testing.T, snapshot TaskCenterSnapshot, key string) TaskCenterItem {
	t.Helper()
	for _, item := range snapshot.Items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("快照里没有 %s: %+v", key, snapshot.Items)
	return TaskCenterItem{}
}

// APP-03：适配表必须恰好覆盖登记表的 26 个 key（P-007 加入 scene_index，P-008 加入 video_edit）。登记表新增一个 key 而适配表没跟上、
// 或适配表里留着登记表已删掉的 key，这个用例都会失败；快照的 Items 也按登记表顺序全部列出。
func TestAPP03TaskCenterAdaptersCoverEveryRegistryKey(t *testing.T) {
	keys := services.BackgroundTaskKeys()
	if len(keys) != 26 {
		t.Fatalf("登记表应有 26 个 key（详细设计 §1.2a + scene_index + video_edit），实际 %d: %v", len(keys), keys)
	}
	registered := map[string]bool{}
	for _, key := range keys {
		registered[key] = true
		if _, ok := taskCenterAdapters[services.BackgroundTaskKey(key)]; !ok {
			t.Errorf("登记表 key %q 没有任务中心适配函数", key)
		}
	}
	for key := range taskCenterAdapters {
		if !registered[string(key)] {
			t.Errorf("适配表里的 %q 不在登记表中", key)
		}
	}

	snapshot := (&App{}).GetTaskCenterSnapshot()
	got := make([]string, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		got = append(got, item.Key)
		if item.State != TaskCenterStateIdle || item.Actions == nil {
			t.Errorf("没有任何服务时 %s 应为 idle 且 actions 非 nil: %+v", item.Key, item)
		}
	}
	if !reflect.DeepEqual(got, keys) {
		t.Fatalf("快照应按登记表顺序列出全部 key:\n got %v\nwant %v", got, keys)
	}
	if snapshot.Recent == nil || snapshot.Warnings == nil {
		t.Fatalf("Recent 与 Warnings 应为空数组而不是 null: %+v", snapshot)
	}
}

// APP-03：三种状态与动作的组合。running 优先于 waiting_idle（跑到一半停在检查点上等空闲），
// 此时 gate_reason 仍给出原因；进度只在运行中给出。
func TestAPP03TaskCenterItemStatesAndActions(t *testing.T) {
	progress := &TaskProgress{Done: 3, Total: 10}
	cases := []struct {
		name        string
		state       taskCenterKeyState
		registry    bool
		waiting     string
		wantState   string
		wantActions []string
		wantProg    bool
	}{
		{"idle 可启动", taskCenterKeyState{canStart: true, canCancel: true}, false, "", TaskCenterStateIdle, []string{TaskCenterActionStart}, false},
		{"idle 无零参启动", taskCenterKeyState{}, false, "", TaskCenterStateIdle, []string{}, false},
		{"登记表在跑可取消", taskCenterKeyState{canStart: true, canCancel: true, progress: progress}, true, "", TaskCenterStateRunning, []string{TaskCenterActionCancel}, true},
		{"服务自报在跑", taskCenterKeyState{running: true, progress: progress}, false, "", TaskCenterStateRunning, []string{}, true},
		{"等待空闲", taskCenterKeyState{canStart: true, progress: progress}, false, services.IdleWaitReasonUserActive, TaskCenterStateWaitingIdle, []string{TaskCenterActionRunNow}, false},
		{"运行中停在检查点", taskCenterKeyState{canCancel: true}, true, services.IdleWaitReasonOnBattery, TaskCenterStateRunning, []string{TaskCenterActionCancel, TaskCenterActionRunNow}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &taskCenterSources{running: map[string]bool{"phash": tc.registry}, waiting: map[string]string{}}
			if tc.waiting != "" {
				src.waiting["phash"] = tc.waiting
			}
			item := buildTaskCenterItem("phash", tc.state, src)
			if item.State != tc.wantState || !reflect.DeepEqual(item.Actions, tc.wantActions) || item.GateReason != tc.waiting {
				t.Fatalf("state=%s actions=%v gate=%q，期望 %s %v %q", item.State, item.Actions, item.GateReason, tc.wantState, tc.wantActions, tc.waiting)
			}
			if (item.Progress != nil) != tc.wantProg {
				t.Fatalf("progress=%+v，期望有进度=%v", item.Progress, tc.wantProg)
			}
		})
	}
}

// APP-03：经真实的登记表与空闲门：登记表里的 key 是 running，被空闲门挡住的是 waiting_idle
// 并带原因与「立即运行」，其余是 idle 且只列出有零参绑定的「启动」。
func TestAPP03TaskCenterSnapshotReportsRunningWaitingIdleAndIdle(t *testing.T) {
	setupAppTestDB(t)
	settings := models.Settings{VideoExtensions: ".mp4", PlayWeight: 2, IdleSchedulingEnabled: true, IdleThresholdMinutes: 5}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatalf("创建设置失败: %v", err)
	}
	app := NewApp()
	app.idleGate.SetProbe(func(context.Context) (services.IdleSample, error) {
		return services.IdleSample{Idle: 10 * time.Second, OnACPower: true}, nil
	})
	app.idleGate.SetProbeInterval(5 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = app.idleGate.Run(ctx, string(services.BackgroundTaskAITagging), func(context.Context) error { return nil })
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitForIdleGateWaiting(t, app, string(services.BackgroundTaskAITagging))

	app.backgroundTasks.Begin(services.BackgroundTaskWatchlistEnrich)
	t.Cleanup(func() { app.backgroundTasks.End(services.BackgroundTaskWatchlistEnrich) })

	snapshot := app.GetTaskCenterSnapshot()
	if len(snapshot.Items) != len(services.BackgroundTaskKeys()) {
		t.Fatalf("快照应覆盖全部 key: %d", len(snapshot.Items))
	}
	if len(snapshot.Warnings) != 0 {
		t.Fatalf("数据库正常时不应有警告: %v", snapshot.Warnings)
	}

	waiting := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskAITagging))
	if waiting.State != TaskCenterStateWaitingIdle || waiting.GateReason != services.IdleWaitReasonUserActive ||
		!reflect.DeepEqual(waiting.Actions, []string{TaskCenterActionRunNow}) {
		t.Fatalf("被空闲门挡住的 AI 打标应为 waiting_idle + run_now: %+v", waiting)
	}
	running := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskWatchlistEnrich))
	if running.State != TaskCenterStateRunning || len(running.Actions) != 0 || running.GateReason != "" {
		t.Fatalf("登记表里的片单补全应为 running、没有零参动作: %+v", running)
	}
	idle := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskEXIF))
	if idle.State != TaskCenterStateIdle || idle.Progress != nil || !reflect.DeepEqual(idle.Actions, []string{TaskCenterActionStart}) {
		t.Fatalf("空闲的 EXIF 补全应为 idle + start: %+v", idle)
	}
	enhancement := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskEnhancement))
	if enhancement.State != TaskCenterStateIdle || len(enhancement.Actions) != 0 {
		t.Fatalf("超分没有零参的启动绑定，key 这一层不给动作: %+v", enhancement)
	}
}

// APP-03：Recent 覆盖字幕、超分、下载、播放代理四类；各类的上一轮结果据此给出（失败的那一类
// last_run.failed > 0，顶栏据此亮红点）。
func TestAPP03TaskCenterRecentCoversSubtitleEnhancementDownloadAndProxy(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	root := t.TempDir()
	source := models.Video{Name: "片A.mp4", Path: filepath.Join(root, "a.mp4"), Directory: root}
	output := models.Video{Name: "片A-超分.mp4", Path: filepath.Join(root, "a-2x.mp4"), Directory: root}
	for _, video := range []*models.Video{&source, &output} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatal(err)
		}
	}
	finished := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := database.DB.Create(&models.SubtitleJob{
		VideoID: source.ID, Engine: "whisperx", SourceLang: "ja", Status: string(services.SubtitleQueueTaskStatusFailed),
		Message: "识别失败", FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.VideoEnhancementTask{
		VideoID: source.ID, Profile: "general", Scale: 2, Status: models.EnhancementStatusCompleted,
		OutputBasename: "a-2x.mp4", OutputVideoID: &output.ID, FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.BrowserDownloadTask{
		TaskUID: "0123456789abcdef0123456789abcdef", DisplayURL: "https://example.com/v.m3u8", FileName: "v.mp4",
		Directory: root, Status: browserDownloadFailed, Error: "网络中断", FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	app.browserDownloads.SetStore(func() *gorm.DB { return database.DB })

	app.playbackProxies = services.NewPlaybackProxyService(t.TempDir(), nil)
	const missingVideoID = 987654
	if _, err := app.playbackProxies.CreatePlaybackProxy(context.Background(), missingVideoID); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !app.playbackProxies.Status().Completed {
		if time.Now().After(deadline) {
			t.Fatalf("代理任务应很快结束: %+v", app.playbackProxies.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}

	snapshot := app.GetTaskCenterSnapshot()
	byKind := map[string]TaskRecentJob{}
	for _, job := range snapshot.Recent {
		if _, seen := byKind[job.Kind]; !seen {
			byKind[job.Kind] = job
		}
	}
	for _, kind := range []string{TaskRecentKindSubtitle, TaskRecentKindEnhancement, TaskRecentKindDownload, TaskRecentKindProxy} {
		if _, ok := byKind[kind]; !ok {
			t.Fatalf("Recent 应包含 %s: %+v", kind, snapshot.Recent)
		}
	}

	subtitle := byKind[TaskRecentKindSubtitle]
	if subtitle.Title != "片A.mp4" || subtitle.Status != "failed" || subtitle.Message != "识别失败" ||
		!reflect.DeepEqual(subtitle.Actions, []string{"retry"}) || subtitle.FinishedAt == nil {
		t.Fatalf("字幕任务映射不对: %+v", subtitle)
	}
	enhancement := byKind[TaskRecentKindEnhancement]
	if enhancement.Status != models.EnhancementStatusCompleted || enhancement.OutputVideoID != output.ID ||
		!reflect.DeepEqual(enhancement.Actions, []string{"reveal_output", "open_output_in_library"}) {
		t.Fatalf("完成的超分任务应带产物与查看动作: %+v", enhancement)
	}
	download := byKind[TaskRecentKindDownload]
	if download.ID != "0123456789abcdef0123456789abcdef" || download.Status != browserDownloadFailed ||
		download.Message != "网络中断" || download.Title != "v.mp4" || len(download.Actions) != 0 {
		t.Fatalf("历史下载任务（重启后不可重试）映射不对: %+v", download)
	}
	proxy := byKind[TaskRecentKindProxy]
	if proxy.VideoID != missingVideoID || proxy.Status != services.PlaybackProxyCodeFileMissing || len(proxy.Actions) != 0 || proxy.FinishedAt == nil {
		t.Fatalf("代理逐项结果映射不对（源文件不在不给重试）: %+v", proxy)
	}

	subtitleItem := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskSubtitle))
	if subtitleItem.LastRun == nil || subtitleItem.LastRun.Failed != 1 || !reflect.DeepEqual(subtitleItem.LastRun.Failures, []string{"片A.mp4：识别失败"}) {
		t.Fatalf("字幕的上一轮应是那次失败: %+v", subtitleItem.LastRun)
	}
	enhancementItem := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskEnhancement))
	if enhancementItem.LastRun == nil || enhancementItem.LastRun.Succeeded != 1 || enhancementItem.LastRun.Failed != 0 {
		t.Fatalf("超分的上一轮应是那次完成: %+v", enhancementItem.LastRun)
	}
	downloadItem := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskBrowserDownload))
	if downloadItem.LastRun == nil || downloadItem.LastRun.Failed != 1 {
		t.Fatalf("下载的上一轮应是那次失败: %+v", downloadItem.LastRun)
	}
	proxyItem := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskProxy))
	if proxyItem.State != TaskCenterStateIdle || proxyItem.LastRun == nil || proxyItem.LastRun.Failed != 1 ||
		len(proxyItem.LastRun.Failures) != 1 || !strings.Contains(proxyItem.LastRun.Failures[0], "视频记录不存在") {
		t.Fatalf("代理的上一轮应带失败明细: %+v", proxyItem)
	}
}

// APP-03 / META-08：恢复或切换进行中（维护围栏立着）时，快照照常列出全部 key 与内存里的状态，
// 读库的部分跳过并给出中文说明；待处理汇总直接返回同一句说明。
func TestAPP03TaskCenterDuringMaintenanceSkipsDatabaseReads(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	app.backgroundTasks.Begin(services.BackgroundTaskProxy)
	t.Cleanup(func() { app.backgroundTasks.End(services.BackgroundTaskProxy) })
	release := database.BeginMaintenance()
	t.Cleanup(release)

	const reason = "数据库正在恢复或切换，暂时无法读取"
	snapshot := app.GetTaskCenterSnapshot()
	if len(snapshot.Items) != len(services.BackgroundTaskKeys()) || !reflect.DeepEqual(snapshot.Warnings, []string{reason}) {
		t.Fatalf("维护期间快照应列出全部 key 并只带中文说明: items=%d warnings=%v", len(snapshot.Items), snapshot.Warnings)
	}
	if item := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskProxy)); item.State != TaskCenterStateRunning {
		t.Fatalf("登记表里的运行状态照样可读: %+v", item)
	}
	if item := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskBackup)); len(item.Actions) != 0 {
		t.Fatalf("维护期间不给「立即备份」: %+v", item)
	}
	assertNoMaintenanceText(t, snapshot)
	if _, err := app.GetPendingWorkSummary(); err == nil || err.Error() != reason {
		t.Fatalf("维护期间待处理汇总应返回中文说明: %v", err)
	}
}

// APP-03：LastRun 的失败明细最多 50 条、每条最多 500 字。
func TestAPP03TaskCenterBoundsLastRunFailures(t *testing.T) {
	failures := make([]string, 0, 60)
	for index := 0; index < 60; index++ {
		failures = append(failures, strings.Repeat("错", 600))
	}
	bounded := boundTaskFailures(failures)
	if len(bounded) != taskCenterFailureLimit {
		t.Fatalf("失败明细应截到 %d 条，实际 %d", taskCenterFailureLimit, len(bounded))
	}
	for _, failure := range bounded {
		if runes := []rune(failure); len(runes) != taskCenterFailureMaxRunes {
			t.Fatalf("每条应截到 %d 字，实际 %d", taskCenterFailureMaxRunes, len(runes))
		}
	}
}

// APP-03：task-center-changed 在合并窗口内只发一次，载荷是当时的快照；窗口过后的变化再发一次。
func TestAPP03TaskCenterChangedEventsAreMergedWithinWindow(t *testing.T) {
	events := stubRuntimeEvents(t)
	const delay = 30 * time.Millisecond
	setTaskCenterChangeDelayForTest(t, delay)

	(&App{}).notifyTaskCenterChanged() // 运行时上下文还没有：不排事件
	app := &App{ctx: context.Background(), backgroundTasks: services.NewBackgroundTaskRegistry()}
	for index := 0; index < 5; index++ {
		app.notifyTaskCenterChanged()
	}
	waitTaskCenterEvents(t, events, 1)
	time.Sleep(3 * delay)
	got := events()
	if len(got) != 1 || got[0].name != taskCenterChangedEvent {
		t.Fatalf("合并窗口内的 5 次变化应只发 1 次 task-center-changed: %+v", got)
	}
	snapshot, ok := got[0].data[0].(TaskCenterSnapshot)
	if !ok || len(snapshot.Items) != len(services.BackgroundTaskKeys()) {
		t.Fatalf("载荷应为完整快照: %#v", got[0].data)
	}

	app.notifyTaskCenterChanged()
	waitTaskCenterEvents(t, events, 2)
}

func waitTaskCenterEvents(t *testing.T, events func() []recordedEvent, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(events()) < want {
		if time.Now().After(deadline) {
			t.Fatalf("应收到 %d 次 task-center-changed，实际 %d", want, len(events()))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAppConsolidationTaskCenterUsesSummariesAndPersistentOpenActions(t *testing.T) {
	setupAppTestDB(t)
	a := newAppConsolidationFixture(t, t.TempDir())
	finished := time.Now()
	for i, status := range []string{"completed", "failed", "cancelled", "interrupted", "running"} {
		row := models.CleanupConsolidationTask{PreviewID: status, OwnerScope: "foreign", Status: status, Total: 3, Completed: i % 3, Error: "detail", FinishedAt: &finished}
		if status == "running" {
			row.FinishedAt = nil
		}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Header-only history must remain available when detail records are missing/corrupt.
	callback := "test:no_consolidation_detail_poll"
	if err := database.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "cleanup_consolidation_task_plans" || tx.Statement.Table == "cleanup_consolidation_task_items" {
			t.Error("task center decoded full consolidation details")
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.DB.Callback().Query().Remove(callback) })
	snapshot := a.GetTaskCenterSnapshot()
	if len(snapshot.Warnings) != 0 {
		t.Fatal(snapshot.Warnings)
	}
	item := taskCenterItemByKey(t, snapshot, "cleanup_consolidation")
	if item.State != TaskCenterStateRunning || len(item.Actions) != 0 || item.Progress == nil || item.Progress.Done != 1 || item.Progress.Total != 3 {
		t.Fatalf("foreign running item: %+v", item)
	}
	if item.LastRun == nil || item.LastRun.Succeeded != 0 {
		t.Fatalf("previous outcome: %+v", item.LastRun)
	}
	var jobs []TaskRecentJob
	for _, job := range snapshot.Recent {
		if job.Kind == TaskRecentKindCleanupConsolidation {
			jobs = append(jobs, job)
		}
	}
	if len(jobs) != 5 {
		t.Fatalf("history: %+v", jobs)
	}
	for i, job := range jobs {
		if job.ID != strconv.Itoa(5-i) || !reflect.DeepEqual(job.Actions, []string{"open_consolidation"}) {
			t.Fatalf("persistent route/action: %+v", job)
		}
	}
	a.backgroundTasks.Begin(services.BackgroundTaskCleanupConsolidation)
	local := taskCenterItemByKey(t, a.GetTaskCenterSnapshot(), "cleanup_consolidation")
	a.backgroundTasks.End(services.BackgroundTaskCleanupConsolidation)
	if !reflect.DeepEqual(local.Actions, []string{TaskCenterActionCancel}) {
		t.Fatalf("local cancellation: %+v", local)
	}
	if err := database.DB.Model(&models.CleanupConsolidationTask{}).Where("status = ?", "running").Update("status", "completed").Error; err != nil {
		t.Fatal(err)
	}
	idle := taskCenterItemByKey(t, a.GetTaskCenterSnapshot(), "cleanup_consolidation")
	if idle.State != TaskCenterStateIdle || len(idle.Actions) != 0 {
		t.Fatalf("no preview bypass start: %+v", idle)
	}
}

func TestAppConsolidationTaskCenterWarningsAreBounded(t *testing.T) {
	a := &App{}
	a.consolidationLifecycle.recoveryErr = errors.New(strings.Repeat("失", 1000))
	warning := a.cleanupConsolidationWarning()
	if warning == "" || len([]rune(warning)) > taskCenterFailureMaxRunes+1 {
		t.Fatalf("unbounded warning: %d", len([]rune(warning)))
	}
}

// Production reads this test-overridden delay under the notifier mutex. A
// background idle probe can still notify after a test cancels its App context.
func setTaskCenterChangeDelayForTest(t *testing.T, delay time.Duration) {
	t.Helper()
	taskCenterChanges.mu.Lock()
	previous := taskCenterChangeDelay
	taskCenterChangeDelay = delay
	taskCenterChanges.mu.Unlock()
	t.Cleanup(func() {
		taskCenterChanges.mu.Lock()
		taskCenterChangeDelay = previous
		taskCenterChanges.mu.Unlock()
	})
}
