package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/appdata"
)

// D-DBG-04：数据库后端回落保护的两个绑定。记录本身的读写与「拦下才可改」在 database 包里测
// （TestDBG04AcceptSQLiteAfterFallbackBlock），这里测绑定的接线：拒绝时不重启，记下之后走 RelaunchApp。

// 本测试进程从未启动数据库，回落保护的结果是空的：真实的 database 判定拒绝，不安排重开、不退出。
func TestDBG04AcceptSQLiteRefusesWhenStartupNotBlocked(t *testing.T) {
	if database.StartupGuard().Blocked {
		t.Fatal("前置条件：本测试进程不应处在被拦下的状态")
	}
	resetQuitGuardForTest(t)
	quits := stubQuitRuntime(t)
	helpers := stubRelaunch(t, "/Applications/析微影策.app/Contents/MacOS/CineInsight", nil)
	app := &App{ctx: context.Background()}

	result, err := app.AcceptSQLiteAfterFallbackBlock()
	if err == nil || result != nil {
		t.Fatalf("没拦下时应拒绝: %#v err=%v", result, err)
	}
	if len(*helpers) != 0 || internalQuitAllowed() {
		t.Fatalf("拒绝时不应安排重开或放行退出: helpers=%v", *helpers)
	}
	select {
	case <-quits:
		t.Fatal("拒绝时不应退出")
	case <-time.After(3 * internalQuitDelay):
	}
}

// 记录先写好，再走现有的 RelaunchApp（从 .app 包运行时安排 open -n 再内部退出）。
func TestDBG04AcceptSQLiteRecordsThenRelaunches(t *testing.T) {
	resetQuitGuardForTest(t)
	quits := stubQuitRuntime(t)
	helpers := stubRelaunch(t, "/Applications/析微影策.app/Contents/MacOS/CineInsight", nil)
	recorded := 0
	previous := acceptSQLiteAfterFallbackBlock
	acceptSQLiteAfterFallbackBlock = func() error {
		if len(*helpers) != 0 {
			t.Error("记录应在安排重开之前写好")
		}
		recorded++
		return nil
	}
	t.Cleanup(func() { acceptSQLiteAfterFallbackBlock = previous })
	app := &App{ctx: context.Background()}

	result, err := app.AcceptSQLiteAfterFallbackBlock()
	if err != nil || result == nil || !result.Relaunched {
		t.Fatalf("记下之后应重新拉起: %#v err=%v", result, err)
	}
	if recorded != 1 || len(*helpers) != 1 || (*helpers)[0] != "/Applications/析微影策.app" {
		t.Fatalf("应写一次记录并对应用包执行 open -n: recorded=%d helpers=%v", recorded, *helpers)
	}
	if allowed := waitQuit(t, quits); !allowed {
		t.Fatal("调用 runtime.Quit 前必须先 allowInternalQuit")
	}
}

// 记录写不进去时不重启：重启之后还会被拦下，不如把错误交回出错页。
func TestDBG04AcceptSQLiteDoesNotRelaunchWhenRecordFails(t *testing.T) {
	resetQuitGuardForTest(t)
	quits := stubQuitRuntime(t)
	helpers := stubRelaunch(t, "/Applications/析微影策.app/Contents/MacOS/CineInsight", nil)
	previous := acceptSQLiteAfterFallbackBlock
	acceptSQLiteAfterFallbackBlock = func() error { return errors.New("disk full") }
	t.Cleanup(func() { acceptSQLiteAfterFallbackBlock = previous })
	app := &App{ctx: context.Background()}

	if _, err := app.AcceptSQLiteAfterFallbackBlock(); err == nil {
		t.Fatal("记录写失败应返回错误")
	}
	if len(*helpers) != 0 || internalQuitAllowed() {
		t.Fatalf("记录写失败时不应重启: helpers=%v", *helpers)
	}
	select {
	case <-quits:
		t.Fatal("记录写失败时不应退出")
	case <-time.After(3 * internalQuitDelay):
	}
}

// 没拦下时绑定返回 blocked=false，config_files 是空数组而不是 null（前端直接遍历）。
func TestDBG04GetStartupBackendGuardDefaultsToNotBlocked(t *testing.T) {
	encoded, err := json.Marshal((&App{}).GetStartupBackendGuard())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"blocked":false,"reason":"","previous_location":"","config_files":[],"data_dir_config":"","data_dir_config_status":"","config_snippet":"","pg_host_empty_in_launch_env":false}`
	if string(encoded) != want {
		t.Fatalf("未拦下时的结果不对: %s want %s", encoded, want)
	}
}

// D-DBG-02 首次运行缺口的前提：database.Init 之前（NewApp）不会建出任何「只有数据库用起来之后才会出现」的目录，
// 否则全新安装第一次启动就会被当成之前用过 PostgreSQL 而拦下。
func TestDBG02NewAppCreatesNoPostDatabaseArtifacts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = NewApp()
	dataDir := filepath.Join(home, appdata.DirName)
	for _, name := range database.PostDatabaseArtifactDirs() {
		if _, err := os.Stat(filepath.Join(dataDir, name)); !os.IsNotExist(err) {
			t.Fatalf("NewApp 不应建出 %s: %v", name, err)
		}
	}
}
