package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"video-master/database"
	"video-master/database/migrator"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// DatabaseBackendStatus 是设置页「数据库」分区要展示的当前状态。
type DatabaseBackendStatus struct {
	Backend           string `json:"backend"`
	Location          string `json:"location"`
	SemanticAvailable bool   `json:"semantic_available"`
	SemanticReason    string `json:"semantic_reason"`
	// PendingRestart 为真表示配置里的后端与正在使用的后端不一致——
	// 切换已经写进配置但应用还没重启。
	PendingRestart bool `json:"pending_restart"`
}

// DatabaseSwitchPreflight 是切换前的检查结果。
type DatabaseSwitchPreflight struct {
	Target     string `json:"target"`
	Reachable  bool   `json:"reachable"`
	Empty      bool   `json:"empty"`
	Location   string `json:"location"`
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message"`
}

// DatabaseSwitchStatus 是迁移进度，供轮询兜底（事件是主通道）。
type DatabaseSwitchStatus struct {
	Running    bool   `json:"running"`
	Target     string `json:"target"`
	Table      string `json:"table"`
	TableIndex int    `json:"table_index"`
	TableTotal int    `json:"table_total"`
	Completed  bool   `json:"completed"`
	Failed     bool   `json:"failed"`
	Message    string `json:"message"`
	// Location 是目标库位置。迁移失败时前端据此显示目标库在哪、并提供「清空目标库」（D-PC55）。
	Location string `json:"location"`
	// RelaunchRequired 在迁移成功后为真：配置已写入，前端显示「立即重启」（调 RelaunchApp）。
	// 此后进入「待重启」终态：维护围栏保持、库只读，恢复与再次切换返回 relaunch_pending。
	RelaunchRequired bool `json:"relaunch_required"`
}

// DatabaseSwitchConfigResult 是「切回之前的后端」（只改配置、不迁移）的结果。
// 被拒绝时 Switched=false，ReasonCode 说明原因：previous_unknown / not_previous /
// target_empty / target_half_migrated / unreachable / same_backend / switch_running / relaunch_pending。
type DatabaseSwitchConfigResult struct {
	Target           string `json:"target"`
	Switched         bool   `json:"switched"`
	RelaunchRequired bool   `json:"relaunch_required"`
	ReasonCode       string `json:"reason_code"`
	Message          string `json:"message"`
}

// DatabaseTargetClearResult 是「清空目标库」的结果（D-PC55）。Removed 列出实际删掉的
// 东西：SQLite 是库文件及其边车的文件名，PostgreSQL 是表名。被拒绝时 Cleared=false，
// ReasonCode 为 confirm_mismatch / active_backend / switch_running / outside_data_dir /
// not_regular_file / unreachable / clear_failed / relaunch_pending 之一。
type DatabaseTargetClearResult struct {
	Target     string   `json:"target"`
	Location   string   `json:"location"`
	Cleared    bool     `json:"cleared"`
	Removed    []string `json:"removed"`
	ReasonCode string   `json:"reason_code"`
	Message    string   `json:"message"`
}

// RelaunchResult 是 App.RelaunchApp 的结果。Relaunched=false 表示当前不是从 .app 包运行，
// 应用只会退出，需要用户手动重新打开（Message 里写明）。
type RelaunchResult struct {
	Relaunched bool   `json:"relaunched"`
	Message    string `json:"message"`
}

// ClearMigrationTargetConfirmText 是清空目标库时用户必须输入的确认文字。
const ClearMigrationTargetConfirmText = "清空"

// DatabaseSwitchReasonRelaunchPending 是「待重启」终态下拒绝操作的原因码：迁移并切换已经成功，
// 维护围栏保持到重启，唯一出口是「立即重启」（D-PC55 / §11）。
const DatabaseSwitchReasonRelaunchPending = "relaunch_pending"

