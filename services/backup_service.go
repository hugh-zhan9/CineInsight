package services

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	defaultBackupRetentionCount = 7
	defaultBackupIntervalHours  = 24
	backupFilePrefix            = "cineinsight-"
	// 后缀按后端区分：Postgres 快照是 pg_dump 自定义格式，SQLite 快照是库文件。
	// 两者互不可用，靠后缀就能在恢复前判出来并明确拒绝，而不是交给工具报一句
	// 看不懂的错。
	backupFileSuffix       = ".dump"
	sqliteBackupFileSuffix = ".sqlite"
)

type BackupFile struct {
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at" ts_type:"string"`
	Fingerprint string    `json:"fingerprint"`
}

type BackupRestoreRequest struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Fingerprint string `json:"fingerprint"`
}

type DatabaseRestoreError struct {
	Committed bool
	Fatal     bool
	Err       error
}

// 恢复失败后必须重启的两类错误，消息开头带原因码（m2，与 relaunch_pending: 同口径），前端据此分类，
// 不再靠中文子串：
//   - restore_committed：数据已经恢复进库，是之后的重连、状态落库失败，应用需要重启；
//   - restore_fatal：没有恢复成功（正式库没被替换或恢复本身失败），但本进程已不能继续用库，应用需要重启。
const (
	DatabaseRestoreReasonCommitted = "restore_committed"
	DatabaseRestoreReasonFatal     = "restore_fatal"
)

func (err *DatabaseRestoreError) Error() string {
	switch {
	case err.Committed:
		return DatabaseRestoreReasonCommitted + ": " + err.Err.Error()
	case err.Fatal:
		return DatabaseRestoreReasonFatal + ": " + err.Err.Error()
	default:
		return err.Err.Error()
	}
}
func (err *DatabaseRestoreError) Unwrap() error { return err.Err }

func DatabaseRestoreRequiresRestart(err error) bool {
	var restoreErr *DatabaseRestoreError
	return errors.As(err, &restoreErr) && (restoreErr.Committed || restoreErr.Fatal)
}

type BackupStatus struct {
	Available        bool       `json:"available"`
	BackupAvailable  bool       `json:"backup_available"`
	RestoreAvailable bool       `json:"restore_available"`
	Reason           string     `json:"reason"`
	Running          bool       `json:"running"`
	BackupDirectory  string     `json:"backup_directory"`
	RetentionCount   int        `json:"retention_count"`
	IntervalHours    int        `json:"interval_hours"`
	LastAttemptAt    *time.Time `json:"last_attempt_at" ts_type:"string"`
	LastSuccessAt    *time.Time `json:"last_success_at" ts_type:"string"`
	LastError        string     `json:"last_error"`
}

type postgresToolRunner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, name string, args []string, env []string) error
}

type execPostgresToolRunner struct{}

func (execPostgresToolRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// stderrTailLimit 限制随错误返回的子进程 stderr 字节数，避免异常输出无限增长。
const stderrTailLimit = 1000

// tailWriter 只保留最近写入的字节。
type tailWriter struct{ tail []byte }

func (w *tailWriter) Write(p []byte) (int, error) {
	w.tail = append(w.tail, p...)
	if len(w.tail) > stderrTailLimit {
		w.tail = w.tail[len(w.tail)-stderrTailLimit:]
	}
	return len(p), nil
}

func (execPostgresToolRunner) Run(ctx context.Context, name string, args []string, env []string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), env...)
	// 口令只经 PGPASSWORD 环境变量传递，pg 工具的 stderr 不会回显口令。
	stderr := &tailWriter{}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if detail := strings.TrimSpace(string(stderr.tail)); detail != "" {
			return fmt.Errorf("%s 执行失败: %w: %s", name, err, detail)
		}
		return fmt.Errorf("%s 执行失败: %w", name, err)
	}
	return nil
}

type BackupService struct {
	dataDir    string
	runner     postgresToolRunner
	now        func() time.Time
	mu         sync.Mutex
	running    atomic.Bool
	registryMu sync.Mutex
	registry   *BackgroundTaskRegistry
	notifier   DesktopNotifier
	// restoreCopy 是 SQLite 恢复把快照写进临时库文件的那一步，测试用它模拟复制中途出错；
	// 为空时用 io.Copy。
	restoreCopy func(dst io.Writer, src io.Reader) (int64, error)
	// diskFree 返回目录所在卷对非特权用户可用的字节数，SQLite 恢复复制临时库之前用它检查空间；
	// 测试用它模拟空间不足。为空时用 enhancementDiskFree（同一个 statfs 实现）。
	diskFree func(path string) (uint64, error)
}

// ErrBackupDuringMaintenance：维护围栏生效期间（恢复备份、切换后端、切换后的待重启）拒绝手动备份。
// 那时库被围栏挡着，备份状态也不能写进正在被迁移的源库。
var ErrBackupDuringMaintenance = errors.New("数据库正在恢复或切换后端，暂时不能备份")

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
// 登记范围与 running 标记一致：手动备份与启动时的自动备份，不含恢复流程里的
// 安全备份（那时数据库已被围栏挡住，界面也不再是"后台任务"的语境）。
func (s *BackupService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.registryMu.Lock()
	s.registry = registry
	s.registryMu.Unlock()
}

