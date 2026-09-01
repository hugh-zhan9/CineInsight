package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"video-master/database"
	"video-master/database/migrator"

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
}

// DatabaseSwitchService 负责后端切换：预检、迁移、写配置。
//
// 它不热换 database.DB（设计 D-007）。句柄被约二十个服务和多个后台 worker 持有，
// 运行期替换没有安全的时机；切换以"写配置 + 要求重启"收尾。
type DatabaseSwitchService struct {
	dataDir string
	mu      sync.Mutex
	running atomic.Bool

	statusMu sync.RWMutex
	status   DatabaseSwitchStatus

	// onProgress 由 App 注入，用来往前端推事件；为空时只更新轮询状态。
	onProgress func(DatabaseSwitchStatus)
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
		// 配置说 A、句柄连着 B —— 切换已写入但还没重启。
		status.PendingRestart = live != string(configured)
		capability := database.PrepareSemanticVectorStorage(database.DB)
		status.SemanticAvailable = capability.Available
		status.SemanticReason = capability.Message
	}
	return status
}

// Preflight 检查目标后端能不能连、是不是空的。不写任何数据。
func (s *DatabaseSwitchService) Preflight(target string) (*DatabaseSwitchPreflight, error) {
	backend, err := normalizeSwitchTarget(target)
	if err != nil {
		return nil, err
	}
	result := &DatabaseSwitchPreflight{Target: string(backend), Location: s.locationOf(backend)}
	if backend == database.ActiveBackend() {
		result.ReasonCode = "same_backend"
		result.Message = "目标与当前后端相同，无需切换"
		return result, nil
	}

	db, cleanup, err := s.openTarget(backend)
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
func (s *DatabaseSwitchService) Switch(ctx context.Context, target string) error {
	backend, err := normalizeSwitchTarget(target)
	if err != nil {
		return err
	}
	if backend == database.ActiveBackend() {
		return fmt.Errorf("目标与当前后端相同，无需切换")
	}
	if !s.mu.TryLock() {
		return fmt.Errorf("已有一次切换正在进行")
	}
	defer s.mu.Unlock()
	s.running.Store(true)
	defer s.running.Store(false)

	s.publish(DatabaseSwitchStatus{Running: true, Target: string(backend), Message: "正在检查目标库"})

	db, cleanup, err := s.openTarget(backend)
	if err != nil {
		return s.fail(backend, fmt.Errorf("连接目标库失败: %w", err))
	}
	defer cleanup()

	result, err := migrator.Migrate(ctx, migrator.Options{
		Source:        database.DB,
		Target:        db,
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
		// 源库全程只读，此处失败不改配置——回滚就是"什么都不做"。
		return s.fail(backend, err)
	}

	if err := persistBackendChoice(s.dataDir, backend); err != nil {
		return s.fail(backend, fmt.Errorf("数据已迁移完成，但写入后端配置失败，重启后仍会连回原来的库: %w", err))
	}

	s.publish(DatabaseSwitchStatus{
		Running: false, Target: string(backend), Completed: true,
		TableIndex: result.Tables, TableTotal: result.Tables,
		Message: "迁移完成，重启应用后生效",
	})
	return nil
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
		Message: err.Error(),
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

// persistBackendChoice 把选择写进数据目录下的 .env。
//
// 写这里而不是仓库根目录：应用是打包分发的，运行时能稳定写入的位置只有用户数据目录，
// 而 loadEnvConfig 已经会加载它。
func persistBackendChoice(dataDir string, backend database.Backend) error {
	path := filepath.Join(dataDir, ".env")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := []string{}
	replaced := false
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "DB_BACKEND=") {
			lines = append(lines, "DB_BACKEND="+string(backend))
			replaced = true
			continue
		}
		lines = append(lines, line)
	}
	if !replaced {
		lines = append(lines, "DB_BACKEND="+string(backend))
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