// ErrDatabaseRelaunchPending 是「待重启」终态下恢复备份、再次迁移切换被拒绝时的错误。
// 这两个入口只返回 error，前端据消息开头的原因码识别。
var ErrDatabaseRelaunchPending = errors.New(DatabaseSwitchReasonRelaunchPending + ": 数据库后端已切换完成，请先重启应用；重启前不能恢复备份或再次切换后端")

// 后端配置文件（数据目录下的 .env）里的两个键。PREVIOUS_BACKEND 记「上一次切换之前用的
// 后端」，只有它才允许「只改配置」地切回去（D-PC55）。
const (
	backendConfigKey         = database.BackendConfigKey
	previousBackendConfigKey = database.PreviousBackendConfigKey
)

// DatabaseSwitchService 负责后端切换：预检、迁移、写配置。
//
// 它不热换 database.DB（设计 D-007）。句柄被约二十个服务和多个后台 worker 持有，
// 运行期替换没有安全的时机；切换以"写配置 + 要求重启"收尾。
type DatabaseSwitchService struct {
	dataDir string
	mu      sync.Mutex
	running atomic.Bool
	// relaunchPending 在一次迁移并切换成功后置位，本进程内不再复位：之后到重启前的写入会落进
	// 旧库、重启后在用户眼里消失，所以维护围栏一直保持，恢复、再次切换、只改配置、清空目标库
	// 一律拒绝（relaunch_pending）。
	relaunchPending atomic.Bool

	// cancelMigration 非空表示有一次迁移正在进行，CancelRunningSwitch 调它取消。
	cancelMu        sync.Mutex
	cancelMigration context.CancelFunc

	statusMu sync.RWMutex
	status   DatabaseSwitchStatus

	// onProgress 由 App 注入，用来往前端推事件；为空时只更新轮询状态。
	onProgress func(DatabaseSwitchStatus)

	// openTargetOverride 仅供测试替换目标库的打开方式：PostgreSQL 分支在测试里必须落到
	// dbtest 的独立 schema，不能照 PG_* 环境变量去连共享库的默认 schema。
	openTargetOverride func(database.Backend) (*gorm.DB, func(), error)

	// backendFromProcessEnvOverride 仅供测试替换「DB_BACKEND 是否来自进程环境」的判定；
	// 为空时用 database.BackendFromProcessEnv（启动时记下）。
	backendFromProcessEnvOverride func() bool
}

func NewDatabaseSwitchService(dataDir string) *DatabaseSwitchService {
	return &DatabaseSwitchService{dataDir: dataDir}
}

func (s *DatabaseSwitchService) SetProgressSink(sink func(DatabaseSwitchStatus)) {
	s.onProgress = sink
}

// Status 返回当前后端与语义检索能力。
func (s *DatabaseSwitchService) Status() DatabaseBackendStatus {
	configured := database.ActiveBackend()
	status := DatabaseBackendStatus{
		Backend:  string(configured),
		Location: s.locationOf(configured),
	}
	if database.DB != nil {
		live := database.DB.Dialector.Name()
		// 配置说 A、句柄连着 B —— 切换已写入但还没重启。进程环境变量在启动时就定下了，
		// 切换只改数据目录下的 .env，所以下次启动的后端要从那份文件读；判定口径与启动时
		// 加载那份文件的口径一致：DB_BACKEND 来自进程环境时文件压不过它，旧格式文件
		// （没有 PREVIOUS_BACKEND）启动时不采用。
		next := configured
		if !s.backendFromProcessEnv() {
			if values, err := readBackendConfig(s.dataDir); err == nil && database.BackendConfigAdoptable(values) &&
				strings.TrimSpace(values[backendConfigKey]) != "" {
				if persisted, err := database.ResolveBackend(database.BackendEnv{Backend: values[backendConfigKey]}); err == nil {
					next = persisted
				}
			}
		}
		status.PendingRestart = live != string(next)
		capability := database.PrepareSemanticVectorStorage(database.DB)
		status.SemanticAvailable = capability.Available
		status.SemanticReason = capability.Message
	}
	return status
}