func (s *BackupService) taskRegistry() *BackgroundTaskRegistry {
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	return s.registry
}

// SetDesktopNotifier 接入桌面通知（D-013）。备份只有失败才通知：
// 成功的备份用户不需要知道，失败了才必须知道。
func (s *BackupService) SetDesktopNotifier(notifier DesktopNotifier) {
	s.registryMu.Lock()
	s.notifier = notifier
	s.registryMu.Unlock()
}

func (s *BackupService) desktopNotifier() DesktopNotifier {
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	return s.notifier
}

// backupDue 判断距上次成功备份是否已到间隔。不碰任何共享状态，可在锁外调用。
func (s *BackupService) backupDue() (bool, error) {
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil {
		return false, err
	}
	interval := normalizedBackupInterval(settings.BackupIntervalHours)
	if interval == 0 {
		return false, nil
	}
	if settings.BackupLastSuccessAt != nil && s.now().Sub(*settings.BackupLastSuccessAt) < time.Duration(interval)*time.Hour {
		return false, nil
	}
	return true, nil
}

func NewBackupService(dataDir string) *BackupService {
	return &BackupService{
		dataDir: dataDir,
		runner:  execPostgresToolRunner{},
		now:     time.Now,
	}
}

func (s *BackupService) GetStatus() BackupStatus {
	status := BackupStatus{Running: s.running.Load()}
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil {
		status.Reason = "无法读取备份设置"
		return status
	}
	return s.statusFromSettings(settings)
}

// HealthStatus reports read failures separately so diagnostic metrics do not become false zeros.
func (s *BackupService) HealthStatus(ctx context.Context) (BackupStatus, error) {
	settings, err := (&SettingsService{}).GetSettingsContext(ctx)
	if err != nil {
		return BackupStatus{}, err
	}
	return s.statusFromSettings(settings), nil
}

func (s *BackupService) statusFromSettings(settings *models.Settings) BackupStatus {
	status := BackupStatus{Running: s.running.Load()}
	status.BackupDirectory = s.resolveDirectory(settings.BackupDirectory)
	status.RetentionCount = normalizedBackupRetention(settings.BackupRetentionCount)
	status.IntervalHours = normalizedBackupInterval(settings.BackupIntervalHours)
	status.LastAttemptAt = settings.BackupLastAttemptAt
	status.LastSuccessAt = settings.BackupLastSuccessAt
	status.LastError = settings.BackupLastError
	// 可用性按后端判（D-PC54）：SQLite 的备份与恢复都只靠 VACUUM INTO 与文件替换，
	// 永远可用；只有 PostgreSQL 需要 pg_dump / pg_restore 与连接配置。此前一律按 PG
	// 判，默认的 SQLite 安装在设置页里备份、恢复两个按钮始终是灰的（APP-01）。
	if database.ActiveBackend() == database.BackendSQLite {
		status.BackupAvailable = true
		status.RestoreAvailable = true
		status.Available = true
		return status
	}
	_, dumpErr := s.runner.LookPath("pg_dump")
	_, restoreErr := s.runner.LookPath("pg_restore")
	status.BackupAvailable = dumpErr == nil && restoreErr == nil
	status.RestoreAvailable = restoreErr == nil && dumpErr == nil
	status.Available = status.BackupAvailable && status.RestoreAvailable
	if dumpErr != nil || restoreErr != nil {
		missing := make([]string, 0, 2)
		if dumpErr != nil {
			missing = append(missing, "pg_dump")
		}
		if restoreErr != nil {
			missing = append(missing, "pg_restore")
		}
		status.Reason = "未找到 PostgreSQL 客户端工具：" + strings.Join(missing, "、")
		return status
	}
	if _, err := database.PostgresCLIConfigFromEnv(); err != nil {
		status.Available = false
		status.BackupAvailable = false
		status.RestoreAvailable = false
		status.Reason = "PostgreSQL 连接配置不完整"
	}
	return status
}

// RevealBackupDirectory 在访达中打开实际使用的备份目录（D-PC54），与 GetStatus 返回的
// backup_directory 是同一个解析结果。目录还不存在时先建出来（与备份时同样的 0700），
// 否则第一次备份之前点「在访达中显示」只会失败。错误文案不带绝对路径（G-3）。
func (s *BackupService) RevealBackupDirectory() error {
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil {
		return fmt.Errorf("读取备份设置失败: %w", err)
	}
	directory := s.resolveDirectory(settings.BackupDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("备份目录无法创建，请检查目录设置与权限")
	}
	if err := openWithDefaultFn(directory, true); err != nil {
		return errors.New("无法在访达中打开备份目录")
	}
	return nil
}

// periodicBackupInterval 是定时备份的检查周期（D-PC56）。每次只是调用 MaybeBackup，
// 真正是否备份仍由「自动备份间隔」设置决定，所以周期取一小时即可。
const periodicBackupInterval = time.Hour

