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
)

// 数据库后端回落保护（D-DBG-01..04）。

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
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeBackendState(dataDir, backend, location, time.Now()); err != nil {
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

// D-DBG-02 判定表：只有「本次是走默认得到的 SQLite、上次是 PostgreSQL」才拦。
func TestDBG02FallbackDecisionTable(t *testing.T) {
	postgres := &BackendState{Backend: BackendPostgres, Location: "me@db.local:5432/cine"}
	sqlite := &BackendState{Backend: BackendSQLite, Location: "/data/library.db"}
	cases := []struct {
		name     string
		resolved Backend
		explicit bool
		previous *BackendState
		want     bool
	}{
		{"隐式 SQLite + 上次 postgres → 拦", BackendSQLite, false, postgres, true},
		{"显式 sqlite + 上次 postgres → 放", BackendSQLite, true, postgres, false},
		{"隐式 SQLite + 没有记录 → 放", BackendSQLite, false, nil, false},
		{"隐式 SQLite + 上次 sqlite → 放", BackendSQLite, false, sqlite, false},
		{"隐式 SQLite + 记录里后端为空 → 放", BackendSQLite, false, &BackendState{}, false},
		{"本次 postgres（PG_HOST 推出）+ 上次 postgres → 放", BackendPostgres, false, postgres, false},
		{"本次 postgres（新出现 PG_HOST）+ 上次 sqlite → 放", BackendPostgres, false, sqlite, false},
		{"本次显式 postgres + 没有记录 → 放", BackendPostgres, true, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldBlockBackendFallback(tc.resolved, tc.explicit, tc.previous); got != tc.want {
				t.Fatalf("判定错误: got=%v want=%v", got, tc.want)
			}
		})
	}
}