// DatabaseSwitchReasonBackendEnvLocked：DB_BACKEND 来自进程环境变量，它压过数据目录下的配置，
// 应用内写入的切换永远不会生效，所以迁移并切换与只改配置都直接拒绝。
const DatabaseSwitchReasonBackendEnvLocked = "backend_env_locked"

const databaseSwitchBackendEnvLockedMessage = "当前后端由环境变量 DB_BACKEND 指定，应用内切换在重启后不会生效；请修改该环境变量后重启应用"

// Preflight 检查目标后端能不能连、是不是空的。不写任何数据。
func (s *DatabaseSwitchService) Preflight(target string) (*DatabaseSwitchPreflight, error) {
	backend, err := normalizeSwitchTarget(target)
	if err != nil {
		return nil, err
	}
	result := &DatabaseSwitchPreflight{Target: string(backend), Location: s.locationOf(backend)}
	if s.backendFromProcessEnv() {
		result.ReasonCode = DatabaseSwitchReasonBackendEnvLocked
		result.Message = databaseSwitchBackendEnvLockedMessage
		return result, nil
	}
	if backend == database.ActiveBackend() {
		result.ReasonCode = "same_backend"
		result.Message = "目标与当前后端相同，无需切换"
		return result, nil
	}

	db, cleanup, err := s.openTargetDB(backend)
	if err != nil {
		result.ReasonCode = "unreachable"
		result.Message = err.Error()
		return result, nil
	}
	defer cleanup()
	result.Reachable = true

	if err := migrator.Preflight(db); err != nil {
		result.ReasonCode = "not_empty"
		result.Message = err.Error()
		return result, nil
	}
	result.Empty = true
	result.Message = "目标可用，可以开始迁移"
	return result, nil
}

// Switch 迁移数据并把后端写进配置。成功后必须重启才会生效。
// 不带维护模式，只供没有 App 的场景使用；应用内的切换走 SwitchWithLifecycle。
func (s *DatabaseSwitchService) Switch(ctx context.Context, target string) error {
	return s.SwitchWithLifecycle(ctx, target, nil, nil)
}

