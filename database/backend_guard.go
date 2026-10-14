package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// 数据库后端回落保护（D-DBG-01..04，docs/loopx/design/2026-10-14-db-backend-guard/）。
//
// 选库规则（ResolveBackend）在 DB_BACKEND 与 PG_HOST 都没有时用 SQLite。PostgreSQL 用户某次启动没读到
// PG_HOST（配置文件随应用包被替换、换了启动方式、环境变量没带上），就会静默落到 SQLite，openBackend
// 顺手建出一个空库，用户看到空片库以为数据丢了。这里记下上次成功打开的后端：上次是 PostgreSQL、
// 这次却是走默认得到的 SQLite 时停下，不打开、也不创建库文件。选库规则本身不变。

// BackendStateFileName 是数据目录下记录「上次成功打开的后端」的文件（D-DBG-01）。
const BackendStateFileName = "backend-state.json"

// BackendFallbackBlockedReason 是拦下隐式回落时的原因码。错误消息以「原因码: 」开头，
// 与 backend_env_locked 同一写法。
const BackendFallbackBlockedReason = "backend_fallback_blocked"

// ErrBackendFallbackBlocked 是拦下隐式回落时返回的错误（用 errors.Is 识别），消息里带上次的库位置。
var ErrBackendFallbackBlocked = errors.New(BackendFallbackBlockedReason)

// BackendState 是 backend-state.json 的内容。Location 不含密码：PostgreSQL 写 user@host:port/dbname，
// SQLite 写库文件路径。UpdatedAt 是 RFC3339。
type BackendState struct {
	Backend   Backend `json:"backend"`
	Location  string  `json:"location"`
	UpdatedAt string  `json:"updated_at"`
}

// BackendConfigFile 是启动时按优先级读过的一个配置文件位置，以及它当时是否存在。
type BackendConfigFile struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

// StartupBackendGuard 是本次启动回落保护的结果，启动出错页据此给出专门说明（D-DBG-03、D-DBG-04）。
// 没有拦下时只有 Blocked=false，其余为空。
type StartupBackendGuard struct {
	Blocked bool `json:"blocked"`
	// PreviousLocation 是记录里上次用的 PostgreSQL 位置（user@host:port/dbname）。
	PreviousLocation string `json:"previous_location"`
	// ConfigFiles 按优先级从高到低列出这次读过的配置文件（与 envConfigPaths 同一顺序）。
	ConfigFiles []BackendConfigFile `json:"config_files"`
	// DataDirConfig 是推荐放配置的位置：数据目录下的 .env。
	DataDirConfig string `json:"data_dir_config"`
}

// shouldBlockBackendFallback 判定启动时是否拦下隐式回落（D-DBG-02，纯函数）。三条同时满足才拦：
// 本次解析结果是 SQLite；不是显式选择（加载完所有配置文件后 DB_BACKEND 仍为空，走的是「没有 PG_HOST
// 就用 SQLite」这条默认）；记录里上次是 PostgreSQL。previous 为 nil 表示没有记录（新装、旧版本升级后
// 的第一次），放行。
func shouldBlockBackendFallback(resolved Backend, explicit bool, previous *BackendState) bool {
	return resolved == BackendSQLite && !explicit && previous != nil && previous.Backend == BackendPostgres
}

// backendFallbackBlockedError 带上次的位置，消息以 backend_fallback_blocked: 开头。
func backendFallbackBlockedError(previousLocation string) error {
	return fmt.Errorf("%w: 上次用的是 PostgreSQL（%s），这次没有读到 PostgreSQL 配置；为了不打开一个空的本机库，已经停下，数据还在 PostgreSQL 里",
		ErrBackendFallbackBlocked, previousLocation)
}

func backendStatePath(dataDir string) string {
	return filepath.Join(dataDir, BackendStateFileName)
}

// readBackendState 读记录；文件不存在时返回 nil、不报错。
func readBackendState(dataDir string) (*BackendState, error) {
	content, err := os.ReadFile(backendStatePath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state BackendState
	if err := json.Unmarshal(content, &state); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", BackendStateFileName, err)
	}
	return &state, nil
}

// writeBackendState 原子写记录：同目录临时文件写完再 rename，中途断电不会留下半截记录。
func writeBackendState(dataDir string, backend Backend, location string, now time.Time) error {
	content, err := json.MarshalIndent(BackendState{Backend: backend, Location: location, UpdatedAt: now.Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dataDir, "."+BackendStateFileName+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if _, err := temp.Write(append(content, '\n')); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, backendStatePath(dataDir)); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

// backendLocation 返回记录里的库位置，不含密码：PostgreSQL 是 user@host:port/dbname，SQLite 是库文件路径。
func backendLocation(backend Backend, dataDir string) string {
	if backend == BackendSQLite {
		return SQLitePath(dataDir)
	}
	config, err := PostgresCLIConfigFromEnv()
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s@%s:%s/%s", config.User, config.Host, config.Port, config.Database)
}

// recordOpenedBackend 在库打开、建好表之后记下这次的后端（D-DBG-01）。写失败只记日志，不影响启动。
func recordOpenedBackend(dataDir string, backend Backend) {
	if err := writeBackendState(dataDir, backend, backendLocation(backend, dataDir), time.Now()); err != nil {
		log.Printf("记下本次使用的数据库后端失败（不影响启动） backend=%s err=%v", backend, err)
	}
}

// startupConfigFiles 按优先级列出这次读过的配置文件及其是否存在，判定口径与 loadEnvConfig 一致
// （os.Stat 成功即存在）。相对路径（工作目录下的 .env）换成绝对路径，出错页上才看得懂。
func startupConfigFiles(dataDir string) []BackendConfigFile {
	paths := envConfigPaths(dataDir)
	files := make([]BackendConfigFile, 0, len(paths))
	for _, path := range paths {
		_, err := os.Stat(path)
		display := path
		if absolute, absErr := filepath.Abs(path); absErr == nil {
			display = absolute
		}
		files = append(files, BackendConfigFile{Path: display, Exists: err == nil})
	}
	return files
}

// StartupGuard 返回本次启动回落保护的结果（GetStartupBackendGuard 绑定读它）。
func StartupGuard() StartupBackendGuard {
	backendStartup.mu.Lock()
	defer backendStartup.mu.Unlock()
	guard := backendStartup.guard
	guard.ConfigFiles = append([]BackendConfigFile{}, guard.ConfigFiles...)
	return guard
}

// AcceptSQLiteAfterFallbackBlock 是启动出错页的「这次改用本机 SQLite」（D-DBG-03、D-DBG-04）：只在本次启动
// 被拦下时可用，把记录改成 sqlite，随后由调用方重启应用；下次启动按「上次是 sqlite」放行。
// 不写任何 .env：选库规则与配置文件都不变，之后把 PostgreSQL 配置放回去、重新打开就回到 PostgreSQL。
func AcceptSQLiteAfterFallbackBlock() error {
	backendStartup.mu.Lock()
	defer backendStartup.mu.Unlock()
	if !backendStartup.guard.Blocked {
		return errors.New("本次启动没有因为读不到 PostgreSQL 配置而停下，不需要改用本机 SQLite")
	}
	dataDir := backendStartup.dataDir
	if err := writeBackendState(dataDir, BackendSQLite, SQLitePath(dataDir), time.Now()); err != nil {
		return fmt.Errorf("记下改用本机 SQLite 失败: %w", err)
	}
	return nil
}
