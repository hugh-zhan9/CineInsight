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
// 状态放在包级而不是 App 字段里（本批次不给 App 加字段）。P-024 在这里补上用户确认退出
// （quitConfirmed / ConfirmQuit）与任务判定。

type quitGuard struct {
	// internal 置位后不再复位：内部退出一旦发起，进程就在退出的路上。
	internal atomic.Bool
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

// beforeClose 是 Wails OnBeforeClose 的处理函数（在 main.go 注册属接线项）。
// 返回 true 阻止关闭。
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	if internalQuitAllowed() {
		return false
	}
	// P-024 判定点：在这里查任务中心快照中 running 的 subtitle / enhancement / proxy /
	// browser_download、字幕队列里排队的任务与进行中的翻译；有任一项时发
	// quit-confirm-required 事件并返回 true，用户确认后由 ConfirmQuit 置位放行再退出。
	// 在那之前一律放行，行为与未注册 OnBeforeClose 时相同。
	return false
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