// SwitchWithLifecycle 在维护模式下迁移并写配置（D-PC55 / APP-02）。
//
// enterMaintenance 由 App 注入（与恢复备份同一个 enterDatabaseRestoreMode：停后台服务、
// 立写入围栏，但**不关连接**）；leaveMaintenance 撤围栏并恢复服务。
//
//   - 失败（含进入维护模式到一半失败、迁移被取消）：只要调用过 enterMaintenance，就在发布终态
//     之前调用一次 leaveMaintenance——已经停掉的服务同样要恢复；目标库可能留为半迁移，
//     由「清空目标库」处理后重试。
//   - 成功：**不**调用 leaveMaintenance，维护围栏一直保持到重启，服务进入「待重启」终态
//     （RelaunchPending）。配置已指向新库，此后落进旧库的写入重启后在用户眼里就消失了。
//
// 围栏期间普通写入一律被 database 的维护屏障拒绝；迁移器读源库经 WithMaintenanceAccess
// 这条恢复流程专用的通道，因此是围栏内唯一能碰源库的访问，而且只读。
func (s *DatabaseSwitchService) SwitchWithLifecycle(
	ctx context.Context,
	target string,
	enterMaintenance func() error,
	leaveMaintenance func(),
) error {
	backend, err := normalizeSwitchTarget(target)
	if err != nil {
		return err
	}
	if s.relaunchPending.Load() {
		return ErrDatabaseRelaunchPending
	}
	if s.backendFromProcessEnv() {
		return fmt.Errorf("%s: %s", DatabaseSwitchReasonBackendEnvLocked, databaseSwitchBackendEnvLockedMessage)
	}
	source := database.ActiveBackend()
	if backend == source {
		return fmt.Errorf("目标与当前后端相同，无需切换")
	}
	if !s.mu.TryLock() {
		return fmt.Errorf("已有一次切换正在进行")
	}
	defer s.mu.Unlock()
	if s.relaunchPending.Load() {
		return ErrDatabaseRelaunchPending
	}
	ctx, cancel := context.WithCancel(ctx)
	s.setMigrationCancel(cancel)
	defer func() {
		s.setMigrationCancel(nil)
		cancel()
	}()
	s.running.Store(true)
	defer s.running.Store(false)

	s.publish(DatabaseSwitchStatus{Running: true, Target: string(backend), Message: "正在检查目标库"})

	db, cleanup, err := s.openTargetDB(backend)
	if err != nil {
		return s.fail(backend, fmt.Errorf("连接目标库失败: %w", err))
	}
	defer cleanup()

	entered := false
	var migrateErr error
	var result *migrator.Result
	if ctxErr := ctx.Err(); ctxErr != nil {
		migrateErr = ctxErr
	} else if enterMaintenance != nil {
		s.publish(DatabaseSwitchStatus{Running: true, Target: string(backend), Message: "正在停止后台任务并暂停写入"})
		entered = true
		if enterErr := enterMaintenance(); enterErr != nil {
			migrateErr = fmt.Errorf("进入维护模式失败，未做任何迁移: %w", enterErr)
		}
	}
	if migrateErr == nil {
		result, migrateErr = s.migrateAndPersist(ctx, db, source, backend)
	}
	if migrateErr != nil {
		if entered && leaveMaintenance != nil {
			leaveMaintenance()
		}
		if errors.Is(migrateErr, context.Canceled) {
			migrateErr = fmt.Errorf("迁移已取消，当前库未受影响；目标库可能残留未完成的迁移，清空后可以重试: %w", migrateErr)
		}
		// 源库全程只读，此处失败不改配置——回滚就是"什么都不做"。
		return s.fail(backend, migrateErr)
	}

	// 先置位再发布终态：前端看到「完成」时，恢复、再次切换、只改配置、清空目标库都已被拒绝。
	s.relaunchPending.Store(true)
	s.publish(DatabaseSwitchStatus{
		Running: false, Target: string(backend), Completed: true,
		TableIndex: result.Tables, TableTotal: result.Tables,
		Location:         s.locationOf(backend),
		RelaunchRequired: true,
		Message:          "迁移完成，请立即重启应用；重启前数据库保持只读",
	})
	return nil
}

// RelaunchPending 报告本进程是否已处在「待重启」终态：一次迁移并切换已经成功，维护围栏保持到重启。
func (s *DatabaseSwitchService) RelaunchPending() bool {
	return s.relaunchPending.Load()
}

