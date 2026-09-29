package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
	"video-master/services"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// P-027 退出守卫（D-PC21 内部放行）与 RelaunchApp（D-PC55「立即重启」）。

func resetQuitGuardForTest(t *testing.T) {
	t.Helper()
	appQuitGuard.internal.Store(false)
	appQuitGuard.confirmed.Store(false)
	t.Cleanup(func() {
		appQuitGuard.internal.Store(false)
		appQuitGuard.confirmed.Store(false)
	})
}

// stubQuitRuntime 把 runtime.Quit 换成记录调用；返回的通道在每次退出时收到当时的内部放行标志。
func stubQuitRuntime(t *testing.T) <-chan bool {
	t.Helper()
	calls := make(chan bool, 4)
	previous := quitRuntime
	quitRuntime = func(context.Context) { calls <- internalQuitAllowed() }
	t.Cleanup(func() { quitRuntime = previous })
	return calls
}

func stubRelaunch(t *testing.T, executable string, helperErr error) *[]string {
	t.Helper()
	calls := []string{}
	previousExecutable, previousHelper := relaunchExecutable, startRelaunchHelper
	relaunchExecutable = func() (string, error) { return executable, nil }
	startRelaunchHelper = func(pid int, bundle string) error {
		if pid != os.Getpid() {
			t.Errorf("辅助进程应等待本进程退出，pid=%d", pid)
		}
		calls = append(calls, bundle)
		return helperErr
	}
	t.Cleanup(func() {
		relaunchExecutable, startRelaunchHelper = previousExecutable, previousHelper
	})
	return &calls
}

func waitQuit(t *testing.T, calls <-chan bool) bool {
	t.Helper()
	select {
	case allowed := <-calls:
		return allowed
	case <-time.After(3 * time.Second):
		t.Fatal("应当发起退出")
		return false
	}
}

// MEDIA-10：内部发起的退出（恢复完成、RelaunchApp）先置位放行标志，beforeClose 不拦——
// 即使这时还有会拦住用户退出的任务在跑，也不查任务、不发确认事件。
func TestMEDIA10BeforeCloseLetsInternalQuitThrough(t *testing.T) {
	resetQuitGuardForTest(t)
	events := stubRuntimeEvents(t)
	app := &App{ctx: context.Background(), backgroundTasks: services.NewBackgroundTaskRegistry()}
	if app.beforeClose(context.Background()) {
		t.Fatal("没有任务在跑时 beforeClose 不应阻止关闭")
	}
	app.backgroundTasks.Begin(services.BackgroundTaskEnhancement)
	allowInternalQuit()
	if !internalQuitAllowed() {
		t.Fatal("allowInternalQuit 之后应处于放行状态")
	}
	if app.beforeClose(context.Background()) {
		t.Fatal("内部发起的退出不得被 beforeClose 拦住")
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("内部退出不应发确认事件: %+v", got)
	}
}

// queuedBrowserDownloads 建一个下载服务并让一个任务停在排队：入队时读得到设置，之后派发时
// 读设置失败，队列就不会往下走（不起 ffmpeg）。
func queuedBrowserDownloads(t *testing.T, title string) *services.BrowserDownloadService {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	directory := t.TempDir()
	downloads := services.NewBrowserDownloadService(services.BrowserDownloadDeps{
		Settings: func() (services.BrowserDownloadSettings, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls > 1 {
				return services.BrowserDownloadSettings{}, errors.New("测试：派发时读不到设置")
			}
			return services.BrowserDownloadSettings{Directory: directory, Concurrency: 1}, nil
		},
	})
	if _, err := downloads.Enqueue(services.BrowserDownloadRequest{URL: "https://example.com/stream.m3u8", Kind: "hls", Title: title}); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	return downloads
}

