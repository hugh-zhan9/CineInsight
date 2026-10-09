package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// 退出守卫（D-PC21）。
//
// Wails 的 OnBeforeClose 会拦下所有退出，包括应用自己发起的那些：恢复备份完成后数据库
// 已经关了、RelaunchApp 要让位给新实例，这时再弹「有任务在跑，确定退出吗」既读不了库，
// 也拦错了对象。所以内部发起的退出先 allowInternalQuit()，beforeClose 看到标志直接放行。
//
// 用户关窗口时（D-PC21、MEDIA-10）：字幕、超分、播放代理、浏览器下载还在跑，或字幕队列里
// 还有排队的任务，就先发 quit-confirm-required 列出这些任务并拦下关闭；用户确认后前端调
// ConfirmQuit，置位 confirmed 再退出，第二次进 beforeClose 直接放行。
//
// 状态放在包级而不是 App 字段里（本批次不给 App 加字段）。

type quitGuard struct {
	// internal 置位后不再复位：内部退出一旦发起，进程就在退出的路上。
	internal atomic.Bool
	// confirmed 是用户在 quit-confirm-required 之后确认了退出（ConfirmQuit），同样不再复位。
	confirmed atomic.Bool
}

var appQuitGuard quitGuard

// allowInternalQuit 标记接下来的退出由应用自己发起，beforeClose 不得阻止。
// 必须在调用 runtime.Quit 之前调用。
func allowInternalQuit() {
	appQuitGuard.internal.Store(true)
}

func internalQuitAllowed() bool {
	return appQuitGuard.internal.Load()
}

// internalQuitDelay 让发起退出的那次绑定调用先把结果交回前端，再退出。
const internalQuitDelay = 150 * time.Millisecond

// quitRuntime 是 runtime.Quit 的替换点，测试里换成记录调用。
var quitRuntime = runtime.Quit

// emitRuntimeEvent 是 runtime.EventsEmit 的替换点（quit-confirm-required、task-center-changed），
// 测试里换成记录调用。
var emitRuntimeEvent = runtime.EventsEmit

// quitConfirmRequiredEvent 是 beforeClose 拦下关闭时发给前端的事件，载荷为 QuitConfirmRequest。
const quitConfirmRequiredEvent = "quit-confirm-required"

// quitBlockingNameLimit 是每类任务在确认框里最多列出的名字数。
const quitBlockingNameLimit = 5

// QuitBlockingTask 是一类拦住退出的任务。Key 取后台任务 key：subtitle / enhancement / proxy /
// browser_download / cleanup_consolidation。Running / Queued 是运行中与排队中的个数（超分只知道在不在跑，Running 为 1）；
// Names 是能在内存里拿到的任务名（视频名或下载标题），最多 quitBlockingNameLimit 个。
type QuitBlockingTask struct {
	Key     string   `json:"key"`
	Running int      `json:"running"`
	Queued  int      `json:"queued"`
	Names   []string `json:"names"`
}

// QuitConfirmRequest 是 quit-confirm-required 事件的载荷。
type QuitConfirmRequest struct {
	Tasks []QuitBlockingTask `json:"tasks"`
}

// beforeClose 是 Wails OnBeforeClose 的处理函数（在 main.go 注册属接线项）。
// 返回 true 阻止关闭。
//
// 三种情况直接放行，不查任务、不发事件：应用自己发起的退出（恢复完成、RelaunchApp）、用户已
// 确认过的退出、切换后端成功后的「待重启」终态（维护围栏一直保持，再弹确认框也读不了库，
// 唯一出口就是退出）。恢复成功后的终态在发起退出前已 allowInternalQuit，归第一种。
//
// 判定只读内存里的状态（登记表、字幕队列、代理与下载队列），不读库：维护期间照样判得出来，
// 也不会因为一次慢查询把关窗口卡住。
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	if internalQuitAllowed() || appQuitGuard.confirmed.Load() {
		return false
	}
	if a.databaseSwitchService != nil && a.databaseSwitchService.RelaunchPending() {
		log.Printf("App beforeClose relaunch pending, quit allowed")
		return false
	}
	tasks := a.quitBlockingTasks()
	if len(tasks) == 0 {
		return false
	}
	keys := make([]string, 0, len(tasks))
	for _, task := range tasks {
		keys = append(keys, task.Key)
	}
	log.Printf("App beforeClose blocked by running tasks=%v", keys)
	if ctx == nil {
		ctx = a.ctx
	}
	if ctx != nil {
		emitRuntimeEvent(ctx, quitConfirmRequiredEvent, QuitConfirmRequest{Tasks: tasks})
	}
	return true
}