// CancelRunningSwitch 取消正在进行的迁移（退出应用时由 shutdown 在拿 restoreMu 之前调用）。
// 取消按失败处理：离开维护模式，配置不改，目标库可能留为半迁移。没有进行中的迁移时返回 false。
func (s *DatabaseSwitchService) CancelRunningSwitch() bool {
	s.cancelMu.Lock()
	cancel := s.cancelMigration
	s.cancelMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (s *DatabaseSwitchService) setMigrationCancel(cancel context.CancelFunc) {
	s.cancelMu.Lock()
	s.cancelMigration = cancel
	s.cancelMu.Unlock()
}

func (s *DatabaseSwitchService) backendFromProcessEnv() bool {
	if s.backendFromProcessEnvOverride != nil {
		return s.backendFromProcessEnvOverride()
	}
	return database.BackendFromProcessEnv()
}

// migrateAndPersist 复制数据，成功后写入「目标后端 + 上一个后端」两项配置。
// 源库与目标库的语句都跟随 ctx：取消时不必等正在复制的那张表读完。
func (s *DatabaseSwitchService) migrateAndPersist(ctx context.Context, target *gorm.DB, source, backend database.Backend) (*migrator.Result, error) {
	if database.DB == nil {
		return nil, fmt.Errorf("当前数据库未连接")
	}
	result, err := migrator.Migrate(ctx, migrator.Options{
		Source:        database.WithMaintenanceAccessContext(ctx, database.DB),
		Target:        target.WithContext(ctx),
		TargetBackend: backend,
		OnProgress: func(p migrator.Progress) {
			s.publish(DatabaseSwitchStatus{
				Running: true, Target: string(backend),
				Table: p.Table, TableIndex: p.TableIndex, TableTotal: p.TableTotal,
				Message: fmt.Sprintf("正在复制 %s", p.Table),
			})
		},
	})
	if err != nil {
		return nil, err
	}
	if err := persistBackendChoice(s.dataDir, backend, source); err != nil {
		return nil, fmt.Errorf("数据已迁移完成，但写入后端配置失败，重启后仍会连回原来的库: %w", err)
	}
	return result, nil
}

// SwitchBackendConfigOnly 切回上一个后端：只改配置、不迁移数据（D-PC55「切回之前的后端」）。
// 不带维护模式，只供没有 App 的场景与测试使用；应用内走 SwitchBackendConfigOnlyWithLifecycle。
func (s *DatabaseSwitchService) SwitchBackendConfigOnly(target string) (*DatabaseSwitchConfigResult, error) {
	return s.SwitchBackendConfigOnlyWithLifecycle(target, nil, nil)
}

// SwitchBackendConfigOnlyWithLifecycle 只在目标就是配置里记下的「上一个后端」、且目标库非空时允许——
// 空库或半迁移的库切过去等于换成一个空片库。切换之后在当前库里产生的改动不会带回目标库，Message 里写明。
//
// 与迁移并切换同一口径（APP-02）：检查全部通过后先 enterMaintenance 立写入围栏，再写配置；写配置失败
// 撤围栏；成功后围栏保持，进入「待重启」终态——配置已指向另一个库，重启前落进当前库的写入重启后
// 在用户眼里就消失了。成功返回 relaunch_required。
func (s *DatabaseSwitchService) SwitchBackendConfigOnlyWithLifecycle(target string, enterMaintenance func() error, leaveMaintenance func()) (*DatabaseSwitchConfigResult, error) {
	backend, err := normalizeSwitchTarget(target)
	if err != nil {
		return nil, err
	}
	result := &DatabaseSwitchConfigResult{Target: string(backend)}
	if s.relaunchPending.Load() {
		result.ReasonCode = DatabaseSwitchReasonRelaunchPending
		result.Message = "后端已切换完成，请先重启应用"
		return result, nil
	}
	if s.backendFromProcessEnv() {
		result.ReasonCode = DatabaseSwitchReasonBackendEnvLocked
		result.Message = databaseSwitchBackendEnvLockedMessage
		return result, nil
	}
	current := database.ActiveBackend()
	if backend == current {
		result.ReasonCode = "same_backend"
		result.Message = "目标与当前后端相同，无需切换"
		return result, nil
	}
	if !s.mu.TryLock() {
		result.ReasonCode = "switch_running"
		result.Message = "已有一次切换正在进行"
		return result, nil
	}
	defer s.mu.Unlock()
	if s.relaunchPending.Load() {
		result.ReasonCode = DatabaseSwitchReasonRelaunchPending
		result.Message = "后端已切换完成，请先重启应用"
		return result, nil
	}

	config, err := readBackendConfig(s.dataDir)
	if err != nil {
		return nil, fmt.Errorf("读取后端配置失败: %w", err)
	}
	previous := strings.ToLower(strings.TrimSpace(config[previousBackendConfigKey]))
	if previous == "" {
		result.ReasonCode = "previous_unknown"
		result.Message = "没有找到上一次切换的记录，只能用「迁移并切换」"
		return result, nil
	}
	if previous != string(backend) {
		result.ReasonCode = "not_previous"
		result.Message = "只能直接切回上一次使用的后端，其余情况请用「迁移并切换」"
		return result, nil
	}

	db, cleanup, err := s.openTargetDB(backend)
	if err != nil {
		result.ReasonCode = "unreachable"
		result.Message = "无法连接目标库: " + err.Error()
		return result, nil
	}
	preflightErr := migrator.Preflight(db)
	cleanup()
	var notEmpty *migrator.ErrTargetNotEmpty
	switch {
	case errors.As(preflightErr, &notEmpty):
		// 非空才是可以切回去的库。
	case errors.Is(preflightErr, migrator.ErrTargetHalfMigrated):
		result.ReasonCode = "target_half_migrated"
		result.Message = "目标库残留着一次未完成的迁移，不能直接切回"
		return result, nil
	case preflightErr == nil:
		result.ReasonCode = "target_empty"
		result.Message = "目标库是空的，切过去会得到一个空片库；请用「迁移并切换」"
		return result, nil
	default:
		return nil, fmt.Errorf("检查目标库失败: %w", preflightErr)
	}

	if enterMaintenance != nil {
		if err := enterMaintenance(); err != nil {
			if leaveMaintenance != nil {
				leaveMaintenance()
			}
			return nil, fmt.Errorf("进入维护模式失败，配置未修改: %w", err)
		}
	}
	if err := persistBackendChoiceFn(s.dataDir, backend, current); err != nil {
		if enterMaintenance != nil && leaveMaintenance != nil {
			leaveMaintenance()
		}
		return nil, fmt.Errorf("写入后端配置失败: %w", err)
	}
	s.relaunchPending.Store(true)
	result.Switched = true
	result.RelaunchRequired = true
	result.Message = fmt.Sprintf("已改为使用 %s，重启应用后生效。切换之后在当前库里产生的改动不会带回 %s。", backend, backend)
	return result, nil
}

// ClearMigrationTarget 清空迁移目标库，使失败的迁移可以重试（D-PC55）。
//
// confirmText 必须等于「清空」。当前正在使用的后端永远不能清空。
//   - SQLite：只删除位于应用数据目录内的目标库文件及其 -wal / -shm / -journal 边车；
//     SQLITE_PATH 指到数据目录之外时拒绝，不删用户放在别处的文件。
//   - PostgreSQL：database.DropApplicationTables——AllModels() 的表（含其隐式关联表）与迁移标记，
//     单个事务、不带 CASCADE，库里其他表不动。
func (s *DatabaseSwitchService) ClearMigrationTarget(target, confirmText string) (*DatabaseTargetClearResult, error) {
	backend, err := normalizeSwitchTarget(target)
	if err != nil {
		return nil, err
	}
	result := &DatabaseTargetClearResult{Target: string(backend), Location: s.locationOf(backend), Removed: []string{}}
	// 待重启时刚迁移完的目标库就是下次启动要用的库，清空它等于清空片库：先于一切判定拒绝。
	if s.relaunchPending.Load() {
		result.ReasonCode = DatabaseSwitchReasonRelaunchPending
		result.Message = "后端已切换完成，目标库就是重启后使用的库，不能清空；请先重启应用"
		return result, nil
	}
	if strings.TrimSpace(confirmText) != ClearMigrationTargetConfirmText {
		result.ReasonCode = "confirm_mismatch"
		result.Message = "请输入「" + ClearMigrationTargetConfirmText + "」确认清空目标库"
		return result, nil
	}
	if backend == database.ActiveBackend() {
		result.ReasonCode = "active_backend"
		result.Message = "不能清空当前正在使用的数据库"
		return result, nil
	}
	if !s.mu.TryLock() {
		result.ReasonCode = "switch_running"
		result.Message = "迁移正在进行，不能清空目标库"
		return result, nil
	}
	defer s.mu.Unlock()
	if s.relaunchPending.Load() {
		result.ReasonCode = DatabaseSwitchReasonRelaunchPending
		result.Message = "后端已切换完成，目标库就是重启后使用的库，不能清空；请先重启应用"
		return result, nil
	}

	if backend == database.BackendSQLite {
		return s.clearSQLiteTarget(result)
	}
	db, cleanup, err := s.openTargetDB(backend)
	if err != nil {
		result.ReasonCode = "unreachable"
		result.Message = "无法连接目标库: " + err.Error()
		return result, nil
	}
	defer cleanup()
	dropped, err := database.DropApplicationTables(db)
	if err != nil {
		result.ReasonCode = "clear_failed"
		result.Message = "清空目标库失败，未删除任何表（目标库里可能有其他对象依赖本应用的表）: " + err.Error()
		return result, nil
	}
	result.Cleared = true
	result.Removed = dropped
	result.Message = fmt.Sprintf("已删除目标库中本应用的 %d 张表，可以重新迁移", len(dropped))
	return result, nil
}

func (s *DatabaseSwitchService) clearSQLiteTarget(result *DatabaseTargetClearResult) (*DatabaseTargetClearResult, error) {
	path := database.SQLitePath(s.dataDir)
	inside, err := pathInsideDirectory(s.dataDir, path)
	if err != nil {
		return nil, fmt.Errorf("检查目标库位置失败: %w", err)
	}
	if !inside {
		result.ReasonCode = "outside_data_dir"
		result.Message = "目标库文件不在应用数据目录内，不会自动删除；请手动处理"
		return result, nil
	}
	// 主文件在最后删：边车先走，中途失败时主文件还在，状态不比清空前更糟。
	candidates := []string{path + "-wal", path + "-shm", path + "-journal", path}
	for _, candidate := range candidates {
		info, statErr := os.Lstat(candidate)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf("读取目标库文件失败: %w", statErr)
		}
		if !info.Mode().IsRegular() {
			result.ReasonCode = "not_regular_file"
			result.Message = "目标库位置上不是普通文件，不会自动删除；请手动处理"
			return result, nil
		}
	}
	for _, candidate := range candidates {
		if err := os.Remove(candidate); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("删除目标库文件失败: %w", err)
		}
		result.Removed = append(result.Removed, filepath.Base(candidate))
	}
	result.Cleared = true
	if len(result.Removed) == 0 {
		result.Message = "目标库文件不存在，无需清空"
	} else {
		result.Message = "已删除目标库文件，可以重新迁移"
	}
	return result, nil
}