// MEDIA-10：字幕、超分、播放代理、浏览器下载在跑（或下载在排队）时拦下关闭，并发
// quit-confirm-required 列出这些任务；其余后台任务（例如感知哈希）不拦。用户确认后
// ConfirmQuit 置位并退出，之后的 beforeClose 直接放行、不再发事件。
func TestMEDIA10BeforeCloseBlocksBlockingTasksUntilConfirmQuit(t *testing.T) {
	resetQuitGuardForTest(t)
	events := stubRuntimeEvents(t)
	quits := stubQuitRuntime(t)
	registry := services.NewBackgroundTaskRegistry()
	app := &App{
		ctx:              context.Background(),
		backgroundTasks:  registry,
		subtitleService:  services.NewSubtitleService(t.TempDir()),
		browserDownloads: queuedBrowserDownloads(t, "排队中的下载"),
	}

	registry.Begin(services.BackgroundTaskPerceptualHash)
	if !app.beforeClose(context.Background()) {
		t.Fatal("下载在排队时应拦下关闭")
	}
	registry.Begin(services.BackgroundTaskSubtitle)
	registry.Begin(services.BackgroundTaskEnhancement)
	registry.Begin(services.BackgroundTaskProxy)
	if !app.beforeClose(context.Background()) {
		t.Fatal("有任务在跑时应拦下关闭")
	}
	got := events()
	if len(got) != 2 || got[1].name != quitConfirmRequiredEvent {
		t.Fatalf("每次拦下都应发 quit-confirm-required: %+v", got)
	}
	request, ok := got[1].data[0].(QuitConfirmRequest)
	if !ok {
		t.Fatalf("载荷应为 QuitConfirmRequest: %#v", got[1].data)
	}
	want := []QuitBlockingTask{
		{Key: "subtitle", Running: 1, Names: []string{}},
		{Key: "enhancement", Running: 1, Names: []string{}},
		{Key: "proxy", Running: 1, Names: []string{}},
		{Key: "browser_download", Queued: 1, Names: []string{"排队中的下载"}},
	}
	if !reflect.DeepEqual(request.Tasks, want) {
		t.Fatalf("拦住退出的任务清单不对:\n got %+v\nwant %+v", request.Tasks, want)
	}

	if err := app.ConfirmQuit(); err != nil {
		t.Fatalf("确认退出失败: %v", err)
	}
	waitQuit(t, quits)
	if app.beforeClose(context.Background()) {
		t.Fatal("用户确认之后的关闭不得再被拦下")
	}
	if len(events()) != 2 {
		t.Fatalf("确认之后不应再发确认事件: %+v", events())
	}
}

// MEDIA-10：只有不拦退出的后台任务（感知哈希等）在跑时直接放行。
func TestMEDIA10BeforeCloseIgnoresNonBlockingBackgroundTasks(t *testing.T) {
	resetQuitGuardForTest(t)
	events := stubRuntimeEvents(t)
	registry := services.NewBackgroundTaskRegistry()
	app := &App{ctx: context.Background(), backgroundTasks: registry, subtitleService: services.NewSubtitleService(t.TempDir())}
	for _, key := range []services.BackgroundTaskKey{services.BackgroundTaskPerceptualHash, services.BackgroundTaskAITagging, services.BackgroundTaskBackup} {
		registry.Begin(key)
	}
	if app.beforeClose(context.Background()) {
		t.Fatal("字幕 / 超分 / 代理 / 下载之外的后台任务不应拦下关闭")
	}
	if len(events()) != 0 {
		t.Fatalf("放行时不应发确认事件: %+v", events())
	}
}

// MEDIA-10：运行时上下文还没有时 ConfirmQuit 报错，不置位。
func TestMEDIA10ConfirmQuitRequiresRuntimeContext(t *testing.T) {
	resetQuitGuardForTest(t)
	if err := (&App{}).ConfirmQuit(); err == nil {
		t.Fatal("运行时未就绪时应返回错误")
	}
	if appQuitGuard.confirmed.Load() {
		t.Fatal("没有发起退出时不应置位确认标志")
	}
}