// StartPeriodic 让应用常驻期间也能按间隔自动备份（D-PC56 / APP-09）：此前只在启动时
// 检查一次，常驻数天的应用永远等不到下一次备份。
//
// 它**阻塞**到 ctx 取消为止，由调用方放进自己的 goroutine 并登记到 backupWG——这样退出时
// 取消 ctx 再 Wait，就不会有一轮备份与关库赛跑。启动时那一次立即检查仍由 startup 负责，
// 这里只管之后每小时一次。
func (s *BackupService) StartPeriodic(ctx context.Context) {
	s.runPeriodic(ctx, periodicBackupInterval)
}

func (s *BackupService) runPeriodic(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.periodicTick(ctx); err != nil && ctx.Err() == nil {
				log.Printf("Periodic database backup failed err=%v", err)
			}
		}
	}
}

// periodicTick 是定时器的一拍：维护模式（恢复备份、切换后端）期间直接跳过——那时库被
// 围栏挡着，调用只会得到一条维护中的错误，何况恢复流程自己会做安全备份。
func (s *BackupService) periodicTick(ctx context.Context) (bool, error) {
	if database.MaintenanceActive() {
		return false, nil
	}
	return s.MaybeBackup(ctx)
}

func (s *BackupService) ListBackups() ([]BackupFile, error) {
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil {
		return nil, fmt.Errorf("读取备份设置失败: %w", err)
	}
	return s.listBackupsIn(s.resolveDirectory(settings.BackupDirectory))
}

func (s *BackupService) CreateBackup(ctx context.Context) (*BackupFile, error) {
	// 判定与后续操作之间围栏仍可能生效：那时的读写会被屏障拒绝，状态也不会经维护通道
	// 写进源库（recordAttempt 不带维护通道），这里只是给用户一句说得清的话。
	if database.MaintenanceActive() {
		return nil, ErrBackupDuringMaintenance
	}
	// 登记表的变化回调在服务锁之外跑：回调会走到空闲门与前端事件，
	// 把它关在备份锁里等于给后来者埋一个隐形的锁序。
	registry := s.taskRegistry()
	registry.Begin(BackgroundTaskBackup)
	defer registry.End(BackgroundTaskBackup)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running.Store(true)
	defer s.running.Store(false)
	return s.createBackupLocked(ctx)
}

func (s *BackupService) MaybeBackup(ctx context.Context) (bool, error) {
	// 先在锁外判一次"到点了没"：不到点就不登记，免得角标为一次空转闪一下。
	due, err := s.backupDue()
	if err != nil || !due {
		return false, err
	}
	registry := s.taskRegistry()
	registry.Begin(BackgroundTaskBackup)
	defer registry.End(BackgroundTaskBackup)
	s.mu.Lock()
	defer s.mu.Unlock()
	// 锁内复查，保持原语义：并发调用时后一个要看到前一个刚写的成功时间。
	due, err = s.backupDue()
	if err != nil || !due {
		return false, err
	}
	s.running.Store(true)
	defer s.running.Store(false)
	backup, err := s.createBackupLocked(ctx)
	return backup != nil, err
}

func (s *BackupService) RestoreBackup(ctx context.Context, request BackupRestoreRequest) error {
	return s.RestoreBackupWithLifecycle(ctx, request, nil, nil)
}