// pathInsideDirectory 判断 path 是否位于 directory 之内（不含 directory 本身）。
// 两边都按符号链接解析后的父目录比较，避免经链接绕出数据目录。
func pathInsideDirectory(directory, path string) (bool, error) {
	base, err := filepath.Abs(directory)
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	parent := filepath.Dir(target)
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		parent = resolved
	}
	target = filepath.Join(parent, filepath.Base(target))
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false, nil
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}
	return true, nil
}

// SwitchStatus 供前端轮询兜底。
func (s *DatabaseSwitchService) SwitchStatus() DatabaseSwitchStatus {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status
}

func (s *DatabaseSwitchService) fail(backend database.Backend, err error) error {
	s.publish(DatabaseSwitchStatus{
		Running: false, Target: string(backend), Failed: true,
		Location: s.locationOf(backend),
		Message:  err.Error(),
	})
	return err
}

func (s *DatabaseSwitchService) publish(status DatabaseSwitchStatus) {
	s.statusMu.Lock()
	s.status = status
	s.statusMu.Unlock()
	if s.onProgress != nil {
		s.onProgress(status)
	}
}

func (s *DatabaseSwitchService) locationOf(backend database.Backend) string {
	if backend == database.BackendSQLite {
		return database.SQLitePath(s.dataDir)
	}
	config, err := database.PostgresCLIConfigFromEnv()
	if err != nil {
		return "未配置 PostgreSQL 连接"
	}
	return fmt.Sprintf("%s:%s/%s", config.Host, config.Port, config.Database)
}

