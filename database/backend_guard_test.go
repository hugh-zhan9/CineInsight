package database

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/internal/appdata"

	"github.com/joho/godotenv"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 数据库后端回落保护（D-DBG-01..05）。

// guardInitEnv 把 Init 隔离到临时家目录（数据目录 <HOME>/.CineInsight，不碰开发机上真实的
// ~/.CineInsight），清掉选库相关的环境变量，启动缓存与全局句柄在结束时还原。返回数据目录（未创建）。
func guardInitEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	resetBackendStartupForTest(t)
	unsetEnvForTest(t, "DB_BACKEND", "PREVIOUS_BACKEND", "PG_HOST", "PG_PORT", "PG_USER", "PG_PASSWORD", "PG_DB", "PG_SSLMODE", "SQLITE_PATH")
	previous := DB
	t.Cleanup(func() {
		if DB != previous {
			_ = Close()
		}
		DB = previous
	})
	return filepath.Join(home, appdata.DirName)
}

func writeRecordForTest(t *testing.T, dataDir string, backend Backend, location string) {
	t.Helper()
	writeStateForTest(t, dataDir, BackendState{Backend: backend, Location: location, UpdatedAt: time.Now().Format(time.RFC3339)})
}

func writeStateForTest(t *testing.T, dataDir string, state BackendState) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeBackendState(dataDir, state); err != nil {
		t.Fatal(err)
	}
}

// writeArtifactForTest 在数据目录下的某个「只有数据库用起来之后才会出现」的目录里放一个文件。
func writeArtifactForTest(t *testing.T, dataDir, directory string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dataDir, directory), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, directory, "1.jpg"), []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRecordForTest(t *testing.T, dataDir string) *BackendState {
	t.Helper()
	state, err := readBackendState(dataDir)
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	return state
}

func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// D-DBG-02 判定表：有记录时只有「本次是走默认得到的 SQLite、上次是 PostgreSQL」才拦；没有记录时只有
// 「走默认得到的 SQLite、库文件不存在、数据目录里却有只有数据库用起来之后才会出现的数据」才拦。
func TestDBG02FallbackDecisionTable(t *testing.T) {
	postgres := &BackendState{Backend: BackendPostgres, Location: "me@db.local:5432/cine"}
	sqlite := &BackendState{Backend: BackendSQLite, Location: "/data/library.db"}
	const (
		record   = BackendGuardReasonPreviousPostgres
		inferred = BackendGuardReasonInferredPostgres
	)
	cases := []struct {
		name string
		in   backendGuardInput
		want string
	}{
		{"隐式 SQLite + 上次 postgres → 拦", backendGuardInput{resolved: BackendSQLite, previous: postgres}, record},
		{"隐式 SQLite + 上次 postgres（库文件在、有缓存也一样）→ 拦", backendGuardInput{resolved: BackendSQLite, previous: postgres, sqliteExists: true, postDatabaseArtifacts: true}, record},
		{"显式 sqlite + 上次 postgres → 放", backendGuardInput{resolved: BackendSQLite, explicit: true, previous: postgres}, ""},
		{"隐式 SQLite + 没有记录（全新安装）→ 放", backendGuardInput{resolved: BackendSQLite}, ""},
		{"隐式 SQLite + 上次 sqlite → 放", backendGuardInput{resolved: BackendSQLite, previous: sqlite}, ""},
		{"隐式 SQLite + 上次 sqlite（库文件没了、有缓存）→ 放", backendGuardInput{resolved: BackendSQLite, previous: sqlite, postDatabaseArtifacts: true}, ""},
		{"隐式 SQLite + 记录里后端为空 → 放", backendGuardInput{resolved: BackendSQLite, previous: &BackendState{}, postDatabaseArtifacts: true}, ""},
		{"本次 postgres（PG_HOST 推出）+ 上次 postgres → 放", backendGuardInput{resolved: BackendPostgres, previous: postgres}, ""},
		{"本次 postgres（新出现 PG_HOST）+ 上次 sqlite → 放", backendGuardInput{resolved: BackendPostgres, previous: sqlite}, ""},
		{"本次显式 postgres + 没有记录 + 有缓存 → 放", backendGuardInput{resolved: BackendPostgres, explicit: true, postDatabaseArtifacts: true}, ""},
		{"没有记录 + 库文件不存在 + 有缓存 → 按推断拦", backendGuardInput{resolved: BackendSQLite, postDatabaseArtifacts: true}, inferred},
		{"没有记录 + 库文件在 + 有缓存 → 放", backendGuardInput{resolved: BackendSQLite, sqliteExists: true, postDatabaseArtifacts: true}, ""},
		{"没有记录 + 库文件不存在 + 没有缓存 → 放", backendGuardInput{resolved: BackendSQLite}, ""},
		{"没有记录 + 显式 sqlite + 有缓存 → 放", backendGuardInput{resolved: BackendSQLite, explicit: true, postDatabaseArtifacts: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backendFallbackBlockReason(tc.in); got != tc.want {
				t.Fatalf("判定错误: got=%q want=%q", got, tc.want)
			}
		})
	}
}