// MEDIA-10 / APP-03 / META-08：切换后端成功后的「待重启」终态（维护围栏一直保持）下，
// beforeClose 直接放行、不查任务也不发确认事件；任务中心与待处理汇总返回中文说明而不是
// 底层的 database is in maintenance mode。
func TestMEDIA10RelaunchPendingStateQuitsDirectlyAndSummariesStayReadable(t *testing.T) {
	resetQuitGuardForTest(t)
	events := stubRuntimeEvents(t)
	dataDir := t.TempDir()
	t.Setenv("DB_BACKEND", "postgres")
	t.Setenv("SQLITE_PATH", "")
	db := openAppLiveDatabase(t, func() *gorm.DB { return dbtest.OpenRaw(t) })
	if err := db.Create(&models.Video{Name: "switch.mp4", Path: "/lib/switch.mp4", Directory: "/lib"}).Error; err != nil {
		t.Fatal(err)
	}
	app := newMaintenanceTestApp(dataDir)
	t.Cleanup(app.releaseDatabaseRestoreMode)
	if err := app.StartDatabaseSwitch("sqlite"); err != nil {
		t.Fatalf("发起切换失败: %v", err)
	}
	waitRestoreMuReleased(t, app)
	if !app.databaseSwitchService.RelaunchPending() || !database.MaintenanceActive() {
		t.Fatal("前置：切换成功后应处在待重启终态、围栏保持")
	}

	app.backgroundTasks.Begin(services.BackgroundTaskEnhancement)
	if app.beforeClose(context.Background()) {
		t.Fatal("待重启终态下 beforeClose 必须直接放行")
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("待重启终态下不应发确认事件: %+v", got)
	}

	const reason = "数据库后端已切换，请重启应用后再查看"
	snapshot := app.GetTaskCenterSnapshot()
	if len(snapshot.Items) != len(services.BackgroundTaskKeys()) || !reflect.DeepEqual(snapshot.Warnings, []string{reason}) {
		t.Fatalf("待重启时快照应照常列出全部 key，并只带中文说明: items=%d warnings=%v", len(snapshot.Items), snapshot.Warnings)
	}
	if item := taskCenterItemByKey(t, snapshot, string(services.BackgroundTaskEnhancement)); item.State != TaskCenterStateRunning {
		t.Fatalf("内存里的运行状态照样可读: %+v", item)
	}
	assertNoMaintenanceText(t, snapshot)
	if _, err := app.GetPendingWorkSummary(); err == nil || err.Error() != reason {
		t.Fatalf("待重启时待处理汇总应返回中文说明: %v", err)
	}
}

// MEDIA-10 / APP-03 / META-08：恢复成功后的终态（数据库已关、围栏保持、内部退出已发起）同样
// 直接放行，快照与汇总给出中文说明。
func TestMEDIA10RestoreTerminalStateQuitsDirectlyAndSummariesStayReadable(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端的库文件恢复")
	}
	resetQuitGuardForTest(t)
	events := stubRuntimeEvents(t)
	quits := stubQuitRuntime(t)
	dataDir := t.TempDir()
	t.Setenv("DB_BACKEND", "sqlite")
	t.Setenv("SQLITE_PATH", "")
	livePath := database.SQLitePath(dataDir)
	openAppLiveDatabase(t, func() *gorm.DB {
		opened, err := gorm.Open(sqlite.Open(database.SQLiteDSN(livePath)), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		return opened
	})
	app := newMaintenanceTestApp(dataDir)
	t.Cleanup(app.releaseDatabaseRestoreMode)
	backup, err := app.CreateDatabaseBackup()
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if err := app.RestoreDatabaseBackup(services.BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint}); err != nil {
		t.Fatalf("恢复应成功: %v", err)
	}
	waitQuit(t, quits)

	app.backgroundTasks.Begin(services.BackgroundTaskEnhancement)
	if app.beforeClose(context.Background()) {
		t.Fatal("恢复完成后的退出必须直接放行")
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("恢复完成后不应发确认事件: %+v", got)
	}
	const reason = "数据库已恢复，应用即将退出，请重新打开后再查看"
	snapshot := app.GetTaskCenterSnapshot()
	if !reflect.DeepEqual(snapshot.Warnings, []string{reason}) {
		t.Fatalf("恢复终态下快照应只带中文说明: %v", snapshot.Warnings)
	}
	assertNoMaintenanceText(t, snapshot)
	if _, err := app.GetPendingWorkSummary(); err == nil || err.Error() != reason {
		t.Fatalf("恢复终态下待处理汇总应返回中文说明: %v", err)
	}
}

