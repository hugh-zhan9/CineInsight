package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 数据库后端回落保护（D-DBG-01..05，docs/loopx/design/2026-10-14-db-backend-guard/）。
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

// ErrBackendFallbackBlocked 是拦下隐式回落时返回的错误（用 errors.Is 识别）。
var ErrBackendFallbackBlocked = errors.New(BackendFallbackBlockedReason)

// 拦下的两种依据（StartupBackendGuard.Reason）。
const (
	// BackendGuardReasonPreviousPostgres：记录里上次是 PostgreSQL。
	BackendGuardReasonPreviousPostgres = "previous_postgres"
	// BackendGuardReasonInferredPostgres：还没有记录（升级后的第一次），但数据目录里有只有数据库用起来之后
	// 才会出现的数据，本机 SQLite 库文件却不存在——看起来之前用的是 PostgreSQL。
	BackendGuardReasonInferredPostgres = "inferred_postgres"
)

// BackendState 是 backend-state.json 的内容。Location 不含密码：PostgreSQL 写 user@host:port/dbname，
// SQLite 写库文件路径。UpdatedAt 是 RFC3339。
type BackendState struct {
	Backend   Backend `json:"backend"`
	Location  string  `json:"location"`
	UpdatedAt string  `json:"updated_at"`
	// Postgres 是上次 PostgreSQL 连接的各项（不含密码），出错页据此预填配置块（D-DBG-05）。
	// SQLite 记录与较早写下的记录没有这一项，读的时候按不知道处理。
	Postgres *BackendStatePostgres `json:"postgres,omitempty"`
}

// BackendStatePostgres 是记录里的 PostgreSQL 连接项。密码永远不记。SSLMode 只在 PG_SSLMODE 显式给出时记。
type BackendStatePostgres struct {
	Host    string `json:"host"`
	Port    string `json:"port"`
	User    string `json:"user"`
	DBName  string `json:"dbname"`
	SSLMode string `json:"sslmode,omitempty"`
}

// BackendConfigFile 是启动时按优先级读过的一个配置文件位置，以及它当时是否存在。
type BackendConfigFile struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

// StartupBackendGuard 是本次启动回落保护的结果，启动出错页据此给出专门说明（D-DBG-03..05）。
// 没有拦下时只有 Blocked=false，其余为空。
type StartupBackendGuard struct {
	Blocked bool `json:"blocked"`
	// Reason 是拦下的依据：previous_postgres（记录里上次是 PostgreSQL）或 inferred_postgres（按数据目录推断）。
	Reason string `json:"reason"`
	// PreviousLocation 是记录里上次用的 PostgreSQL 位置（user@host:port/dbname）；推断出来的为空。
	PreviousLocation string `json:"previous_location"`
	// ConfigFiles 按优先级从高到低列出这次读过的配置文件（与 envConfigPaths 同一顺序）。
	ConfigFiles []BackendConfigFile `json:"config_files"`
	// DataDirConfig 是推荐放配置的位置：数据目录下的 .env。
	DataDirConfig string `json:"data_dir_config"`
	// ConfigSnippet 是可以直接存成 DataDirConfig 的配置块（本版本写出的格式，按记录预填，密码留给用户填）。
	ConfigSnippet string `json:"config_snippet"`
	// PGHostEmptyInLaunchEnv：启动环境（进程环境变量，加载配置文件之前）里有 PG_HOST 但是空值。
	// 配置文件不覆盖已有的变量，写在任何配置文件里的 PG_HOST 都会被它挡住。
	PGHostEmptyInLaunchEnv bool `json:"pg_host_empty_in_launch_env"`
}

// backendGuardInput 是回落保护判定的全部输入（D-DBG-02）。
type backendGuardInput struct {
	resolved Backend       // 本次解析结果
	explicit bool          // 加载完所有配置文件后 DB_BACKEND 非空
	previous *BackendState // 记录；nil 表示没有记录
	// 以下两项只在「没有记录」时有意义（首次运行缺口）。
	sqliteExists          bool // 本次解析出的 SQLite 库文件已经存在
	postDatabaseArtifacts bool // 数据目录里有只有数据库用起来之后才会出现的数据
}

// backendFallbackBlockReason 判定启动时是否拦下隐式回落（D-DBG-02，纯函数），返回拦下的依据，放行时返回空串。
// 本次解析结果是 SQLite、且不是显式选择（加载完所有配置文件后 DB_BACKEND 仍为空，走的是「没有 PG_HOST 就用
// SQLite」这条默认）时才看下去：
//   - 有记录：上次是 PostgreSQL 就拦，否则放行；
//   - 没有记录：本次的 SQLite 库文件不存在、而数据目录里又有只有数据库用起来之后才会出现的数据时拦——
//     升级后第一次启动还没有记录，这种组合说明之前用的不是这个本机库；全新安装两样都没有，放行。
func backendFallbackBlockReason(in backendGuardInput) string {
	if in.resolved != BackendSQLite || in.explicit {
		return ""
	}
	if in.previous != nil {
		if in.previous.Backend == BackendPostgres {
			return BackendGuardReasonPreviousPostgres
		}
		return ""
	}
	if !in.sqliteExists && in.postDatabaseArtifacts {
		return BackendGuardReasonInferredPostgres
	}
	return ""
}