// D-DBG-01：SQLite 记录写库文件路径；原子写不留临时文件，读回来字段一致。
func TestDBG01BackendStateRoundTripSQLite(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SQLITE_PATH", "")
	now := time.Date(2026, 10, 14, 9, 30, 0, 0, time.FixedZone("CST", 8*3600))
	if err := writeBackendState(dataDir, newBackendState(BackendSQLite, dataDir, now)); err != nil {
		t.Fatalf("写记录失败: %v", err)
	}
	state := readRecordForTest(t, dataDir)
	want := BackendState{Backend: BackendSQLite, Location: filepath.Join(dataDir, DefaultSQLiteFileName), UpdatedAt: "2026-10-14T09:30:00+08:00"}
	if state == nil || state.Postgres != nil || state.Backend != want.Backend || state.Location != want.Location || state.UpdatedAt != want.UpdatedAt {
		t.Fatalf("记录不对: %#v want %#v", state, want)
	}
	if names := dirEntryNames(t, dataDir); len(names) != 1 || names[0] != BackendStateFileName {
		t.Fatalf("原子写不应留下临时文件: %v", names)
	}
	if info, err := os.Stat(filepath.Join(dataDir, BackendStateFileName)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("记录应是 0600: %v err=%v", info.Mode().Perm(), err)
	}
	// 再写一次是整份替换，不是追加。
	if err := writeBackendState(dataDir, BackendState{Backend: BackendPostgres, Location: "me@db.local:5432/cine"}); err != nil {
		t.Fatal(err)
	}
	if state := readRecordForTest(t, dataDir); state.Backend != BackendPostgres || state.Location != "me@db.local:5432/cine" {
		t.Fatalf("第二次写应整份替换: %#v", state)
	}

	// 没有记录时返回 nil、不报错。
	if state := readRecordForTest(t, t.TempDir()); state != nil {
		t.Fatalf("没有记录时应返回 nil: %#v", state)
	}
}