// quitBlockingTasks 收集拦住退出的任务，顺序固定：字幕、超分、播放代理、浏览器下载、集中整理。
// 运行与否与任务中心同一个来源（登记表），字幕另外算上队列里排队的任务。
//
// 进行中的字幕翻译（D-PC21 清单里的最后一项）不在其中：字幕服务目前没有只读的「哪些翻译在跑」
// 查询入口，这里无从判断。
func (a *App) quitBlockingTasks() []QuitBlockingTask {
	running := map[string]bool{}
	for _, key := range a.backgroundTasks.Snapshot() {
		running[key] = true
	}
	tasks := make([]QuitBlockingTask, 0, 5)

	if a.subtitleService != nil {
		queue := a.subtitleService.GetSubtitleQueueState()
		task := QuitBlockingTask{Key: string(services.BackgroundTaskSubtitle), Queued: len(queue.QueuedTasks), Names: []string{}}
		if queue.ActiveTask != nil {
			task.Running = 1
			task.Names = appendQuitName(task.Names, queue.ActiveTask.VideoName)
		} else if running[task.Key] {
			task.Running = 1
		}
		for _, queued := range queue.QueuedTasks {
			task.Names = appendQuitName(task.Names, queued.VideoName)
		}
		// 「翻译已有字幕」不进队列，单独登记；退出同样会中断它（D-PC21）。
		task.Running += len(a.subtitleService.ActiveTranslationVideoIDs())
		if task.Running+task.Queued > 0 {
			tasks = append(tasks, task)
		}
	} else if running[string(services.BackgroundTaskSubtitle)] {
		tasks = append(tasks, QuitBlockingTask{Key: string(services.BackgroundTaskSubtitle), Running: 1, Names: []string{}})
	}

	if running[string(services.BackgroundTaskEnhancement)] {
		tasks = append(tasks, QuitBlockingTask{Key: string(services.BackgroundTaskEnhancement), Running: 1, Names: []string{}})
	}

	proxy := a.playbackProxies.Status()
	if proxy.Running || running[string(services.BackgroundTaskProxy)] {
		task := QuitBlockingTask{Key: string(services.BackgroundTaskProxy), Running: 1, Queued: proxy.Queued, Names: []string{}}
		task.Names = appendQuitName(task.Names, proxy.CurrentVideoName)
		tasks = append(tasks, task)
	}

	download := QuitBlockingTask{Key: string(services.BackgroundTaskBrowserDownload), Names: []string{}}
	for _, item := range a.browserDownloads.ListTasks() {
		switch item.State {
		case browserDownloadRunning, browserDownloadImporting:
			download.Running++
		case browserDownloadQueued:
			download.Queued++
		default:
			continue
		}
		name := item.Title
		if name == "" {
			name = item.Filename
		}
		download.Names = appendQuitName(download.Names, name)
	}
	if download.Running == 0 && running[download.Key] {
		download.Running = 1
	}
	if download.Running+download.Queued > 0 {
		tasks = append(tasks, download)
	}
	if running[string(services.BackgroundTaskCleanupConsolidation)] {
		tasks = append(tasks, QuitBlockingTask{Key: string(services.BackgroundTaskCleanupConsolidation), Running: 1, Names: []string{}})
	}
	return tasks
}