func assertNoMaintenanceText(t *testing.T, snapshot TaskCenterSnapshot) {
	t.Helper()
	encoded := fmt.Sprintf("%+v", snapshot)
	if strings.Contains(encoded, database.ErrMaintenance.Error()) || strings.Contains(encoded, "database is closed") {
		t.Fatalf("快照里不应出现底层英文错误: %s", encoded)
	}
}

// APP-02：从 .app 包运行时，安排「等本进程退出后 open -n <包>」再内部退出。
func TestAPP02RelaunchAppSchedulesBundleReopenThenQuits(t *testing.T) {
	resetQuitGuardForTest(t)
	quits := stubQuitRuntime(t)
	helpers := stubRelaunch(t, "/Applications/析微影策.app/Contents/MacOS/CineInsight", nil)
	app := &App{ctx: context.Background()}

	result, err := app.RelaunchApp()
	if err != nil || result == nil || !result.Relaunched {
		t.Fatalf("包内运行应重新拉起: %#v err=%v", result, err)
	}
	if len(*helpers) != 1 || (*helpers)[0] != "/Applications/析微影策.app" {
		t.Fatalf("应对应用包执行 open -n: %v", *helpers)
	}
	if allowed := waitQuit(t, quits); !allowed {
		t.Fatal("调用 runtime.Quit 前必须先 allowInternalQuit")
	}
}

// APP-02：不是从包运行时只退出，并在结果里提示手动重新打开。
func TestAPP02RelaunchAppOutsideBundleOnlyQuitsWithManualHint(t *testing.T) {
	resetQuitGuardForTest(t)
	quits := stubQuitRuntime(t)
	helpers := stubRelaunch(t, "/tmp/wails-dev/CineInsight", nil)
	app := &App{ctx: context.Background()}

	result, err := app.RelaunchApp()
	if err != nil || result == nil || result.Relaunched || !strings.Contains(result.Message, "手动") {
		t.Fatalf("非包运行应只退出并提示手动打开: %#v err=%v", result, err)
	}
	if len(*helpers) != 0 {
		t.Fatalf("非包运行不应安排重开: %v", *helpers)
	}
	if allowed := waitQuit(t, quits); !allowed {
		t.Fatal("调用 runtime.Quit 前必须先 allowInternalQuit")
	}
}

// APP-02：安排重开失败时不退出，把错误交回前端。
func TestAPP02RelaunchAppKeepsRunningWhenRelaunchCannotBeScheduled(t *testing.T) {
	resetQuitGuardForTest(t)
	quits := stubQuitRuntime(t)
	stubRelaunch(t, "/Applications/析微影策.app/Contents/MacOS/CineInsight", errors.New("fork failed"))
	app := &App{ctx: context.Background()}

	if _, err := app.RelaunchApp(); err == nil {
		t.Fatal("安排重开失败应返回错误")
	}
	if internalQuitAllowed() {
		t.Fatal("没有发起退出时不应置位放行标志")
	}
	select {
	case <-quits:
		t.Fatal("安排重开失败时不应退出")
	case <-time.After(3 * internalQuitDelay):
	}
}

func TestAPP02AppBundlePathRecognizesOnlyMacOSBundleLayout(t *testing.T) {
	cases := map[string]string{
		"/Applications/析微影策.app/Contents/MacOS/CineInsight":             "/Applications/析微影策.app",
		"/Users/x/build/bin/CineInsight.app/Contents/MacOS/CineInsight": "/Users/x/build/bin/CineInsight.app",
		"/tmp/go-build123/exe/CineInsight":                              "",
		"/Applications/CineInsight/Contents/MacOS/CineInsight":          "",
	}
	for executable, want := range cases {
		got, ok := appBundlePath(executable)
		if got != want || ok != (want != "") {
			t.Fatalf("appBundlePath(%q)=%q,%v 期望 %q", executable, got, ok, want)
		}
	}
}
