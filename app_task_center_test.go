package main

import (
	"context"
	"errors"
	"os"
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

// APP-03：适配表必须恰好覆盖登记表的 31 个 key（P-007 加入 scene_index，P-008 加入 video_edit，数据维护加入四项，
// D-P3-05 加入 local_metadata_export）。登记表新增一个 key 而适配表没跟上、
// 或适配表里留着登记表已删掉的 key，这个用例都会失败；快照的 Items 也按登记表顺序全部列出。
func TestAPP03TaskCenterAdaptersCoverEveryRegistryKey(t *testing.T) {
	keys := services.BackgroundTaskKeys()
	if len(keys) != 31 {
		t.Fatalf("登记表应有 31 个 key（详细设计 §1.2a + scene_index + video_edit + 数据维护四项 + local_metadata_export），实际 %d: %v", len(keys), keys)
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

// ===== D-P3-05：任务中心补齐片库页后台任务状态条上的几项 =====

// 各批量任务的服务状态原样搬进上一轮：跳过数（有跳过口径的都填）、取消与完成分开、近重复指纹的
// 复用 / 移出范围、总数为 0 的「已是最新」。期望里没写的字段都应为零值。
func TestP3ABatchRunsMapServiceCountsIntoLastRun(t *testing.T) {
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		run  batchRun
		want TaskLastRun
	}{
		{"技术信息：跳过数照搬，完成不算取消", technicalBackfillRun(services.TechnicalBackfillStatus{Completed: true, Total: 6, Processed: 6, Succeeded: 3, Skipped: 2, Failed: 1, UpdatedAt: &at,
			Failures: []services.TechnicalBackfillFailure{{Name: "a.mp4", Error: "坏文件"}}}),
			TaskLastRun{Succeeded: 3, Skipped: 2, Failed: 1, Failures: []string{"a.mp4：坏文件"}}},
		{"技术信息：取消", technicalBackfillRun(services.TechnicalBackfillStatus{Cancelled: true, Total: 6, Processed: 2, Succeeded: 1, Skipped: 1, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 1, Skipped: 1, Cancelled: true}},
		{"技术信息：总数为 0 是已是最新", technicalBackfillRun(services.TechnicalBackfillStatus{Completed: true, UpdatedAt: &at}),
			TaskLastRun{NothingToDo: true}},
		{"技术信息：准备失败（总数 0、失败 1）不是已是最新", technicalBackfillRun(services.TechnicalBackfillStatus{Completed: true, Failed: 1, UpdatedAt: &at,
			Failures: []services.TechnicalBackfillFailure{{Name: "准备补全任务", Error: "库不可读"}}}),
			TaskLastRun{Failed: 1, Failures: []string{"准备补全任务：库不可读"}}},
		{"近重复指纹：复用与移出范围", perceptualHashRun(services.PerceptualHashStatus{Completed: true, Total: 10, Processed: 10, Succeeded: 4, Skipped: 1, Reused: 3, OutOfScope: 2, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 4, Skipped: 1, Reused: 3, OutOfScope: 2}},
		{"近重复指纹：取消时服务同时置 Completed，总数为 0 也不算已是最新", perceptualHashRun(services.PerceptualHashStatus{Completed: true, Cancelled: true, UpdatedAt: &at}),
			TaskLastRun{Cancelled: true}},
		{"帧哈希：跳过", frameHashRun(services.FrameHashStatus{Completed: true, Total: 3, Processed: 3, Succeeded: 1, Skipped: 2, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 1, Skipped: 2}},
		{"帧哈希：总数为 0", frameHashRun(services.FrameHashStatus{Completed: true, UpdatedAt: &at}),
			TaskLastRun{NothingToDo: true}},
		{"本地资料补全：跳过与取消", localMetadataBackfillRun(services.LocalMetadataBackfillStatus{Completed: true, Cancelled: true, Total: 5, Processed: 3, Succeeded: 2, Skipped: 1, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 2, Skipped: 1, Cancelled: true}},
		{"NFO 写出：完成，失败明细取原因", localMetadataExportRun(services.LocalMetadataExportStatus{Completed: true, Total: 2, Processed: 2, Succeeded: 1, Failed: 1, UpdatedAt: &at,
			Failures: []services.LocalMetadataFailure{{VideoID: 9, Message: "NFO 被占用"}}}),
			TaskLastRun{Succeeded: 1, Failed: 1, Failures: []string{"NFO 被占用"}}},
		{"NFO 写出：取消", localMetadataExportRun(services.LocalMetadataExportStatus{Completed: true, Cancelled: true, Total: 4, Processed: 1, Succeeded: 1, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 1, Cancelled: true}},
		{"NFO 写出：筛选结果为空", localMetadataExportRun(services.LocalMetadataExportStatus{Completed: true, UpdatedAt: &at}),
			TaskLastRun{NothingToDo: true}},
		{"场景画面索引：跳过", sceneIndexRun(services.SceneIndexStatus{Completed: true, Total: 4, Processed: 4, Succeeded: 3, Skipped: 1, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 3, Skipped: 1}},
		{"人脸分析：中断也算这一轮结束，整轮错误进失败明细", faceAnalysisRun(services.FaceAnalysisStatus{Interrupted: true, Total: 5, Processed: 2, Succeeded: 2, LastError: "sidecar 退出", UpdatedAt: &at}),
			TaskLastRun{Succeeded: 2, Failures: []string{"sidecar 退出"}}},
		{"人脸分析：取消", faceAnalysisRun(services.FaceAnalysisStatus{Cancelled: true, Total: 5, Processed: 1, Succeeded: 1, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 1, Cancelled: true}},
		{"语义索引：跳过", semanticIndexRun(services.SemanticIndexStatus{Completed: true, Total: 3, Processed: 3, Succeeded: 2, Skipped: 1, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 2, Skipped: 1}},
		{"图片语义索引：跳过与取消", imageSemanticIndexRun(services.ImageSemanticIndexStatus{Cancelled: true, Total: 3, Processed: 2, Succeeded: 1, Skipped: 1, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 1, Skipped: 1, Cancelled: true}},
		{"图片 AI 打标：跳过", imageAITaggingRun(services.ImageAITaggingStatus{Completed: true, Total: 3, Processed: 3, Succeeded: 1, Skipped: 2, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 1, Skipped: 2}},
		{"图片 EXIF：跳过", imageEXIFRun(services.ImageEXIFBackfillStatus{Completed: true, Total: 3, Processed: 3, Succeeded: 1, Skipped: 2, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 1, Skipped: 2}},
		{"图片指纹：跳过", imagePerceptualHashRun(services.ImagePerceptualHashBackfillStatus{Completed: true, Total: 2, Processed: 2, Succeeded: 1, Skipped: 1, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 1, Skipped: 1}},
		{"图片指纹：总数为 0", imagePerceptualHashRun(services.ImagePerceptualHashBackfillStatus{Completed: true, UpdatedAt: &at}),
			TaskLastRun{NothingToDo: true}},
		{"播放代理：跳过数与失败项", playbackProxyRun(services.PlaybackProxyStatus{Completed: true, Total: 3, Processed: 3, Succeeded: 1, Skipped: 1, Failed: 1, UpdatedAt: &at,
			Results: []services.PlaybackProxyItemResult{{VideoID: 1, Name: "a.avi", Code: services.PlaybackProxyCodeAlreadyExists}, {VideoID: 2, Name: "b.avi", Code: "encode_failed", Message: "ffmpeg 退出"}}}),
			TaskLastRun{Succeeded: 1, Skipped: 1, Failed: 1, Failures: []string{"b.avi：ffmpeg 退出"}}},
		{"播放代理：取消", playbackProxyRun(services.PlaybackProxyStatus{Cancelled: true, Total: 3, Processed: 1, Succeeded: 1, UpdatedAt: &at}),
			TaskLastRun{Succeeded: 1, Cancelled: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.run.lastRun()
			if got == nil {
				t.Fatalf("结束了的一轮应有上一轮结果: %+v", tc.run)
			}
			want := tc.want
			want.FinishedAt = &at
			if want.Failures == nil {
				want.Failures = []string{}
			}
			if !reflect.DeepEqual(*got, want) {
				t.Fatalf("上一轮映射不对:\n got %+v\nwant %+v", *got, want)
			}
		})
	}
}

// 跑着的批量任务只报进度（准备阶段连进度也没有、只报准备中），不报上一轮。
func TestP3ARunningBatchReportsPreparingOrProgressButNoLastRun(t *testing.T) {
	// 准备阶段服务只置 Preparing（Running 可能还没翻）：这四项都按 running || preparing 算在跑。
	for name, run := range map[string]batchRun{
		"技术信息":   technicalBackfillRun(services.TechnicalBackfillStatus{Preparing: true}),
		"帧哈希":    frameHashRun(services.FrameHashStatus{Preparing: true}),
		"人脸分析":   faceAnalysisRun(services.FaceAnalysisStatus{Running: true, Preparing: true}),
		"场景画面索引": sceneIndexRun(services.SceneIndexStatus{Preparing: true}),
	} {
		if !run.running || !run.preparing || run.progress() != nil || run.lastRun() != nil {
			t.Fatalf("%s 准备阶段应为运行中 + 准备中、没有进度与上一轮: %+v", name, run)
		}
	}
	running := frameHashRun(services.FrameHashStatus{Running: true, Total: 10, Processed: 4, Succeeded: 3, Skipped: 1})
	if running.preparing || running.lastRun() != nil || !reflect.DeepEqual(running.progress(), &TaskProgress{Done: 4, Total: 10}) {
		t.Fatalf("运行中应只有进度: %+v", running)
	}
	if run := localMetadataExportRun(services.LocalMetadataExportStatus{Running: true, Total: 5, Processed: 2}); !run.running || run.preparing ||
		!reflect.DeepEqual(run.progress(), &TaskProgress{Done: 2, Total: 5}) || run.lastRun() != nil {
		t.Fatalf("NFO 写出运行中应只有进度: %+v", run)
	}
	if run := technicalBackfillRun(services.TechnicalBackfillStatus{}); run.running || run.lastRun() != nil {
		t.Fatalf("从没跑过时既不在跑也没有上一轮: %+v", run)
	}
}

// Preparing 只在运行中给出（等待空闲或空闲的项不报准备中），此时没有进度。
func TestP3ATaskCenterItemReportsPreparingOnlyWhileRunning(t *testing.T) {
	src := &taskCenterSources{running: map[string]bool{}, waiting: map[string]string{}}
	item := buildTaskCenterItem("technical", taskCenterKeyState{running: true, preparing: true, canStart: true, canCancel: true}, src)
	if item.State != TaskCenterStateRunning || !item.Preparing || item.Progress != nil || !reflect.DeepEqual(item.Actions, []string{TaskCenterActionCancel}) {
		t.Fatalf("准备阶段应为 running + preparing + 可取消: %+v", item)
	}
	item = buildTaskCenterItem("technical", taskCenterKeyState{running: true, canCancel: true, progress: &TaskProgress{Done: 1, Total: 3}}, src)
	if item.Preparing || item.Progress == nil {
		t.Fatalf("统计完之后不再报准备中: %+v", item)
	}
	src.waiting["technical"] = services.IdleWaitReasonUserActive
	item = buildTaskCenterItem("technical", taskCenterKeyState{preparing: true, canStart: true}, src)
	if item.State != TaskCenterStateWaitingIdle || item.Preparing {
		t.Fatalf("没在跑的项不报准备中: %+v", item)
	}
}

// 「一次分析」类与批量批准、自动规则：取消的那一轮单独标出，不再与完成混为一谈。
func TestP3ASingleOutcomeTasksSeparateCancelledFromCompleted(t *testing.T) {
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	if run := cleanupLastRun(true, false, "", &at); run == nil || run.Cancelled || run.Succeeded != 1 || run.Failed != 0 {
		t.Fatalf("清理分析完成: %+v", run)
	}
	if run := cleanupLastRun(false, true, "", &at); run == nil || !run.Cancelled || run.Succeeded != 0 || run.Failed != 0 || run.NothingToDo {
		t.Fatalf("清理分析取消: %+v", run)
	}
	if run := cleanupLastRun(false, false, "磁盘已满", &at); run == nil || run.Cancelled || run.Failed != 1 || !reflect.DeepEqual(run.Failures, []string{"磁盘已满"}) {
		t.Fatalf("清理分析出错: %+v", run)
	}
	if run := cleanupLastRun(false, false, "", &at); run != nil {
		t.Fatalf("没跑过不该有上一轮: %+v", run)
	}

	if run := collectionSuggestLastRun(services.CollectionSuggestionStatus{Cancelled: true, UpdatedAt: &at}); run == nil || !run.Cancelled || run.Succeeded != 0 {
		t.Fatalf("建议作品集取消: %+v", run)
	}
	if run := collectionSuggestLastRun(services.CollectionSuggestionStatus{Completed: true, UpdatedAt: &at}); run == nil || run.Cancelled || run.Succeeded != 1 {
		t.Fatalf("建议作品集完成: %+v", run)
	}
	if run := collectionSuggestLastRun(services.CollectionSuggestionStatus{Completed: true, LastError: "读库失败", UpdatedAt: &at}); run == nil || run.Cancelled || run.Failed != 1 {
		t.Fatalf("建议作品集出错优先: %+v", run)
	}

	review := services.ReviewApprovalState{State: "cancelled", FinishedAt: &at, Succeeded: 2, Skipped: 1, Remaining: 3}
	if run := reviewApprovalLastRun(review); run == nil || !run.Cancelled || run.Succeeded != 2 || run.Skipped != 1 || run.Remaining != 3 || len(run.Failures) != 0 {
		t.Fatalf("批量批准取消: %+v", run)
	}
	review.State = "completed"
	if run := reviewApprovalLastRun(review); run == nil || run.Cancelled {
		t.Fatalf("批量批准完成: %+v", run)
	}
	review.State = "failed"
	if run := reviewApprovalLastRun(review); run == nil || run.Cancelled || len(run.Failures) != 1 {
		t.Fatalf("批量批准中止: %+v", run)
	}
	if run := reviewApprovalLastRun(services.ReviewApprovalState{State: "idle"}); run != nil {
		t.Fatalf("没有批次时不该有上一轮: %+v", run)
	}

	if run := automationRuleLastRun(services.AutomationRuleRunStatus{Completed: true, Cancelled: true, Matched: 4, FinishedAt: &at}); !run.Cancelled || run.Succeeded != 4 {
		t.Fatalf("自动规则取消: %+v", run)
	}
	if run := automationRuleLastRun(services.AutomationRuleRunStatus{Completed: true, Matched: 4, FinishedAt: &at}); run.Cancelled {
		t.Fatalf("自动规则完成: %+v", run)
	}
}

// 以单个任务为单位的几类：最近结束的那一个是被取消的，上一轮就标已取消（不算成功也不算失败）。
func TestP3AJobTasksMarkCancelledLastRun(t *testing.T) {
	app := &App{}
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	cancelledSubtitle := app.taskCenterSubtitle(&taskCenterSources{subtitleJobs: []services.SubtitleJobItem{{Status: services.SubtitleQueueTaskStatusCancelled, FinishedAt: &at}}})
	if run := cancelledSubtitle.lastRun; run == nil || !run.Cancelled || run.Succeeded != 0 || run.Failed != 0 {
		t.Fatalf("字幕取消: %+v", run)
	}
	doneSubtitle := app.taskCenterSubtitle(&taskCenterSources{subtitleJobs: []services.SubtitleJobItem{{Status: services.SubtitleQueueTaskStatusSucceeded, FinishedAt: &at}}})
	if run := doneSubtitle.lastRun; run == nil || run.Cancelled || run.Succeeded != 1 {
		t.Fatalf("字幕完成: %+v", run)
	}

	var cancelledTask, completedTask services.EnhancementTaskView
	cancelledTask.Status, cancelledTask.FinishedAt = models.EnhancementStatusCancelled, &at
	completedTask.Status, completedTask.FinishedAt = models.EnhancementStatusCompleted, &at
	if run := app.taskCenterEnhancement(&taskCenterSources{enhancement: []services.EnhancementTaskView{cancelledTask}}).lastRun; run == nil || !run.Cancelled || run.Succeeded != 0 {
		t.Fatalf("超分取消: %+v", run)
	}
	if run := app.taskCenterEnhancement(&taskCenterSources{enhancement: []services.EnhancementTaskView{completedTask}}).lastRun; run == nil || run.Cancelled || run.Succeeded != 1 {
		t.Fatalf("超分完成: %+v", run)
	}

	edit := app.taskCenterVideoEdit(&taskCenterSources{videoEdits: []services.EditProjectSummary{{Status: "cancelled", ItemCount: 3, CompletedCount: 1, FinishedAt: &at}}})
	if run := edit.lastRun; run == nil || !run.Cancelled || run.Succeeded != 1 || run.Failed != 0 {
		t.Fatalf("视频工作台取消: %+v", run)
	}
	edit = app.taskCenterVideoEdit(&taskCenterSources{videoEdits: []services.EditProjectSummary{{Status: "partial", ItemCount: 3, CompletedCount: 2, FinishedAt: &at}}})
	if run := edit.lastRun; run == nil || run.Cancelled || run.Failed != 1 {
		t.Fatalf("视频工作台部分完成: %+v", run)
	}

	var consolidation services.CleanupConsolidationSummary
	consolidation.Status, consolidation.Total, consolidation.Completed, consolidation.FinishedAt = "cancelled", 3, 1, &at
	if run := app.taskCenterCleanupConsolidation(&taskCenterSources{consolidations: []services.CleanupConsolidationSummary{consolidation}}).lastRun; run == nil || !run.Cancelled || run.Succeeded != 1 {
		t.Fatalf("集中整理取消: %+v", run)
	}
	consolidation.Status = "completed"
	if run := app.taskCenterCleanupConsolidation(&taskCenterSources{consolidations: []services.CleanupConsolidationSummary{consolidation}}).lastRun; run == nil || run.Cancelled {
		t.Fatalf("集中整理完成: %+v", run)
	}
}

// NFO 写出与本地资料补全分成两个 key：写出的运行与上一轮只出现在 local_metadata_export 上，
// 它没有零参启动（要选筛选范围）；补全那一项不受牵连，照旧可启动。
func TestP3ATaskCenterSplitsNFOExportFromLocalMetadataBackfill(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	root := t.TempDir()
	videoPath := filepath.Join(root, "片B.mp4")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.Video{Name: "片B.mp4", Path: videoPath, Directory: root}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := app.StartLocalMetadataExport(services.LocalMetadataExportRequest{}); err != nil {
		t.Fatalf("启动 NFO 写出失败: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for app.GetLocalMetadataExportStatus().Running || len(app.GetBackgroundTasks()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("NFO 写出应很快结束: %+v %v", app.GetLocalMetadataExportStatus(), app.GetBackgroundTasks())
		}
		time.Sleep(5 * time.Millisecond)
	}

	snapshot := app.GetTaskCenterSnapshot()
	export := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskLocalMetadataExport))
	if export.State != TaskCenterStateIdle || export.LastRun == nil || export.LastRun.Succeeded != 1 || export.LastRun.Cancelled ||
		export.LastRun.NothingToDo || len(export.Actions) != 0 {
		t.Fatalf("NFO 写出的上一轮应在自己的 key 上、且没有零参启动: %+v last=%+v", export, export.LastRun)
	}
	backfill := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskLocalMetadata))
	if backfill.State != TaskCenterStateIdle || backfill.LastRun != nil || !reflect.DeepEqual(backfill.Actions, []string{TaskCenterActionStart}) {
		t.Fatalf("补全那一项不该带上写出的结果: %+v last=%+v", backfill, backfill.LastRun)
	}

	app.backgroundTasks.Begin(services.BackgroundTaskLocalMetadataExport)
	defer app.backgroundTasks.End(services.BackgroundTaskLocalMetadataExport)
	snapshot = app.GetTaskCenterSnapshot()
	if item := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskLocalMetadataExport)); item.State != TaskCenterStateRunning {
		t.Fatalf("登记表里的写出应显示运行中: %+v", item)
	}
	if item := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskLocalMetadata)); item.State != TaskCenterStateIdle {
		t.Fatalf("写出在跑时补全那一项仍是空闲: %+v", item)
	}
}

// ===== 2026-10-14：近重复指纹在跑时就给出本轮计数（片库页状态条原来在跑时就列）=====

// 跑着的近重复指纹把服务逐项累计的四个数原样放进 runCounts（含本轮新生成）；不在跑时没有，
// 这一轮的数只在上一轮里。
func TestPerceptualHashRunCountsOnlyWhileRunning(t *testing.T) {
	running := perceptualHashRun(services.PerceptualHashStatus{Running: true, Total: 10, Processed: 7, Succeeded: 2, Skipped: 4, Reused: 3, OutOfScope: 1, Failed: 1})
	if got, want := running.runCounts(), (&TaskRunCounts{Succeeded: 2, Reused: 3, Failed: 1, OutOfScope: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("运行中的计数不对: got %+v want %+v", got, want)
	}
	if running.lastRun() != nil {
		t.Fatalf("运行中不报上一轮: %+v", running.lastRun())
	}
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for name, run := range map[string]batchRun{
		"完成":   perceptualHashRun(services.PerceptualHashStatus{Completed: true, Total: 2, Processed: 2, Succeeded: 1, Reused: 1, Skipped: 1, UpdatedAt: &at}),
		"取消":   perceptualHashRun(services.PerceptualHashStatus{Completed: true, Cancelled: true, Total: 2, Processed: 1, Failed: 1, UpdatedAt: &at}),
		"从没跑过": perceptualHashRun(services.PerceptualHashStatus{}),
	} {
		if run.runCounts() != nil {
			t.Fatalf("%s 时不该有本轮计数: %+v", name, run.runCounts())
		}
	}

	src := &taskCenterSources{running: map[string]bool{}, waiting: map[string]string{}}
	counts := &TaskRunCounts{Reused: 1}
	if item := buildTaskCenterItem("phash", taskCenterKeyState{running: true, canCancel: true, progress: &TaskProgress{Done: 1, Total: 3}, runCounts: counts}, src); item.RunCounts != counts {
		t.Fatalf("运行中的项应带本轮计数: %+v", item)
	}
	src.waiting["phash"] = services.IdleWaitReasonUserActive
	if item := buildTaskCenterItem("phash", taskCenterKeyState{canStart: true, runCounts: counts}, src); item.State != TaskCenterStateWaitingIdle || item.RunCounts != nil {
		t.Fatalf("没在跑的项不带本轮计数: %+v", item)
	}
}

// steppedPauseHook 是测试用的项间检查点：第 at 里的那几次 Wait（即处理第 N 项之前）停住，把 N 发到
// paused，等 release（或这一轮被取消）再放行；其余各次直接放行。calls 只在 worker 协程里读写。
type steppedPauseHook struct {
	at      map[int]bool
	calls   int
	paused  chan int
	release chan struct{}
}

func (h *steppedPauseHook) Wait(ctx context.Context, _ func(services.TaskGateState)) error {
	h.calls++
	if !h.at[h.calls] {
		return nil
	}
	select {
	case h.paused <- h.calls:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *steppedPauseHook) Release() {}

// startPausedPerceptualHashRun 起一轮真实的近重复指纹补全，停在第 4 项之前：前三项依次是移出处理范围
// （开跑后才标失效）、已有指纹复用、处理失败（文件不在），所以此刻服务里各有 1 个，本轮新生成为 0
// （生成指纹要真解码，上面的纯函数用例覆盖它）。第 4 项也是不在的文件。
func startPausedPerceptualHashRun(t *testing.T) (*App, *steppedPauseHook) {
	t.Helper()
	setupAppTestDB(t)
	root := t.TempDir()
	seed := func(name string, onDisk bool) models.Video {
		path := filepath.Join(root, name)
		if onDisk {
			if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		video := models.Video{Name: name, Path: path, Directory: root, Duration: 100}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		return video
	}
	removed := seed("removed.mp4", true)
	cached := seed("cached.mp4", true)
	seed("missing.mp4", false)
	seed("tail.mp4", false)
	info, err := os.Stat(cached.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.VideoPerceptualHash{VideoID: cached.ID, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
		HashEarly: "0f0f0f0f0f0f0f0f", HashMiddle: "0f0f0f0f0f0f0f0f", HashLate: "0f0f0f0f0f0f0f0f", ComputedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	t.Cleanup(app.perceptualHash.StopAndWait)
	hook := &steppedPauseHook{at: map[int]bool{1: true, 4: true}, paused: make(chan int), release: make(chan struct{})}
	if _, err := app.perceptualHash.StartWithPauseHook(context.Background(), hook); err != nil {
		t.Fatalf("启动近重复指纹失败: %v", err)
	}
	waitPaused := func(want int) {
		t.Helper()
		select {
		case got := <-hook.paused:
			if got != want {
				t.Fatalf("停在第 %d 项之前，期望第 %d 项", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("等第 %d 项之前的检查点超时: %+v", want, app.perceptualHash.Status())
		}
	}
	waitPaused(1)
	if err := database.DB.Model(&removed).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	hook.release <- struct{}{}
	waitPaused(4)
	return app, hook
}

// 一轮跑到一半时任务中心的近重复指纹带着本轮计数，没有上一轮；放行跑完与中途取消后计数收起，
// 上一轮照旧给出（取消的那一轮就是取消时的计数）。
func TestPerceptualHashTaskCenterShowsRunCountsMidRun(t *testing.T) {
	assertMidRun := func(t *testing.T, app *App) {
		t.Helper()
		item := taskCenterItemByKey(t, app.GetTaskCenterSnapshot(), string(services.BackgroundTaskPerceptualHash))
		if item.State != TaskCenterStateRunning || item.LastRun != nil || !reflect.DeepEqual(item.Progress, &TaskProgress{Done: 3, Total: 4}) ||
			!reflect.DeepEqual(item.Actions, []string{TaskCenterActionCancel}) {
			t.Fatalf("跑到一半应为运行中、进度 3/4、没有上一轮: %+v", item)
		}
		if want := (&TaskRunCounts{Reused: 1, Failed: 1, OutOfScope: 1}); !reflect.DeepEqual(item.RunCounts, want) {
			t.Fatalf("跑到一半的本轮计数不对: got %+v want %+v", item.RunCounts, want)
		}
	}
	finishedItem := func(t *testing.T, app *App) TaskCenterItem {
		t.Helper()
		waitForPerceptualHashIdle(t, app)
		app.perceptualHash.StopAndWait() // 等 worker 退出（登记表随之收起），快照才是空闲
		item := taskCenterItemByKey(t, app.GetTaskCenterSnapshot(), string(services.BackgroundTaskPerceptualHash))
		if item.State != TaskCenterStateIdle || item.RunCounts != nil || item.Progress != nil || item.LastRun == nil ||
			!reflect.DeepEqual(item.Actions, []string{TaskCenterActionStart}) {
			t.Fatalf("结束后应为空闲、不带本轮计数、有上一轮: %+v", item)
		}
		return item
	}
	assertLastRun := func(t *testing.T, got *TaskLastRun, want TaskLastRun) {
		t.Helper()
		if got.FinishedAt == nil || len(got.Failures) != want.Failed {
			t.Fatalf("上一轮应有结束时间与 %d 条失败明细: %+v", want.Failed, got)
		}
		for _, failure := range got.Failures {
			if !strings.HasPrefix(failure, "missing.mp4：") && !strings.HasPrefix(failure, "tail.mp4：") {
				t.Fatalf("失败明细应是不在的那两个文件: %q", failure)
			}
		}
		trimmed := *got
		trimmed.FinishedAt, trimmed.Failures = nil, nil
		if !reflect.DeepEqual(trimmed, want) {
			t.Fatalf("上一轮不对:\n got %+v\nwant %+v", trimmed, want)
		}
	}

	t.Run("跑完", func(t *testing.T) {
		app, hook := startPausedPerceptualHashRun(t)
		assertMidRun(t, app)
		hook.release <- struct{}{}
		item := finishedItem(t, app)
		assertLastRun(t, item.LastRun, TaskLastRun{Skipped: 2, Reused: 1, OutOfScope: 1, Failed: 2})
	})

	t.Run("中途取消", func(t *testing.T) {
		app, _ := startPausedPerceptualHashRun(t)
		assertMidRun(t, app)
		if err := app.CancelPerceptualHashBackfill(); err != nil {
			t.Fatalf("取消失败: %v", err)
		}
		item := finishedItem(t, app)
		assertLastRun(t, item.LastRun, TaskLastRun{Skipped: 2, Reused: 1, OutOfScope: 1, Failed: 1, Cancelled: true})
	})
}