// D-DBG-01：SQLite 记录写库文件路径；原子写不留临时文件，读回来字段一致。
func TestDBG01BackendStateRoundTripSQLite(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SQLITE_PATH", "")
	now := time.Date(2026, 10, 14, 9, 30, 0, 0, time.FixedZone("CST", 8*3600))
	if err := writeBackendState(dataDir, BackendSQLite, backendLocation(BackendSQLite, dataDir), now); err != nil {
		t.Fatalf("写记录失败: %v", err)
	}
	state := readRecordForTest(t, dataDir)
	want := BackendState{Backend: BackendSQLite, Location: filepath.Join(dataDir, DefaultSQLiteFileName), UpdatedAt: "2026-10-14T09:30:00+08:00"}
	if state == nil || *state != want {
		t.Fatalf("记录不对: %#v want %#v", state, want)
	}
	if names := dirEntryNames(t, dataDir); len(names) != 1 || names[0] != BackendStateFileName {
		t.Fatalf("原子写不应留下临时文件: %v", names)
	}
	// 再写一次是整份替换，不是追加。
	if err := writeBackendState(dataDir, BackendPostgres, "me@db.local:5432/cine", now); err != nil {
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
	if _, err := time.Parse(time.RFC3339, state.UpdatedAt); err != nil {
		t.Fatalf("updated_at 应为 RFC3339: %q err=%v", state.UpdatedAt, err)
	}
	// 没给端口时与连接口径一致，记成 5432。
	t.Setenv("PG_PORT", "")
	if got := backendLocation(BackendPostgres, dataDir); got != "me@db.local:5432/cine" {
		t.Fatalf("默认端口应记成 5432: %s", got)
	}
}

// D-DBG-02：上次是 PostgreSQL、这次走默认得到 SQLite → Init 在打开之前停下，数据目录里不出现库文件，
// 错误带 backend_fallback_blocked: 前缀；出错页要的信息（上次位置、读过的配置文件、推荐位置）记下来。
func TestDBG02InitBlocksImplicitSQLiteAfterPostgres(t *testing.T) {
	dataDir := guardInitEnv(t)
	writeRecordForTest(t, dataDir, BackendPostgres, "me@db.local:5432/cine")
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
	if !guard.Blocked || guard.PreviousLocation != "me@db.local:5432/cine" || guard.DataDirConfig != dataDirConfig {
		t.Fatalf("回落保护结果不对: %#v", guard)
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

// D-DBG-05：数据目录下的 .env 不是本版本写出的格式（缺 PREVIOUS_BACKEND）时，只忽略 DB_BACKEND 与
// PREVIOUS_BACKEND 两个键，其余键（用户自己写的 PG_* 等）照常加载，选库仍按现有规则。
func TestDBG05NonAdoptableDataDirConfigLoadsEverythingButBackendKeys(t *testing.T) {
	const pgLines = "PG_HOST=db.local\nPG_PORT=5433\nPG_USER=me\nPG_PASSWORD=pw\nPG_DB=cine\nPG_SSLMODE=require\n"
	cases := []struct {
		name    string
		content string
		want    Backend
		pgHost  string
	}{
		{"只有 PG_* → postgres", pgLines, BackendPostgres, "db.local"},
		{"DB_BACKEND=sqlite + PG_* → postgres（DB_BACKEND 被忽略）", "DB_BACKEND=sqlite\n" + pgLines, BackendPostgres, "db.local"},
		{"DB_BACKEND=postgres、没有 PG_* → sqlite（与以前整份不采用时一样）", "DB_BACKEND=postgres\n", BackendSQLite, ""},
		{"PREVIOUS_BACKEND 为空值 + PG_* → postgres", "PREVIOUS_BACKEND=\n" + pgLines, BackendPostgres, "db.local"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := guardInitEnv(t)
			if err := os.MkdirAll(dataDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			backend, err := resolveStartupBackend(dataDir)
			if err != nil || backend != tc.want {
				t.Fatalf("解析结果不对: backend=%s err=%v want %s", backend, err, tc.want)
			}
			for _, key := range []string{BackendConfigKey, PreviousBackendConfigKey} {
				if got, ok := os.LookupEnv(key); ok {
					t.Fatalf("缺 PREVIOUS_BACKEND 的文件里的 %s 不应进环境: %q", key, got)
				}
			}
			if got := os.Getenv("PG_HOST"); got != tc.pgHost {
				t.Fatalf("PG_HOST=%q want %q", got, tc.pgHost)
			}
			if tc.pgHost != "" && (os.Getenv("PG_PORT") != "5433" || os.Getenv("PG_USER") != "me" || os.Getenv("PG_PASSWORD") != "pw" ||
				os.Getenv("PG_DB") != "cine" || os.Getenv("PG_SSLMODE") != "require") {
				t.Fatal("其余 PG_* 应照常加载")
			}
		})
	}
}

// D-DBG-05：与 godotenv.Load 同一口径，进程环境里已有的变量（空值也算）不被文件覆盖，没有的才补上。
func TestDBG05ProcessEnvWinsOverNonAdoptableDataDirConfig(t *testing.T) {
	dataDir := guardInitEnv(t)
	t.Setenv("PG_HOST", "env-host")
	t.Setenv("PG_PORT", "")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte("PG_HOST=file-host\nPG_PORT=5433\nPG_USER=file-user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loadEnvConfig(dataDir)
	if got := os.Getenv("PG_HOST"); got != "env-host" {
		t.Fatalf("进程环境的 PG_HOST 应优先: %q", got)
	}
	if got, ok := os.LookupEnv("PG_PORT"); !ok || got != "" {
		t.Fatalf("进程环境里的空值也不应被覆盖: %q ok=%v", got, ok)
	}
	if got := os.Getenv("PG_USER"); got != "file-user" {
		t.Fatalf("进程环境里没有的键应由文件补上: %q", got)
	}
}

// D-DBG-05：本版本写出的格式（带 PREVIOUS_BACKEND）照旧整份加载，DB_BACKEND 照常生效。
func TestDBG05AdoptableDataDirConfigStillLoadsWholeFile(t *testing.T) {
	dataDir := guardInitEnv(t)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte("DB_BACKEND=sqlite\nPREVIOUS_BACKEND=postgres\nPG_HOST=db.local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend, err := resolveStartupBackend(dataDir)
	if err != nil || backend != BackendSQLite {
		t.Fatalf("带 PREVIOUS_BACKEND 的文件里的 DB_BACKEND 应生效: backend=%s err=%v", backend, err)
	}
	if os.Getenv(BackendConfigKey) != "sqlite" || os.Getenv(PreviousBackendConfigKey) != "postgres" || os.Getenv("PG_HOST") != "db.local" {
		t.Fatalf("应整份加载: DB_BACKEND=%q PREVIOUS_BACKEND=%q PG_HOST=%q",
			os.Getenv(BackendConfigKey), os.Getenv(PreviousBackendConfigKey), os.Getenv("PG_HOST"))
	}
}

// D-DBG-05：被忽略的 DB_BACKEND 不算显式选择——上次是 PostgreSQL 时照样拦下。
func TestDBG05IgnoredBackendKeyDoesNotCountAsExplicit(t *testing.T) {
	dataDir := guardInitEnv(t)
	writeRecordForTest(t, dataDir, BackendPostgres, "me@db.local:5432/cine")
	if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte("DB_BACKEND=sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Init(); !errors.Is(err, ErrBackendFallbackBlocked) {
		t.Fatalf("被忽略的 DB_BACKEND 不应算显式选择: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, DefaultSQLiteFileName)); !os.IsNotExist(err) {
		t.Fatalf("被拦下时不应创建 SQLite 库文件: %v", err)
	}
}

// D-DBG-03 + D-DBG-05 的出口：被拦下后照出错页的推荐把 PG_* 写进数据目录下的 .env（不带 PREVIOUS_BACKEND），
// 下次启动不再被拦，按 PostgreSQL 去连（测试里连一个没人监听的端口，得到的是连接错误而不是拦截），
// 也不建 SQLite 库文件。
func TestDBG05PGConfigInDataDirConfigUnblocksNextStart(t *testing.T) {
	dataDir := guardInitEnv(t)
	writeRecordForTest(t, dataDir, BackendPostgres, "me@127.0.0.1:5432/cine")
	if err := Init(); !errors.Is(err, ErrBackendFallbackBlocked) {
		t.Fatalf("应先被拦下: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte("PG_HOST=127.0.0.1\nPG_PORT=1\nPG_USER=me\nPG_DB=cine\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 模拟「重新打开」：新进程的启动缓存是空的。
	resetBackendStartupForTest(t)
	err := Init()
	if err == nil {
		t.Fatal("测试端口上没有 PostgreSQL，应得到连接错误")
	}
	if errors.Is(err, ErrBackendFallbackBlocked) || StartupGuard().Blocked {
		t.Fatalf("写好 PG_* 之后不应再被拦: %v", err)
	}
	if got := ActiveBackend(); got != BackendPostgres {
		t.Fatalf("应按 PostgreSQL 去连: %s", got)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, DefaultSQLiteFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("不应创建 SQLite 库文件: %v", statErr)
	}
	if state := readRecordForTest(t, dataDir); state.Backend != BackendPostgres {
		t.Fatalf("连接失败时记录不应改动: %#v", state)
	}
}