// openTargetDB 是所有打开目标库的入口：测试可经 openTargetOverride 换掉实际连接。
func (s *DatabaseSwitchService) openTargetDB(backend database.Backend) (*gorm.DB, func(), error) {
	if s.openTargetOverride != nil {
		return s.openTargetOverride(backend)
	}
	return s.openTarget(backend)
}

// openTarget 打开目标后端的连接。返回的 cleanup 一定要调用——切换过程里这是一个
// 临时连接，不能泄漏到进程生命周期里。
func (s *DatabaseSwitchService) openTarget(backend database.Backend) (*gorm.DB, func(), error) {
	var db *gorm.DB
	var err error
	switch backend {
	case database.BackendSQLite:
		path := database.SQLitePath(s.dataDir)
		if mkErr := os.MkdirAll(filepath.Dir(path), 0755); mkErr != nil {
			return nil, nil, mkErr
		}
		db, err = gorm.Open(sqlite.Open(database.SQLiteDSN(path)), &gorm.Config{})
	case database.BackendPostgres:
		dsn, dsnErr := database.PostgresDSNFromEnv()
		if dsnErr != nil {
			return nil, nil, dsnErr
		}
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	default:
		return nil, nil, fmt.Errorf("不支持的后端: %s", backend)
	}
	if err != nil {
		return nil, nil, err
	}
	return db, func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	}, nil
}