// backendFallbackBlockedError 按拦下的依据写说明，消息以 backend_fallback_blocked: 开头。
func backendFallbackBlockedError(guard StartupBackendGuard) error {
	if guard.Reason == BackendGuardReasonInferredPostgres {
		return fmt.Errorf("%w: 数据目录里有之前使用留下的数据，但本机 SQLite 库文件不存在，这次也没有读到 PostgreSQL 配置；看起来之前用的是 PostgreSQL，为了不打开一个空的本机库，已经停下",
			ErrBackendFallbackBlocked)
	}
	return fmt.Errorf("%w: 上次用的是 PostgreSQL（%s），这次没有读到 PostgreSQL 配置；为了不打开一个空的本机库，已经停下，数据还在 PostgreSQL 里",
		ErrBackendFallbackBlocked, guard.PreviousLocation)
}

// postDatabaseArtifactDirs 是数据目录下只有数据库用起来之后才会写东西的目录（D-DBG-02 首次运行缺口）：
// 视频与图片缩略图缓存、托管图片（头像、封面等）、播放代理、数据库备份（默认位置）、人脸裁剪图。它们都是
// 处理库里的记录时才建出来的；database.Init 之前应用只建出一个空的数据目录（NewApp），日志（app.log，读到
// 设置才写）、用户自己放的 .env、记录本身都不在其列。
var postDatabaseArtifactDirs = []string{"thumbnails", "image-thumbnails", "media-details", "proxies", "backups", "faces"}

// PostDatabaseArtifactDirs 返回上面那份目录名（只读副本），供 main 的测试核对 NewApp 不会预先建出它们。
func PostDatabaseArtifactDirs() []string {
	return append([]string{}, postDatabaseArtifactDirs...)
}

// hasPostDatabaseArtifacts 报告数据目录里是否有上面任一目录且里面至少有一项（空目录说明不了什么）。
// 只读一个目录项，缩略图目录再大也不会拖慢启动。
func hasPostDatabaseArtifacts(dataDir string) bool {
	for _, name := range postDatabaseArtifactDirs {
		directory, err := os.Open(filepath.Join(dataDir, name))
		if err != nil {
			continue
		}
		names, _ := directory.Readdirnames(1)
		_ = directory.Close()
		if len(names) > 0 {
			return true
		}
	}
	return false
}

func backendStatePath(dataDir string) string {
	return filepath.Join(dataDir, BackendStateFileName)
}

// readBackendState 读记录；文件不存在时返回 nil、不报错。较早写下的记录没有 postgres 一项，照常读。
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

// newBackendState 按当前环境生成这次的记录，不含密码：PostgreSQL 记 user@host:port/dbname 与各连接项，
// SQLite 记库文件路径。
func newBackendState(backend Backend, dataDir string, now time.Time) BackendState {
	state := BackendState{Backend: backend, UpdatedAt: now.Format(time.RFC3339)}
	if backend == BackendSQLite {
		state.Location = SQLitePath(dataDir)
		return state
	}
	config, err := PostgresCLIConfigFromEnv()
	if err != nil {
		return state
	}
	state.Location = fmt.Sprintf("%s@%s:%s/%s", config.User, config.Host, config.Port, config.Database)
	state.Postgres = &BackendStatePostgres{
		Host:    config.Host,
		Port:    config.Port,
		User:    config.User,
		DBName:  config.Database,
		SSLMode: strings.TrimSpace(os.Getenv("PG_SSLMODE")),
	}
	return state
}

// writeBackendState 原子写记录（0600）：同目录临时文件写完、fsync 之后再 rename，中途断电不会留下半截记录；
// rename 之后再 fsync 目录，让改名本身落盘。目录 fsync 失败只记日志：改名已经生效，本机后续读到的就是新记录。
func writeBackendState(dataDir string, state BackendState) error {
	content, err := json.MarshalIndent(state, "", "  ")
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
	if err := temp.Sync(); err != nil {
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
	if err := syncDirectory(dataDir); err != nil {
		log.Printf("记录已改名但数据目录 fsync 失败 err=%v", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// recordOpenedBackend 在库打开、建好表之后记下这次的后端（D-DBG-01）。写失败只记日志，不影响启动。
func recordOpenedBackend(dataDir string, backend Backend) {
	if err := writeBackendState(dataDir, newBackendState(backend, dataDir, time.Now())); err != nil {
		log.Printf("记下本次使用的数据库后端失败（不影响启动） backend=%s err=%v", backend, err)
	}
}

// configSnippetPasswordPlaceholder 是配置块里密码一项的占位：记录里从来没有密码。
const configSnippetPasswordPlaceholder = "（在这里填密码）"

// BackendConfigSnippet 是出错页给的配置块（D-DBG-05）：与应用内切换写下的数据目录 .env 同一格式
// （DB_BACKEND=postgres、PREVIOUS_BACKEND=sqlite，带 PREVIOUS_BACKEND 才会被启动采用），PG_* 按记录预填，
// 不知道的留空；密码永远是占位，由用户自己填。PG_SSLMODE 只在记录里有时才写。
func BackendConfigSnippet(pg *BackendStatePostgres) string {
	var known BackendStatePostgres
	if pg != nil {
		known = *pg
	}
	lines := []string{
		BackendConfigKey + "=" + string(BackendPostgres),
		PreviousBackendConfigKey + "=" + string(BackendSQLite),
		"PG_HOST=" + known.Host,
		"PG_PORT=" + known.Port,
		"PG_USER=" + known.User,
		"PG_PASSWORD=" + configSnippetPasswordPlaceholder,
		"PG_DB=" + known.DBName,
	}
	if known.SSLMode != "" {
		lines = append(lines, "PG_SSLMODE="+known.SSLMode)
	}
	return strings.Join(lines, "\n") + "\n"
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
	if err := writeBackendState(dataDir, newBackendState(BackendSQLite, dataDir, time.Now())); err != nil {
		return fmt.Errorf("记下改用本机 SQLite 失败: %w", err)
	}
	return nil
}
