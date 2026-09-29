package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// P-027 退出守卫（D-PC21 内部放行）与 RelaunchApp（D-PC55「立即重启」）。

func resetQuitGuardForTest(t *testing.T) {
	t.Helper()
	appQuitGuard.internal.Store(false)
	t.Cleanup(func() { appQuitGuard.internal.Store(false) })
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

// MEDIA-10：内部发起的退出（恢复完成、RelaunchApp）先置位放行标志，beforeClose 不拦。
func TestMEDIA10BeforeCloseLetsInternalQuitThrough(t *testing.T) {
	resetQuitGuardForTest(t)
	app := &App{}
	if app.beforeClose(context.Background()) {
		t.Fatal("没有任务判定（P-024 前）时 beforeClose 不应阻止关闭")
	}
	allowInternalQuit()
	if !internalQuitAllowed() {
		t.Fatal("allowInternalQuit 之后应处于放行状态")
	}
	if app.beforeClose(context.Background()) {
		t.Fatal("内部发起的退出不得被 beforeClose 拦住")
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
