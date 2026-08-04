package services

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
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
)

const (
	defaultBackupRetentionCount = 7
	defaultBackupIntervalHours  = 24
	backupFilePrefix            = "cineinsight-"
	backupFileSuffix            = ".dump"
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

func (err *DatabaseRestoreError) Error() string { return err.Err.Error() }
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
	dataDir string
	runner  postgresToolRunner
	now     func() time.Time
	mu      sync.Mutex
	running atomic.Bool
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
	status.BackupDirectory = s.resolveDirectory(settings.BackupDirectory)
	status.RetentionCount = normalizedBackupRetention(settings.BackupRetentionCount)
	status.IntervalHours = normalizedBackupInterval(settings.BackupIntervalHours)
	status.LastAttemptAt = settings.BackupLastAttemptAt
	status.LastSuccessAt = settings.BackupLastSuccessAt
	status.LastError = settings.BackupLastError
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

func (s *BackupService) ListBackups() ([]BackupFile, error) {
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil {
		return nil, fmt.Errorf("读取备份设置失败: %w", err)
	}
	return s.listBackupsIn(s.resolveDirectory(settings.BackupDirectory))
}

func (s *BackupService) CreateBackup(ctx context.Context) (*BackupFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running.Store(true)
	defer s.running.Store(false)
	return s.createBackupLocked(ctx)
}

func (s *BackupService) MaybeBackup(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	backupPath, err := s.copyVerifiedBackup(directory, request)
	if err != nil {
		return err
	}
	defer os.Remove(backupPath)
	config, env, err := postgresCommandEnvironment()
	if err != nil {
		return s.recordedFailure(err)
	}
	if err := s.runner.Run(ctx, "pg_restore", []string{"--list", backupPath}, env); err != nil {
		return s.recordedFailure(fmt.Errorf("备份文件校验失败，数据库未被修改: %w", err))
	}
	if beforeRestore != nil {
		if err := beforeRestore(); err != nil {
			if DatabaseRestoreRequiresRestart(err) {
				return err
			}
			return s.recordedFailure(fmt.Errorf("进入数据库维护模式失败，数据库未被修改: %w", err))
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
		return s.recordedFailure(fmt.Errorf("恢复前安全备份失败，数据库未被修改: %w", safetyErr))
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
		return s.recordedFailure(fmt.Errorf("恢复失败: %w", restoreErr))
	}
	if err := s.recordAttempt(true, nil); err != nil {
		return &DatabaseRestoreError{
			Committed: true,
			Fatal:     true,
			Err:       fmt.Errorf("数据库已恢复，但状态持久化失败；应用必须重启: %w", err),
		}
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
	finalPath := filepath.Join(directory, name)
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
	// 在发布前计算指纹，发布之后不再有可失败的读取步骤。
	fingerprint, err := hashFile(tempPath)
	if err != nil {
		return nil, fmt.Errorf("读取备份产物失败: %w", err)
	}
	// Reserve the final name exclusively, then atomically replace our own empty
	// placeholder. This preserves no-overwrite semantics without requiring hard
	// link support from user-selected removable or network filesystems.
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

func (s *BackupService) recordAttempt(success bool, operationErr error) error {
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
	return database.WithMaintenanceAccess(db).Model(&models.Settings{}).Where("id > 0").Updates(updates).Error
}

func (s *BackupService) recordedFailure(operationErr error) error {
	if statusErr := s.recordAttempt(false, operationErr); statusErr != nil {
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

func isBackupFileName(name string) bool {
	return backupFileNamePattern.MatchString(name)
}

var backupFileNamePattern = regexp.MustCompile(`^cineinsight-\d{8}-\d{6}\.\d{9}\.dump$`)

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