func appendQuitName(names []string, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" || len(names) >= quitBlockingNameLimit {
		return names
	}
	return append(names, name)
}

// ConfirmQuit 是 quit-confirm-required 确认框上的「仍然退出」：置位后退出应用，
// 随后的 beforeClose 直接放行。与 RelaunchApp 一样稍后再退出，让这次绑定调用先返回。
func (a *App) ConfirmQuit() error {
	if a.ctx == nil {
		return errors.New("应用尚未就绪，无法退出")
	}
	appQuitGuard.confirmed.Store(true)
	log.Printf("API ConfirmQuit")
	go func(runtimeCtx context.Context) {
		time.Sleep(internalQuitDelay)
		quitRuntime(runtimeCtx)
	}(a.ctx)
	return nil
}

// relaunchExecutable 与 startRelaunchHelper 是 RelaunchApp 的替换点，测试里换掉。
var (
	relaunchExecutable  = os.Executable
	startRelaunchHelper = spawnRelaunchHelper
)

// RelaunchApp 重新拉起应用（D-PC55「立即重启」，APP-02）：切换后端写好配置之后，
// 只有新进程才会连到新后端（D-007 不热换句柄）。
//
// 从 .app 包运行时，先安排一个等本进程退出后执行 `open -n <bundle>` 的辅助进程，
// 再内部退出；不是从包运行（wails dev、go run）时只退出，并在结果里提示手动重新打开。
// 安排辅助进程失败时不退出，把错误交回前端，由用户自己决定。
func (a *App) RelaunchApp() (*services.RelaunchResult, error) {
	if a.ctx == nil {
		return nil, errors.New("应用尚未就绪，无法重启")
	}
	if a.databaseSwitchService != nil && a.databaseSwitchService.SwitchStatus().Running {
		return nil, errors.New("数据库迁移正在进行，完成后再重启")
	}
	result := &services.RelaunchResult{}
	executable, err := relaunchExecutable()
	bundle, inBundle := "", false
	if err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		bundle, inBundle = appBundlePath(executable)
	}
	if inBundle {
		if err := startRelaunchHelper(os.Getpid(), bundle); err != nil {
			log.Printf("API RelaunchApp schedule relaunch failed err=%v", err)
			return nil, fmt.Errorf("无法安排重新启动，请手动退出后重新打开应用")
		}
		result.Relaunched = true
		result.Message = "应用即将重新启动"
	} else {
		result.Message = "当前不是从应用包运行，应用将退出，请手动重新打开"
	}
	log.Printf("API RelaunchApp relaunched=%v", result.Relaunched)
	allowInternalQuit()
	go func(runtimeCtx context.Context) {
		time.Sleep(internalQuitDelay)
		quitRuntime(runtimeCtx)
	}(a.ctx)
	return result, nil
}

// appBundlePath 从可执行文件路径推出 .app 包路径：<名字>.app/Contents/MacOS/<可执行文件>。
func appBundlePath(executable string) (string, bool) {
	macOSDir := filepath.Dir(executable)
	contentsDir := filepath.Dir(macOSDir)
	bundle := filepath.Dir(contentsDir)
	if filepath.Base(macOSDir) != "MacOS" || filepath.Base(contentsDir) != "Contents" || !strings.HasSuffix(bundle, ".app") {
		return "", false
	}
	return bundle, true
}

// relaunchHelperScript 等旧进程退出后再 `open -n`：两个实例同时在跑会抢手机端、Jellyfin
// 与桥接的端口，新实例的 ApplySchema 还会与旧实例退出时的收尾写入并发。
// 进程号与包路径经位置参数传入，不拼进脚本文本。
const relaunchHelperScript = `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec /usr/bin/open -n "$2"`

func spawnRelaunchHelper(pid int, bundle string) error {
	command := exec.Command("/bin/sh", "-c", relaunchHelperScript, "cineinsight-relaunch", strconv.Itoa(pid), bundle)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