func (s *BackupService) RestoreBackupWithLifecycle(
	ctx context.Context,
	request BackupRestoreRequest,
	beforeRestore func() error,
	reconnect func() error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running.Store(true)
	defer s.running.Store(false)

	settings, err := (&SettingsService{}).GetSettings()
	if err != nil {
		return fmt.Errorf("读取备份设置失败: %w", err)
	}
	directory := s.resolveDirectory(settings.BackupDirectory)
	backend := database.ActiveBackend()
	// 先判快照与后端是否匹配：此时还没进维护模式、还没做安全备份，
	// 拒绝的代价最小，用户拿到的也是一句说得清的话。
	if !backupSuffixMatchesBackend(request.Name, backend) {
		return s.recordedRestoreFailure(fmt.Errorf(
			"备份文件与当前数据库后端不匹配，数据库未被修改：当前是 %s，而 %s 是另一种后端的快照",
			backend, request.Name))
	}
	backupPath, err := s.copyVerifiedBackup(directory, request)
	if err != nil {
		return err
	}
	defer os.Remove(backupPath)
	if backend == database.BackendSQLite {
		return s.restoreSQLite(ctx, directory, backupPath, request, settings, beforeRestore)
	}
	config, env, err := postgresCommandEnvironment()
	if err != nil {
		return s.recordedRestoreFailure(err)
	}
	if err := s.runner.Run(ctx, "pg_restore", []string{"--list", backupPath}, env); err != nil {
		return s.recordedRestoreFailure(fmt.Errorf("备份文件校验失败，数据库未被修改: %w", err))
	}
	if beforeRestore != nil {
		if err := beforeRestore(); err != nil {
			if DatabaseRestoreRequiresRestart(err) {
				return err
			}
			return s.recordedRestoreFailure(fmt.Errorf("进入数据库维护模式失败，数据库未被修改: %w", err))
		}
	}
	// 写入围栏生效后才做安全备份，保证围栏前落库的写入都包含在安全备份里。
	// 此时数据库连接可能已被围栏关闭，安全备份只能走纯文件系统与 pg 工具路径；
	// 失败时必须先通过 reconnect 退出维护模式再返回。
	if safetyErr := s.performSafetyBackup(ctx, directory, normalizedBackupRetention(settings.BackupRetentionCount), request.Name); safetyErr != nil {
		if reconnect != nil {
			if reconnectErr := reconnect(); reconnectErr != nil {
				return &DatabaseRestoreError{
					Fatal: true,
					Err:   fmt.Errorf("恢复前安全备份失败，且退出数据库维护模式失败，应用必须重启: %w", errors.Join(safetyErr, reconnectErr)),
				}
			}
		}
		return s.recordedRestoreFailure(fmt.Errorf("恢复前安全备份失败，数据库未被修改: %w", safetyErr))
	}
	args := []string{
		"--clean", "--if-exists", "--no-owner", "--no-privileges",
		"--exit-on-error", "--single-transaction", "--dbname", config.Database,
		backupPath,
	}
	restoreErr := s.runner.Run(ctx, "pg_restore", args, env)
	if reconnect != nil {
		if err := reconnect(); err != nil {
			return &DatabaseRestoreError{
				Committed: restoreErr == nil,
				Fatal:     true,
				Err:       fmt.Errorf("恢复后重新连接数据库失败，应用必须重启: %w", err),
			}
		}
	}
	if restoreErr != nil {
		return s.recordedRestoreFailure(fmt.Errorf("恢复失败: %w", restoreErr))
	}
	if err := s.recordRestoreAttempt(true, nil); err != nil {
		return &DatabaseRestoreError{
			Committed: true,
			Fatal:     true,
			Err:       fmt.Errorf("数据库已恢复，但状态持久化失败；应用必须重启: %w", err),
		}
	}
	return nil
}