func normalizeSwitchTarget(target string) (database.Backend, error) {
	switch database.Backend(strings.ToLower(strings.TrimSpace(target))) {
	case database.BackendSQLite:
		return database.BackendSQLite, nil
	case database.BackendPostgres:
		return database.BackendPostgres, nil
	default:
		return "", fmt.Errorf("不支持的目标后端: %s（可选 sqlite / postgres）", target)
	}
}

// backendConfigPath 是后端配置文件的位置：数据目录下的 .env。
func backendConfigPath(dataDir string) string {
	return filepath.Join(dataDir, ".env")
}

// readBackendConfig 读后端配置文件；文件不存在时返回空表。
func readBackendConfig(dataDir string) (map[string]string, error) {
	path := backendConfigPath(dataDir)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	return godotenv.Read(path)
}

// persistBackendChoiceFn 是只改配置那条路径写配置的入口；只在单测里替换，用来模拟写配置失败。
var persistBackendChoiceFn = persistBackendChoice

// persistBackendChoice 把选择写进数据目录下的 .env：DB_BACKEND 是切换后的后端，
// PREVIOUS_BACKEND 是切换之前的后端（供「切回之前的后端」判定，D-PC55）。
//
// 写这里而不是仓库根目录：应用是打包分发的，运行时能稳定写入的位置只有用户数据目录。
func persistBackendChoice(dataDir string, backend, previous database.Backend) error {
	path := backendConfigPath(dataDir)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	values := []struct {
		key   string
		value string
	}{
		{backendConfigKey, string(backend)},
		{previousBackendConfigKey, string(previous)},
	}
	written := make([]bool, len(values))
	lines := []string{}
	for _, line := range strings.Split(string(existing), "\n") {
		replaced := false
		for index, entry := range values {
			if strings.HasPrefix(strings.TrimSpace(line), entry.key+"=") {
				lines = append(lines, entry.key+"="+entry.value)
				written[index] = true
				replaced = true
				break
			}
		}
		if !replaced {
			lines = append(lines, line)
		}
	}
	for index, entry := range values {
		if !written[index] {
			lines = append(lines, entry.key+"="+entry.value)
		}
	}
	content := strings.TrimLeft(strings.Join(lines, "\n"), "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	// 先写临时文件再改名：中途断电不会留下一个半截的 .env，那会让应用下次启动
	// 读到一个非法的后端取值而直接失败。
	temp := path + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	if err := os.WriteFile(temp, []byte(content), 0600); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}