// D-DBG-01：PostgreSQL 记录写 user@host:port/dbname，任何地方都不能出现密码。
func TestDBG01PostgresRecordOmitsPassword(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PG_HOST", "db.local")
	t.Setenv("PG_PORT", "5433")
	t.Setenv("PG_USER", "me")
	t.Setenv("PG_PASSWORD", "s3cret-pw")
	t.Setenv("PG_DB", "cine")
	recordOpenedBackend(dataDir, BackendPostgres)
	content, err := os.ReadFile(filepath.Join(dataDir, BackendStateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "s3cret-pw") {
		t.Fatalf("记录里不能有密码: %s", content)
	}
	var state BackendState
	if err := json.Unmarshal(content, &state); err != nil {
		t.Fatal(err)
	}
	if state.Backend != BackendPostgres || state.Location != "me@db.local:5433/cine" {
		t.Fatalf("PostgreSQL 记录不对: %#v", state)
	}
	// 出错页预填配置块要的连接项（D-DBG-05）：没给 PG_SSLMODE 时不记。
	if state.Postgres == nil || *state.Postgres != (BackendStatePostgres{Host: "db.local", Port: "5433", User: "me", DBName: "cine"}) {
		t.Fatalf("PostgreSQL 连接项不对: %#v", state.Postgres)
	}
	if _, err := time.Parse(time.RFC3339, state.UpdatedAt); err != nil {
		t.Fatalf("updated_at 应为 RFC3339: %q err=%v", state.UpdatedAt, err)
	}
	// 没给端口时与连接口径一致，记成 5432；显式给了 PG_SSLMODE 才记。
	t.Setenv("PG_PORT", "")
	t.Setenv("PG_SSLMODE", "require")
	next := newBackendState(BackendPostgres, dataDir, time.Now())
	if next.Location != "me@db.local:5432/cine" || next.Postgres.Port != "5432" || next.Postgres.SSLMode != "require" {
		t.Fatalf("默认端口应记成 5432、显式的 sslmode 应记下: %#v %#v", next, next.Postgres)
	}
}

// 较早写下的记录（没有 postgres 一项）照常读：上次位置照旧，连接项按不知道处理，配置块里留空。
func TestDBG01ReadsRecordWithoutPostgresFields(t *testing.T) {
	dataDir := guardInitEnv(t)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"backend":"postgres","location":"me@db.local:5432/cine","updated_at":"2026-10-14T09:30:00+08:00"}`
	if err := os.WriteFile(filepath.Join(dataDir, BackendStateFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	state := readRecordForTest(t, dataDir)
	if state.Backend != BackendPostgres || state.Location != "me@db.local:5432/cine" || state.Postgres != nil {
		t.Fatalf("较早的记录应照常读: %#v", state)
	}
	if err := Init(); !errors.Is(err, ErrBackendFallbackBlocked) {
		t.Fatalf("较早的记录同样按上次 postgres 拦: %v", err)
	}
	guard := StartupGuard()
	if guard.PreviousLocation != "me@db.local:5432/cine" || guard.ConfigSnippet != BackendConfigSnippet(nil) {
		t.Fatalf("没有连接项时配置块留空: %#v", guard)
	}
}

// D-DBG-02：上次是 PostgreSQL、这次走默认得到 SQLite → Init 在打开之前停下，数据目录里不出现库文件，
// 错误带 backend_fallback_blocked: 前缀；出错页要的信息（上次位置、读过的配置文件、推荐位置）记下来。
func TestDBG02InitBlocksImplicitSQLiteAfterPostgres(t *testing.T) {
	dataDir := guardInitEnv(t)
	writeStateForTest(t, dataDir, BackendState{
		Backend: BackendPostgres, Location: "me@db.local:5432/cine", UpdatedAt: time.Now().Format(time.RFC3339),
		Postgres: &BackendStatePostgres{Host: "db.local", Port: "5432", User: "me", DBName: "cine"},
	})
	// 数据目录下的 .env 存在但没给后端（与选库无关的内容）：列表里标为存在，判定不受影响。
	dataDirConfig := filepath.Join(dataDir, BackendConfigFileName)
	if err := os.WriteFile(dataDirConfig, []byte("# 用户自己的配置\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := DB

	err := Init()
	if err == nil {
		t.Fatal("隐式回落应被拦下")
	}
	if !errors.Is(err, ErrBackendFallbackBlocked) || !strings.HasPrefix(err.Error(), BackendFallbackBlockedReason+": ") {
		t.Fatalf("错误应带 backend_fallback_blocked: 前缀: %v", err)
	}
	if !strings.Contains(err.Error(), "me@db.local:5432/cine") {
		t.Fatalf("错误应写明上次的库位置: %v", err)
	}
	if DB != previous {
		t.Fatal("被拦下时不应换掉数据库句柄")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, DefaultSQLiteFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("被拦下时不应创建 SQLite 库文件: %v", statErr)
	}
	names := dirEntryNames(t, dataDir)
	if len(names) != 2 || names[0] != BackendConfigFileName || names[1] != BackendStateFileName {
		t.Fatalf("数据目录里不应多出任何文件: %v", names)
	}
	if state := readRecordForTest(t, dataDir); state.Backend != BackendPostgres {
		t.Fatalf("被拦下时记录不应改动: %#v", state)
	}

	guard := StartupGuard()
	if !guard.Blocked || guard.Reason != BackendGuardReasonPreviousPostgres || guard.PreviousLocation != "me@db.local:5432/cine" ||
		guard.DataDirConfig != dataDirConfig || guard.PGHostEmptyInLaunchEnv {
		t.Fatalf("回落保护结果不对: %#v", guard)
	}
	wantSnippet := "DB_BACKEND=postgres\nPREVIOUS_BACKEND=sqlite\nPG_HOST=db.local\nPG_PORT=5432\nPG_USER=me\nPG_PASSWORD=（在这里填密码）\nPG_DB=cine\n"
	if guard.ConfigSnippet != wantSnippet {
		t.Fatalf("配置块应按记录预填:\n%s\nwant\n%s", guard.ConfigSnippet, wantSnippet)
	}
	wantPaths := envConfigPaths(dataDir)
	if len(guard.ConfigFiles) != len(wantPaths) {
		t.Fatalf("应按 envConfigPaths 列出读过的配置文件: %#v want %v", guard.ConfigFiles, wantPaths)
	}
	if guard.ConfigFiles[0] != (BackendConfigFile{Path: dataDirConfig, Exists: true}) {
		t.Fatalf("第一项应是数据目录下的 .env 且标为存在: %#v", guard.ConfigFiles[0])
	}
	for _, file := range guard.ConfigFiles {
		if !filepath.IsAbs(file.Path) {
			t.Fatalf("配置文件位置应是绝对路径: %#v", file)
		}
	}

	// 同一进程再调一次（沿用启动结果）仍是拦下，仍不建库文件。
	if err := Init(); !errors.Is(err, ErrBackendFallbackBlocked) {
		t.Fatalf("再次调用应沿用拦下的结果: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, DefaultSQLiteFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("再次调用也不应创建 SQLite 库文件: %v", statErr)
	}
}

// D-DBG-02：DB_BACKEND=sqlite 显式给出（进程环境，或应用内切换写下的数据目录 .env）时放行，打开后记录改成 sqlite。
func TestDBG02InitAllowsExplicitSQLiteAndUpdatesRecord(t *testing.T) {
	t.Run("进程环境", func(t *testing.T) {
		dataDir := guardInitEnv(t)
		writeRecordForTest(t, dataDir, BackendPostgres, "me@db.local:5432/cine")
		t.Setenv("DB_BACKEND", "sqlite")
		if err := Init(); err != nil {
			t.Fatalf("显式 sqlite 应放行: %v", err)
		}
		if state := readRecordForTest(t, dataDir); state.Backend != BackendSQLite || state.Location != filepath.Join(dataDir, DefaultSQLiteFileName) {
			t.Fatalf("打开后记录应改成 sqlite: %#v", state)
		}
		if StartupGuard().Blocked {
			t.Fatal("放行时不应标为拦下")
		}
	})
	t.Run("应用内切换写下的配置", func(t *testing.T) {
		dataDir := guardInitEnv(t)
		writeRecordForTest(t, dataDir, BackendPostgres, "me@db.local:5432/cine")
		if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte("DB_BACKEND=sqlite\nPREVIOUS_BACKEND=postgres\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Init(); err != nil {
			t.Fatalf("切换写下的显式 sqlite 应放行: %v", err)
		}
		if state := readRecordForTest(t, dataDir); state.Backend != BackendSQLite {
			t.Fatalf("打开后记录应改成 sqlite: %#v", state)
		}
	})
}

// D-DBG-02：没有记录（新装、旧版本升级后的第一次）放行，打开后写下记录。
func TestDBG02InitAllowsWithoutRecordAndWritesIt(t *testing.T) {
	dataDir := guardInitEnv(t)
	if err := Init(); err != nil {
		t.Fatalf("没有记录时应放行: %v", err)
	}
	state := readRecordForTest(t, dataDir)
	if state == nil || state.Backend != BackendSQLite || state.Location != filepath.Join(dataDir, DefaultSQLiteFileName) {
		t.Fatalf("打开后应写下记录: %#v", state)
	}
	if _, err := time.Parse(time.RFC3339, state.UpdatedAt); err != nil {
		t.Fatalf("updated_at 应为 RFC3339: %q", state.UpdatedAt)
	}
}

// D-DBG-02：上次就是 sqlite 时放行。
func TestDBG02InitAllowsWhenPreviousWasSQLite(t *testing.T) {
	dataDir := guardInitEnv(t)
	writeRecordForTest(t, dataDir, BackendSQLite, filepath.Join(dataDir, DefaultSQLiteFileName))
	if err := Init(); err != nil {
		t.Fatalf("上次是 sqlite 时应放行: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, DefaultSQLiteFileName)); err != nil {
		t.Fatalf("应打开 SQLite 库: %v", err)
	}
}

// D-DBG-01：记录写不进去（同名的是个目录）只记日志，启动照常成功；读不出的记录按没有记录处理。
func TestDBG01RecordWriteFailureDoesNotFailStartup(t *testing.T) {
	dataDir := guardInitEnv(t)
	blocker := filepath.Join(dataDir, BackendStateFileName)
	if err := os.MkdirAll(filepath.Join(blocker, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(); err != nil {
		t.Fatalf("记录写失败不应影响启动: %v", err)
	}
	if DB == nil {
		t.Fatal("启动应发布数据库句柄")
	}
	if info, err := os.Stat(blocker); err != nil || !info.IsDir() {
		t.Fatalf("占位目录应原样留着: %v", err)
	}
	for _, name := range dirEntryNames(t, dataDir) {
		if strings.Contains(name, ".tmp-") {
			t.Fatalf("写失败不应留下临时文件: %s", name)
		}
	}
}

// 恢复备份后的重连（再次调用 Init）沿用启动时的结果，不再判定：启动之后才写成 postgres 的记录、
// 进程环境里没了 DB_BACKEND，都不会让重连被拦。
func TestDBG02ReconnectDoesNotRecheckGuard(t *testing.T) {
	dataDir := guardInitEnv(t)
	t.Setenv("DB_BACKEND", "sqlite")
	if err := Init(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	writeRecordForTest(t, dataDir, BackendPostgres, "me@db.local:5432/cine")
	if err := os.Unsetenv("DB_BACKEND"); err != nil {
		t.Fatal(err)
	}
	if err := Init(); err != nil {
		t.Fatalf("重连不应再判定回落保护: %v", err)
	}
	if StartupGuard().Blocked {
		t.Fatal("重连不应标为拦下")
	}
	if state := readRecordForTest(t, dataDir); state.Backend != BackendSQLite {
		t.Fatalf("重连成功后记录照常改写: %#v", state)
	}
}

// D-DBG-04「这次改用本机 SQLite」：没拦下时拒绝、什么都不写；拦下时只把记录改成 sqlite（不写 .env、
// 不打开库），之后重新启动按「上次是 sqlite」放行。
func TestDBG04AcceptSQLiteAfterFallbackBlock(t *testing.T) {
	t.Run("没拦下时拒绝", func(t *testing.T) {
		dataDir := guardInitEnv(t)
		if err := AcceptSQLiteAfterFallbackBlock(); err == nil {
			t.Fatal("没拦下时应拒绝")
		}
		if _, err := os.Stat(filepath.Join(dataDir, BackendStateFileName)); !os.IsNotExist(err) {
			t.Fatalf("拒绝时不应写记录: %v", err)
		}
		// 正常启动之后同样拒绝。
		if err := Init(); err != nil {
			t.Fatal(err)
		}
		if err := AcceptSQLiteAfterFallbackBlock(); err == nil {
			t.Fatal("正常启动之后应拒绝")
		}
	})
	t.Run("拦下后改记录再重新启动", func(t *testing.T) {
		dataDir := guardInitEnv(t)
		writeRecordForTest(t, dataDir, BackendPostgres, "me@db.local:5432/cine")
		if err := Init(); !errors.Is(err, ErrBackendFallbackBlocked) {
			t.Fatalf("应先被拦下: %v", err)
		}
		if err := AcceptSQLiteAfterFallbackBlock(); err != nil {
			t.Fatalf("拦下后应可改用本机 SQLite: %v", err)
		}
		state := readRecordForTest(t, dataDir)
		if state.Backend != BackendSQLite || state.Location != filepath.Join(dataDir, DefaultSQLiteFileName) {
			t.Fatalf("记录应改成 sqlite: %#v", state)
		}
		if _, err := os.Stat(filepath.Join(dataDir, BackendConfigFileName)); !os.IsNotExist(err) {
			t.Fatalf("不应写任何 .env: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, DefaultSQLiteFileName)); !os.IsNotExist(err) {
			t.Fatalf("改记录本身不应打开库: %v", err)
		}

		// 模拟重启：新进程的启动缓存是空的。
		resetBackendStartupForTest(t)
		if err := Init(); err != nil {
			t.Fatalf("改用本机 SQLite 之后重新启动应放行: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, DefaultSQLiteFileName)); err != nil {
			t.Fatalf("应打开 SQLite 库: %v", err)
		}
	})
}

// D-DBG-02 首次运行缺口：只认那几个目录、且里面至少有一项；日志、用户的 .env、记录、空目录都不算。
func TestDBG02PostDatabaseArtifactsDetection(t *testing.T) {
	dataDir := t.TempDir()
	if hasPostDatabaseArtifacts(dataDir) {
		t.Fatal("空的数据目录不算")
	}
	if hasPostDatabaseArtifacts(filepath.Join(dataDir, "missing")) {
		t.Fatal("不存在的数据目录不算")
	}
	for _, name := range []string{"app.log", BackendConfigFileName, BackendStateFileName} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range postDatabaseArtifactDirs {
		if err := os.MkdirAll(filepath.Join(dataDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if hasPostDatabaseArtifacts(dataDir) {
		t.Fatal("日志、.env、记录与空目录都不算")
	}
	for _, name := range postDatabaseArtifactDirs {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeArtifactForTest(t, dir, name)
			if !hasPostDatabaseArtifacts(dir) {
				t.Fatalf("%s 里有东西应算", name)
			}
		})
	}
}

// D-DBG-02 首次运行缺口：全新安装（数据目录还没有，或只有 database.Init 之前就可能出现的东西）不拦，打开后写下记录。
func TestDBG02FirstRunFreshInstallIsNotBlocked(t *testing.T) {
	t.Run("数据目录还没有", func(t *testing.T) {
		dataDir := guardInitEnv(t)
		if err := Init(); err != nil {
			t.Fatalf("全新安装不应被拦: %v", err)
		}
		if state := readRecordForTest(t, dataDir); state == nil || state.Backend != BackendSQLite {
			t.Fatalf("打开后应写下记录: %#v", state)
		}
	})
	t.Run("只有空目录、日志与用户的 .env", func(t *testing.T) {
		dataDir := guardInitEnv(t)
		for _, name := range postDatabaseArtifactDirs {
			if err := os.MkdirAll(filepath.Join(dataDir, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dataDir, "app.log"), []byte("log"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte("# 用户自己的配置\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Init(); err != nil {
			t.Fatalf("没有只在数据库用起来之后才出现的数据时不应被拦: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, DefaultSQLiteFileName)); err != nil {
			t.Fatalf("应建出 SQLite 库: %v", err)
		}
	})
}

// D-DBG-02 首次运行缺口：没有记录、本机库文件不存在、数据目录里却有缩略图等数据 → 按推断拦下，不建库文件；
// 出错页说不出上次的位置，配置块留空；「这次改用本机 SQLite」之后重新启动放行。
func TestDBG02FirstRunBlocksWhenDataExistsWithoutLibrary(t *testing.T) {
	dataDir := guardInitEnv(t)
	writeArtifactForTest(t, dataDir, "thumbnails")
	err := Init()
	if !errors.Is(err, ErrBackendFallbackBlocked) || !strings.HasPrefix(err.Error(), BackendFallbackBlockedReason+": ") {
		t.Fatalf("应按推断拦下: %v", err)
	}
	if !strings.Contains(err.Error(), "看起来之前用的是 PostgreSQL") {
		t.Fatalf("推断拦下的说明不对: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, DefaultSQLiteFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("被拦下时不应创建 SQLite 库文件: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, BackendStateFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("被拦下时不应写记录: %v", statErr)
	}
	guard := StartupGuard()
	if !guard.Blocked || guard.Reason != BackendGuardReasonInferredPostgres || guard.PreviousLocation != "" || guard.ConfigSnippet != BackendConfigSnippet(nil) {
		t.Fatalf("推断拦下的结果不对: %#v", guard)
	}

	if err := AcceptSQLiteAfterFallbackBlock(); err != nil {
		t.Fatalf("推断拦下后同样可以改用本机 SQLite: %v", err)
	}
	resetBackendStartupForTest(t)
	if err := Init(); err != nil {
		t.Fatalf("改用本机 SQLite 之后（记录是 sqlite）重新启动应放行: %v", err)
	}
}

// D-DBG-02 首次运行缺口：库文件在（之前就是这个本机库），或显式选了 sqlite，都不拦。
func TestDBG02FirstRunNotBlockedWhenLibraryExistsOrExplicit(t *testing.T) {
	t.Run("库文件在", func(t *testing.T) {
		dataDir := guardInitEnv(t)
		writeArtifactForTest(t, dataDir, "image-thumbnails")
		// 0 字节的文件就是一个空的 SQLite 库。
		if err := os.WriteFile(filepath.Join(dataDir, DefaultSQLiteFileName), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Init(); err != nil {
			t.Fatalf("库文件在时不应被拦: %v", err)
		}
	})
	t.Run("显式 sqlite", func(t *testing.T) {
		dataDir := guardInitEnv(t)
		writeArtifactForTest(t, dataDir, "media-details")
		t.Setenv("DB_BACKEND", "sqlite")
		if err := Init(); err != nil {
			t.Fatalf("显式 sqlite 不应被拦: %v", err)
		}
		if state := readRecordForTest(t, dataDir); state.Backend != BackendSQLite {
			t.Fatalf("打开后应写下记录: %#v", state)
		}
	})
}

// D-DBG-03：启动环境里有空的 PG_HOST（加载配置文件之前就在）时，出错页要能说清是它挡住了配置文件。
func TestDBG03ReportsEmptyPGHostInLaunchEnv(t *testing.T) {
	dataDir := guardInitEnv(t)
	writeRecordForTest(t, dataDir, BackendPostgres, "me@db.local:5432/cine")
	t.Setenv("PG_HOST", " ")
	if err := Init(); !errors.Is(err, ErrBackendFallbackBlocked) {
		t.Fatalf("空的 PG_HOST 走的是默认 SQLite，应被拦: %v", err)
	}
	if !StartupGuard().PGHostEmptyInLaunchEnv {
		t.Fatal("应报告启动环境里的 PG_HOST 是空值")
	}
}

// D-DBG-05：配置块是应用内切换写下的同一格式（带 PREVIOUS_BACKEND，启动时整份采用），按记录预填，密码永远是占位。
func TestDBG05ConfigSnippetPrefillsFromRecord(t *testing.T) {
	full := BackendConfigSnippet(&BackendStatePostgres{Host: "db.local", Port: "5433", User: "me", DBName: "cine", SSLMode: "require"})
	wantFull := "DB_BACKEND=postgres\nPREVIOUS_BACKEND=sqlite\nPG_HOST=db.local\nPG_PORT=5433\nPG_USER=me\nPG_PASSWORD=（在这里填密码）\nPG_DB=cine\nPG_SSLMODE=require\n"
	if full != wantFull {
		t.Fatalf("预填的配置块不对:\n%s\nwant\n%s", full, wantFull)
	}
	empty := BackendConfigSnippet(nil)
	wantEmpty := "DB_BACKEND=postgres\nPREVIOUS_BACKEND=sqlite\nPG_HOST=\nPG_PORT=\nPG_USER=\nPG_PASSWORD=（在这里填密码）\nPG_DB=\n"
	if empty != wantEmpty {
		t.Fatalf("不知道连接项时应留空、不写 PG_SSLMODE:\n%s", empty)
	}
	values, err := godotenv.Unmarshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if !BackendConfigAdoptable(values) || values[BackendConfigKey] != "postgres" {
		t.Fatalf("配置块必须是启动时会采用的格式: %v", values)
	}
}

// D-DBG-03 + D-DBG-05 的出口：被拦下后把出错页给的配置块（填上密码）存成数据目录下的 .env，下次启动不再被拦，
// 按 PostgreSQL 去连（测试里连一个没人监听的端口，得到的是连接错误而不是拦截），也不建 SQLite 库文件。
func TestDBG05SnippetSavedAsDataDirConfigUnblocksNextStart(t *testing.T) {
	dataDir := guardInitEnv(t)
	writeStateForTest(t, dataDir, BackendState{
		Backend: BackendPostgres, Location: "me@127.0.0.1:1/cine", UpdatedAt: time.Now().Format(time.RFC3339),
		Postgres: &BackendStatePostgres{Host: "127.0.0.1", Port: "1", User: "me", DBName: "cine"},
	})
	if err := Init(); !errors.Is(err, ErrBackendFallbackBlocked) {
		t.Fatalf("应先被拦下: %v", err)
	}
	snippet := strings.Replace(StartupGuard().ConfigSnippet, configSnippetPasswordPlaceholder, "s3cret", 1)
	if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte(snippet), 0o600); err != nil {
		t.Fatal(err)
	}

	// 模拟「重新打开」：新进程的启动缓存是空的。
	resetBackendStartupForTest(t)
	err := Init()
	if err == nil {
		t.Fatal("测试端口上没有 PostgreSQL，应得到连接错误")
	}
	if errors.Is(err, ErrBackendFallbackBlocked) || StartupGuard().Blocked {
		t.Fatalf("存好配置块之后不应再被拦: %v", err)
	}
	if ActiveBackend() != BackendPostgres || os.Getenv("PG_HOST") != "127.0.0.1" || os.Getenv("PG_PASSWORD") != "s3cret" {
		t.Fatalf("应按配置块去连 PostgreSQL: backend=%s PG_HOST=%q", ActiveBackend(), os.Getenv("PG_HOST"))
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, DefaultSQLiteFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("不应创建 SQLite 库文件: %v", statErr)
	}
	if state := readRecordForTest(t, dataDir); state.Backend != BackendPostgres || state.Location != "me@127.0.0.1:1/cine" {
		t.Fatalf("连接失败时记录不应改动: %#v", state)
	}
}

// D-DBG-01：ApplySchema 失败（库打开了、建表失败）时记录不动——只有库真正可用之后才记。
func TestDBG01FailedApplySchemaLeavesRecordUnchanged(t *testing.T) {
	dataDir := guardInitEnv(t)
	writeRecordForTest(t, dataDir, BackendPostgres, "me@db.local:5432/cine")
	before, err := os.ReadFile(filepath.Join(dataDir, BackendStateFileName))
	if err != nil {
		t.Fatal(err)
	}
	// 一张缺列的 videos 表：ApplySchema 开头清理重复视频时查 path / deleted_at 就失败。
	broken, err := gorm.Open(sqlite.Open(filepath.Join(dataDir, DefaultSQLiteFileName)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Exec("CREATE TABLE videos (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := broken.DB(); err == nil {
		_ = sqlDB.Close()
	}
	t.Setenv("DB_BACKEND", "sqlite")
	if err := Init(); err == nil {
		t.Fatal("建表失败时 Init 应返回错误")
	}
	after, err := os.ReadFile(filepath.Join(dataDir, BackendStateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("ApplySchema 失败时记录不应改动:\nbefore %s\nafter  %s", before, after)
	}
}