// restoreSQLite 用快照替换当前库文件。
//
// 与 Postgres 那条路径的关键差别：那边是把数据灌回同一个库（连接可以保留并重连），
// 这边是换掉库文件本身。正在使用的句柄不能安全地指向一个被换掉的文件，所以这里
// 不重连；恢复成功后由 App 走与 Postgres 成功时相同的「提示成功并退出」收尾。
//
// 顺序：清扫遗留临时库 → 校验 → 安全快照 → 查空间并备好临时库 → 进维护模式围栏、关句柄 →
// 换文件（D-PC54）。安全快照必须在围栏**之前**：SQLite 的快照是本进程经 database.DB 执行的
// VACUUM INTO，围栏生效后它会被维护屏障拒绝，何况进入维护模式时连接已被关闭——此前的顺序让
// SQLite 恢复必然失败并把应用卡进「必须重启」的错误态（APP-01）。代价是快照与围栏之间落库的
// 零星后台写入不在安全快照里；它们随后也会被恢复覆盖，安全快照要保的是「恢复前那一刻」。
//
// 临时库同样在关句柄**之前**备好（m3）：复制、fsync、改权限、关闭都可能失败（最常见的是磁盘写满），
// 那时库还开着、也没进维护模式，失败只是一次普通的恢复失败；句柄关掉之后只剩删边车、rename、
// 目录 fsync 这几步，失败才需要重启。
func (s *BackupService) restoreSQLite(
	ctx context.Context,
	directory string,
	backupPath string,
	request BackupRestoreRequest,
	settings *models.Settings,
	beforeRestore func() error,
) error {
	livePath := database.SQLitePath(s.dataDir)
	// 上一次恢复中途崩溃留下的临时库有一整个库文件那么大，先清掉再算空间（m4）。
	// 恢复持 s.mu 串行，此刻没有别的恢复在写临时库。
	database.SweepSQLiteRestoreTemps(livePath)
	if err := verifySQLiteSnapshot(backupPath); err != nil {
		return s.recordedRestoreFailure(fmt.Errorf("备份文件校验失败，数据库未被修改: %w", err))
	}
	// 失败即中止：此时还没进维护模式，库原样可用，按普通失败返回（App 走恢复失败续跑）。
	if err := s.performSafetyBackup(ctx, directory, normalizedBackupRetention(settings.BackupRetentionCount), request.Name); err != nil {
		return s.recordedRestoreFailure(fmt.Errorf("恢复前安全备份失败，数据库未被修改: %w", err))
	}
	tempPath, err := s.prepareSQLiteRestoreLibrary(backupPath, livePath)
	if err != nil {
		return s.recordedRestoreFailure(fmt.Errorf("准备恢复用的库文件失败，数据库未被修改: %w", err))
	}
	swapped := false
	defer func() {
		if !swapped {
			_ = os.Remove(tempPath)
		}
	}()

	if beforeRestore != nil {
		if err := beforeRestore(); err != nil {
			if DatabaseRestoreRequiresRestart(err) {
				return err
			}
			return s.recordedRestoreFailure(fmt.Errorf("进入数据库维护模式失败，数据库未被修改: %w", err))
		}
	}

	// 关掉句柄再换文件。句柄一关，本进程就不能再用这个库，之后任何一步失败都只能要求重启；
	// 换文件本身是原子的，失败时正式库文件原样留着，重启后仍是恢复前的库。
	if database.DB != nil {
		if sqlDB, err := database.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	if err := swapSQLiteLibrary(tempPath, livePath); err != nil {
		if errors.Is(err, errSQLiteUncheckpointedWAL) {
			return &DatabaseRestoreError{
				Fatal: true,
				Err:   fmt.Errorf("库文件旁还有尚未写回主库的 WAL 日志（可能有未落盘的提交），为免丢数据已中止恢复，库文件未被替换；请重启应用后再恢复: %w", err),
			}
		}
		return &DatabaseRestoreError{
			Fatal: true,
			Err:   fmt.Errorf("替换数据库文件失败，库文件未被替换，应用必须重启（恢复前的安全快照在备份目录里）: %w", err),
		}
	}
	swapped = true
	// 恢复成功就是成功：返回 nil，App 与 Postgres 成功时一样提示「恢复成功，应用将自动
	// 退出」并走内部退出。此前这里返回 Committed+Fatal 的错误，前端会把一次成功的恢复
	// 显示成「数据库恢复失败：……必须重启」。旧句柄已关，本次尝试的状态不写库——
	// 恢复出来的库带着快照时刻的备份状态，重启后由自动备份按间隔自行续上。
	return nil
}

// verifySQLiteSnapshot 确认快照确实是一个能打开、且含本应用表的 SQLite 库。
// 只读打开，不做迁移——迁移会改动快照本身。
func verifySQLiteSnapshot(path string) error {
	db, err := gorm.Open(sqlite.Open(path+"?mode=ro"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("无法作为 SQLite 库打开: %w", err)
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	if !db.Migrator().HasTable(&models.Video{}) {
		return errors.New("快照里没有 videos 表，不像是本应用的备份")
	}
	return nil
}

// prepareSQLiteRestoreLibrary 在库文件同目录备好替换用的临时库（I-2、m3），返回它的路径。
// 调用时连接还开着、也没进维护模式：这里的任何失败都只是普通的恢复失败，临时文件已清掉。
//
// 先查库目录所在卷的剩余空间，至少要放得下一份快照；不够就直接失败，不去写一个注定写不完的文件。
// 之后复制 → fsync → 沿用原库文件的权限（rename 换的是整个目录项，临时文件的 0600 会跟着过去）→ 关闭。
func (s *BackupService) prepareSQLiteRestoreLibrary(snapshotPath, livePath string) (string, error) {
	directory := filepath.Dir(livePath)
	source, err := os.Open(snapshotPath)
	if err != nil {
		return "", fmt.Errorf("打开恢复快照失败: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return "", fmt.Errorf("读取恢复快照失败: %w", err)
	}
	diskFree := s.diskFree
	if diskFree == nil {
		diskFree = enhancementDiskFree
	}
	free, err := diskFree(directory)
	if err != nil {
		return "", fmt.Errorf("无法检查库文件所在磁盘的剩余空间: %w", err)
	}
	if need := uint64(max(info.Size(), 0)); free < need {
		return "", fmt.Errorf("库文件所在磁盘剩余空间不足（需要约 %s，可用 %s）",
			formatEnhancementBytes(need), formatEnhancementBytes(free))
	}
	temp, err := os.CreateTemp(directory, database.SQLiteRestoreTempPattern(livePath))
	if err != nil {
		return "", fmt.Errorf("创建临时库文件失败: %w", err)
	}
	tempPath := temp.Name()
	ready := false
	defer func() {
		if !ready {
			_ = temp.Close()
			_ = os.Remove(tempPath)
		}
	}()
	copyFn := s.restoreCopy
	if copyFn == nil {
		copyFn = io.Copy
	}
	if _, err := copyFn(temp, source); err != nil {
		return "", fmt.Errorf("写入临时库文件失败: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return "", fmt.Errorf("落盘临时库文件失败: %w", err)
	}
	if liveInfo, err := os.Stat(livePath); err == nil {
		if err := temp.Chmod(liveInfo.Mode().Perm()); err != nil {
			return "", fmt.Errorf("设置临时库文件权限失败: %w", err)
		}
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("关闭临时库文件失败: %w", err)
	}
	ready = true
	return tempPath, nil
}

// errSQLiteUncheckpointedWAL：关掉连接之后库文件旁的 -wal 仍然非空。
var errSQLiteUncheckpointedWAL = errors.New("WAL 日志非空")

// swapSQLiteLibrary 用备好的临时库原子地替换正式库文件。调用前连接必须已经关闭；这里只做
// 删边车 → rename 覆盖正式库文件 → fsync 目录。rename 之前任何一步失败，正式库文件都原样留着。
//
// 连接关闭时 SQLite 会做完 checkpoint 并删掉边车，所以边车通常不存在；残留的边车若留到 rename
// 之后，会被当成新库的一部分而让内容对不上，所以在 rename 之前删。但 -wal 非空说明还有提交没写回
// 主库（例如还有别的连接开着这个库）：删掉它就是丢数据，此时中止、不动正式库（m4）。
func swapSQLiteLibrary(tempPath, livePath string) error {
	walPath := livePath + "-wal"
	if info, err := os.Lstat(walPath); err == nil {
		if info.Size() > 0 {
			return errSQLiteUncheckpointedWAL
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 WAL 边车文件失败: %w", err)
	}
	for _, sidecar := range []string{walPath, livePath + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理 WAL 边车文件失败: %w", err)
		}
	}
	if err := replaceSubtitleFileAtomically(tempPath, livePath); err != nil {
		return fmt.Errorf("替换库文件失败: %w", err)
	}
	// 目录项已经换好；目录落盘失败只影响断电时的持久性（最坏回到恢复前的库），不否定这次替换。
	if err := syncSubtitleParentDirectory(filepath.Dir(livePath)); err != nil {
		log.Printf("SQLite 恢复后同步库目录失败 err=%v", err)
	}
	return nil
}

func (s *BackupService) createBackupLocked(ctx context.Context) (*BackupFile, error) {
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil {
		return nil, fmt.Errorf("读取备份设置失败: %w", err)
	}
	directory := s.resolveDirectory(settings.BackupDirectory)
	backup, err := s.performBackup(ctx, directory)
	if err != nil {
		// 失败原因不进通知文案：pg_dump 的报错里带备份目录的绝对路径。
		notifyDesktop(s.desktopNotifier(), "数据库备份失败", "备份未完成，可在设置页的数据库备份分区查看原因")
		return nil, s.recordedFailure(err)
	}
	// 转储已成功，先落成功状态；随后的轮转问题只作为告警，不否定本次成功。
	if err := s.recordAttempt(true, nil); err != nil {
		return backup, fmt.Errorf("备份已创建，但状态持久化失败: %w", err)
	}
	if err := s.rotateBackups(directory, normalizedBackupRetention(settings.BackupRetentionCount), ""); err != nil {
		warn := fmt.Errorf("备份已创建，但轮转失败: %w", err)
		if statusErr := s.recordAttempt(false, warn); statusErr != nil {
			return backup, errors.Join(warn, fmt.Errorf("记录备份状态失败: %w", statusErr))
		}
		return backup, warn
	}
	return backup, nil
}

// performSafetyBackup 供恢复流程在写入围栏生效后调用，只走文件系统与 pg 工具，
// 不访问应用数据库（此时连接可能已关闭）。轮转时保护待恢复的备份文件。
func (s *BackupService) performSafetyBackup(ctx context.Context, directory string, retain int, protectedBackup string) error {
	if _, err := s.performBackup(ctx, directory); err != nil {
		return err
	}
	return s.rotateBackups(directory, retain, protectedBackup)
}

// performBackup 生成并校验一份新的转储文件。它不读写应用数据库，因此在数据库
// 维护围栏生效期间也可以安全执行；状态记录由调用方负责。
func (s *BackupService) performBackup(ctx context.Context, directory string) (*BackupFile, error) {
	if database.ActiveBackend() == database.BackendSQLite {
		return s.performSQLiteBackup(directory)
	}
	return s.performPostgresBackup(ctx, directory)
}

// performSQLiteBackup 用 VACUUM INTO 写一份单文件一致性快照。
//
// 选它而不是复制库文件：VACUUM INTO 在事务边界上产出一致快照，不需要停写，
// 也不受 WAL 的 -wal / -shm 边车文件影响——直接拷贝库文件会漏掉尚未 checkpoint
// 的 WAL 内容，拷出来的东西可能根本打不开。
func (s *BackupService) performSQLiteBackup(directory string) (*BackupFile, error) {
	if database.DB == nil {
		return nil, errors.New("数据库未初始化")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("创建备份目录失败: %w", err)
	}
	s.sweepStaleTempFiles(directory)

	now := s.now()
	name := backupFilePrefix + now.Format("20060102-150405.000000000") + sqliteBackupFileSuffix
	// VACUUM INTO 要求目标文件不存在，所以这里只取一个唯一名字，不预先建文件。
	tempPath := filepath.Join(directory, fmt.Sprintf(".cineinsight-backup-%d.tmp", now.UnixNano()))
	defer os.Remove(tempPath)

	if err := database.DB.Exec("VACUUM INTO ?", tempPath).Error; err != nil {
		return nil, fmt.Errorf("写出 SQLite 快照失败: %w", err)
	}
	return s.publishBackupArtifact(directory, tempPath, name, now)
}

func (s *BackupService) performPostgresBackup(ctx context.Context, directory string) (*BackupFile, error) {
	if _, err := s.runner.LookPath("pg_dump"); err != nil {
		return nil, errors.New("未找到 pg_dump")
	}
	if _, err := s.runner.LookPath("pg_restore"); err != nil {
		return nil, errors.New("未找到 pg_restore，无法验证备份产物")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("创建备份目录失败: %w", err)
	}
	s.sweepStaleTempFiles(directory)
	config, env, err := postgresCommandEnvironment()
	if err != nil {
		return nil, err
	}
	now := s.now()
	name := backupFilePrefix + now.Format("20060102-150405.000000000") + backupFileSuffix
	tempFile, err := os.CreateTemp(directory, ".cineinsight-backup-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("创建备份临时文件失败: %w", err)
	}
	tempPath := tempFile.Name()
	if closeErr := tempFile.Close(); closeErr != nil {
		_ = os.Remove(tempPath)
		return nil, closeErr
	}
	defer os.Remove(tempPath)
	args := []string{"--format=custom", "--no-owner", "--no-privileges", "--file", tempPath, "--dbname", config.Database}
	if err := s.runner.Run(ctx, "pg_dump", args, env); err != nil {
		return nil, err
	}
	if err := s.runner.Run(ctx, "pg_restore", []string{"--list", tempPath}, env); err != nil {
		return nil, fmt.Errorf("备份产物校验失败: %w", err)
	}
	return s.publishBackupArtifact(directory, tempPath, name, now)
}

// publishBackupArtifact 把临时产物原子地发布成正式备份文件。两个后端共用：
// 校验非空、收紧权限、先算指纹再发布（发布之后不再有可失败的读取步骤），
// 用"独占创建占位 + rename"保证不覆盖同名文件，同时不依赖用户所选磁盘的硬链接支持。
func (s *BackupService) publishBackupArtifact(directory, tempPath, name string, now time.Time) (*BackupFile, error) {
	finalPath := filepath.Join(directory, name)
	info, err := os.Stat(tempPath)
	if err != nil || info.Size() == 0 {
		if err == nil {
			err = errors.New("备份产物为空")
		}
		return nil, err
	}
	if err := os.Chmod(tempPath, 0600); err != nil {
		return nil, err
	}
	fingerprint, err := hashFile(tempPath)
	if err != nil {
		return nil, fmt.Errorf("读取备份产物失败: %w", err)
	}
	placeholder, err := os.OpenFile(finalPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("发布备份文件失败: %w", err)
	}
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(finalPath)
		return nil, fmt.Errorf("发布备份文件失败: %w", err)
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(finalPath)
		return nil, fmt.Errorf("发布备份文件失败: %w", err)
	}
	return &BackupFile{Name: name, Size: info.Size(), CreatedAt: now, Fingerprint: fingerprint}, nil
}

// staleBackupTempFileMaxAge 之前遗留的临时文件视为崩溃残留。
const staleBackupTempFileMaxAge = 24 * time.Hour

// sweepStaleTempFiles 清理崩溃遗留的备份/恢复临时文件。清理属于机会性回收，
// 单个文件删除失败不阻断备份流程。
func (s *BackupService) sweepStaleTempFiles(directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	cutoff := s.now().Add(-staleBackupTempFileMaxAge)
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !isBackupTempFileName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(directory, entry.Name()))
	}
}

func isBackupTempFileName(name string) bool {
	if !strings.HasSuffix(name, ".tmp") {
		return false
	}
	return strings.HasPrefix(name, ".cineinsight-backup-") || strings.HasPrefix(name, ".cineinsight-restore-")
}

func (s *BackupService) resolveDirectory(configured string) string {
	if strings.TrimSpace(configured) == "" {
		return filepath.Join(s.dataDir, "backups")
	}
	return filepath.Clean(configured)
}

// listBackupsIn 附带指纹供前端恢复确认流程使用；只需要文件名与时间的路径
// （如轮转）应使用 scanBackupEntries，避免对每个转储做整文件哈希。
func (s *BackupService) listBackupsIn(directory string) ([]BackupFile, error) {
	entries, err := s.scanBackupEntries(directory)
	if err != nil {
		return nil, err
	}
	backups := make([]BackupFile, 0, len(entries))
	for _, entry := range entries {
		fingerprint, err := hashFile(filepath.Join(directory, entry.name))
		if err != nil {
			continue
		}
		backups = append(backups, BackupFile{Name: entry.name, Size: entry.size, CreatedAt: entry.createdAt, Fingerprint: fingerprint})
	}
	return backups, nil
}

type backupDirEntry struct {
	name      string
	size      int64
	createdAt time.Time
}

func (s *BackupService) scanBackupEntries(directory string) ([]backupDirEntry, error) {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return []backupDirEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取备份目录失败: %w", err)
	}
	backups := make([]backupDirEntry, 0, len(entries))
	for _, entry := range entries {
		if !isBackupFileName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		backups = append(backups, backupDirEntry{name: entry.Name(), size: info.Size(), createdAt: info.ModTime()})
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].createdAt.Equal(backups[j].createdAt) {
			return backups[i].name > backups[j].name
		}
		return backups[i].createdAt.After(backups[j].createdAt)
	})
	return backups, nil
}

func (s *BackupService) rotateBackups(directory string, retain int, protectedBackup string) error {
	backups, err := s.scanBackupEntries(directory)
	if err != nil {
		return err
	}
	kept := 0
	for _, backup := range backups {
		if kept < retain || backup.name == protectedBackup {
			kept++
			continue
		}
		if err := os.Remove(filepath.Join(directory, backup.name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *BackupService) copyVerifiedBackup(directory string, request BackupRestoreRequest) (string, error) {
	if filepath.Base(request.Name) != request.Name || !isBackupFileName(request.Name) || request.Size <= 0 || request.Fingerprint == "" {
		return "", errors.New("无效的备份文件名")
	}
	sourcePath := filepath.Join(directory, request.Name)
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return "", errors.New("备份文件不存在或无法读取")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != request.Size {
		return "", errors.New("备份文件自确认后已发生变化")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", errors.New("备份文件无法打开")
	}
	defer source.Close()
	temp, err := os.CreateTemp(directory, ".cineinsight-restore-*.tmp")
	if err != nil {
		return "", fmt.Errorf("创建恢复临时文件失败: %w", err)
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		_ = temp.Close()
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hash), source)
	if err != nil || written != request.Size {
		return "", errors.New("复制待恢复备份失败")
	}
	if err := temp.Sync(); err != nil {
		return "", fmt.Errorf("落盘恢复临时文件失败: %w", err)
	}
	if err := temp.Chmod(0600); err != nil {
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != request.Fingerprint {
		return "", errors.New("备份文件自确认后已发生变化")
	}
	cleanup = false
	return tempPath, nil
}

// recordAttempt 记下一次备份的结果，走普通通道：维护围栏生效时（恢复备份、切换后端）被屏障拒绝。
// 手动与自动备份只用它——切换后端期间经维护通道写状态，写进的是正在被迁移的源库，那次写入
// 不会出现在新库里。
func (s *BackupService) recordAttempt(success bool, operationErr error) error {
	return s.writeAttempt(false, success, operationErr)
}

// recordRestoreAttempt 是恢复流程专用的记录：恢复自己立的围栏挡住了普通写入，经维护通道记下
// 本次恢复的结果。恢复与切换后端互斥（App 的 restoreMu），这时的围栏只可能是恢复自己的。
func (s *BackupService) recordRestoreAttempt(success bool, operationErr error) error {
	return s.writeAttempt(true, success, operationErr)
}

func (s *BackupService) writeAttempt(maintenanceAccess, success bool, operationErr error) error {
	now := s.now()
	updates := map[string]any{"backup_last_attempt_at": &now}
	if success {
		updates["backup_last_success_at"] = &now
		updates["backup_last_error"] = ""
	} else if operationErr != nil {
		message := operationErr.Error()
		if len(message) > 1000 {
			message = message[:1000]
		}
		updates["backup_last_error"] = message
	}
	db := database.DB
	if db == nil {
		return errors.New("数据库连接不可用")
	}
	if maintenanceAccess {
		db = database.WithMaintenanceAccess(db)
	}
	return db.Model(&models.Settings{}).Where("id > 0").Updates(updates).Error
}

func (s *BackupService) recordedFailure(operationErr error) error {
	return joinRecordError(operationErr, s.recordAttempt(false, operationErr))
}

func (s *BackupService) recordedRestoreFailure(operationErr error) error {
	return joinRecordError(operationErr, s.recordRestoreAttempt(false, operationErr))
}

func joinRecordError(operationErr, statusErr error) error {
	if statusErr != nil {
		return errors.Join(operationErr, fmt.Errorf("记录备份状态失败: %w", statusErr))
	}
	return operationErr
}

func postgresCommandEnvironment() (database.PostgresCLIConfig, []string, error) {
	config, err := database.PostgresCLIConfigFromEnv()
	if err != nil {
		return database.PostgresCLIConfig{}, nil, fmt.Errorf("PostgreSQL 连接配置不完整: %w", err)
	}
	return config, config.Environment(), nil
}

func normalizedBackupRetention(value int) int {
	if value <= 0 {
		return defaultBackupRetentionCount
	}
	if value > 100 {
		return 100
	}
	return value
}

func normalizedBackupInterval(value int) int {
	if value < 0 {
		return defaultBackupIntervalHours
	}
	if value > 24*365 {
		return 24 * 365
	}
	return value
}

// isBackupFileName 认两种后缀，列表因此能同时看见历史 Postgres 快照与 SQLite 快照。
// 能不能用是恢复时按后端判的（见 backupSuffixMatchesBackend），不在这里筛。
func isBackupFileName(name string) bool {
	return backupFileNamePattern.MatchString(name)
}

var backupFileNamePattern = regexp.MustCompile(`^cineinsight-\d{8}-\d{6}\.\d{9}\.(dump|sqlite)$`)

// backupSuffixMatchesBackend 判断快照能不能在当前后端上恢复。
//
// 两种快照互不可用：Postgres 的是 pg_dump 自定义格式，SQLite 的是库文件。
// 明确拒绝而不是交给工具去试——pg_restore 面对一个 SQLite 文件只会报一句
// 用户看不懂的话，而且此时维护模式围栏可能已经生效。
func backupSuffixMatchesBackend(name string, backend database.Backend) bool {
	if backend == database.BackendSQLite {
		return strings.HasSuffix(name, sqliteBackupFileSuffix)
	}
	return strings.HasSuffix(name, backupFileSuffix)
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
