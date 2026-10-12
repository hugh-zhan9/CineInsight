package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"video-master/database"
)

// 备份校验（D-DH-03，4.2）：重算哈希与清单比对、逐条读文件包、SQLite 快照只读打开做完整性检查并核对行数、
// 表结构是否为当前版本所认得。结果按备份名记在数据目录的 backup-verifications.json 里，
// 设置页的列表与体检的「备份」项读它。

const (
	BackupVerifyLevelFull    = "full"
	BackupVerifyLevelArchive = "archive"
	BackupVerifyLevelLegacy  = "legacy"
	backupVerificationsFile  = "backup-verifications.json"
)

type BackupVerification struct {
	Name             string    `json:"name"`
	Level            string    `json:"level"`
	OK               bool      `json:"ok"`
	HashOK           bool      `json:"hash_ok"`
	FilesOK          bool      `json:"files_ok"`
	QuickCheck       string    `json:"quick_check"`
	RowsOK           bool      `json:"rows_ok"`
	SchemaCompatible bool      `json:"schema_compatible"`
	Problems         []string  `json:"problems"`
	VerifiedAt       time.Time `json:"verified_at" ts_type:"string"`
}

// VerifyDatabaseBackup 校验一份备份，在 backup 任务键下运行，与备份、恢复互斥（同一把锁）。
func (s *BackupService) VerifyDatabaseBackup(ctx context.Context, name string) (BackupVerification, error) {
	if database.MaintenanceActive() {
		return BackupVerification{}, ErrBackupDuringMaintenance
	}
	if filepath.Base(name) != name || !isBackupFileName(name) {
		return BackupVerification{}, errors.New("无效的备份文件名")
	}
	registry := s.taskRegistry()
	registry.Begin(BackgroundTaskBackup)
	defer registry.End(BackgroundTaskBackup)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running.Store(true)
	defer s.running.Store(false)

	settings, err := (&SettingsService{}).GetSettings()
	if err != nil {
		return BackupVerification{}, fmt.Errorf("读取备份设置失败: %w", err)
	}
	directory := s.resolveDirectory(settings.BackupDirectory)
	info, err := os.Lstat(filepath.Join(directory, name))
	if err != nil || !info.Mode().IsRegular() {
		return BackupVerification{}, errors.New("备份文件不存在或无法读取")
	}
	result := s.verifyBackup(ctx, directory, name)
	result.VerifiedAt = s.now()
	if err := s.recordVerification(directory, result); err != nil {
		log.Printf("记录备份校验结果失败 err=%v", err)
	}
	return result, nil
}

// verifyBackup 按有无清单与后端分三档（4.2）：full（SQLite）、archive（Postgres：哈希 + 归档目录可读）、
// legacy（没有清单：只检查快照能否打开）。
func (s *BackupService) verifyBackup(ctx context.Context, directory, name string) BackupVerification {
	result := BackupVerification{Name: name, Problems: []string{}}
	problem := func(text string) { result.Problems = append(result.Problems, text) }
	snapshotPath := filepath.Join(directory, name)
	sqliteSnapshot := strings.HasSuffix(name, sqliteBackupFileSuffix)
	manifest, err := loadBackupManifest(directory, name)
	if errors.Is(err, errBackupManifestMissing) {
		result.Level = BackupVerifyLevelLegacy
		if err := s.openableSnapshot(ctx, snapshotPath, sqliteSnapshot); err != nil {
			problem(err.Error())
		}
		result.OK = len(result.Problems) == 0
		return result
	}
	result.Level = BackupVerifyLevelArchive
	if sqliteSnapshot {
		result.Level = BackupVerifyLevelFull
	}
	if err != nil {
		problem(err.Error())
		return result
	}

	snapshotHash, snapshotErr := hashFile(snapshotPath)
	filesHash, entryErr, filesErr := readBackupFilesPack(filepath.Join(directory, manifest.Files.Name), manifest.Files, nil)
	result.HashOK = snapshotErr == nil && filesErr == nil &&
		snapshotHash == manifest.Snapshot.SHA256 && filesHash == manifest.Files.SHA256
	if !result.HashOK {
		problem("快照或文件包的哈希与清单不一致")
	}
	result.FilesOK = filesErr == nil && entryErr == nil
	if filesErr != nil {
		problem(filesErr.Error())
	} else if entryErr != nil {
		problem(entryErr.Error())
	}

	if unknown, err := backupSchemaUnknown(manifest); err != nil {
		problem("读取当前表结构失败")
	} else if len(unknown) > 0 {
		problem("来自更新版本的应用（有当前版本不认识的表或列），当前版本不能恢复")
	} else {
		result.SchemaCompatible = true
	}

	if !sqliteSnapshot {
		if err := s.openableSnapshot(ctx, snapshotPath, false); err != nil {
			problem(err.Error())
		}
		result.OK = result.HashOK && result.FilesOK && result.SchemaCompatible && len(result.Problems) == 0
		return result
	}
	facts, err := inspectSQLiteSnapshot(ctx, snapshotPath)
	switch {
	case err != nil:
		result.QuickCheck = "无法打开"
		problem("快照无法作为 SQLite 库打开")
	case facts.QuickCheck != "ok":
		result.QuickCheck = facts.QuickCheck
		problem("快照未通过完整性检查")
	default:
		result.QuickCheck = "ok"
		result.RowsOK = sameBackupRowCounts(facts.Rows, manifest.Rows)
		if !result.RowsOK {
			problem("各表行数与清单不一致")
		}
	}
	result.OK = result.HashOK && result.FilesOK && result.QuickCheck == "ok" && result.RowsOK && result.SchemaCompatible
	return result
}

func sameBackupRowCounts(actual, recorded map[string]int64) bool {
	if len(actual) != len(recorded) {
		return false
	}
	for table, count := range recorded {
		if value, ok := actual[table]; !ok || value != count {
			return false
		}
	}
	return true
}

// openableSnapshot 是 legacy 档与 Postgres 归档的「能否打开」：SQLite 只读打开且有本应用的表；
// Postgres 用 pg_restore --list 读归档目录（不连库，不做试恢复）。
func (s *BackupService) openableSnapshot(ctx context.Context, path string, sqliteSnapshot bool) error {
	if sqliteSnapshot {
		if err := verifySQLiteSnapshot(path); err != nil {
			return errors.New("快照无法作为本应用的 SQLite 库打开")
		}
		return nil
	}
	if _, err := s.runner.LookPath("pg_restore"); err != nil {
		return errors.New("未找到 pg_restore，无法读取 Postgres 归档")
	}
	// 只读目录不连库；连接配置缺失（例如当前是 SQLite）时不带环境变量照样能读。
	_, env, _ := postgresCommandEnvironment()
	if err := s.runner.Run(ctx, "pg_restore", []string{"--list", path}, env); err != nil {
		return errors.New("Postgres 归档目录无法读取")
	}
	return nil
}

// loadVerifications 读出各备份最近一次的校验结果；文件不存在或读不出时为空（只是没有记录）。
func (s *BackupService) loadVerifications() map[string]BackupVerification {
	results := map[string]BackupVerification{}
	data, err := os.ReadFile(filepath.Join(s.dataDir, backupVerificationsFile))
	if err != nil {
		return results
	}
	if err := json.Unmarshal(data, &results); err != nil {
		return map[string]BackupVerification{}
	}
	return results
}

// recordVerification 记下这次校验结果，顺带去掉已经不在备份目录里的那些备份的记录。原子替换写入。
func (s *BackupService) recordVerification(directory string, result BackupVerification) error {
	results := s.loadVerifications()
	results[result.Name] = result
	entries, err := s.scanBackupEntries(directory)
	if err != nil {
		return err
	}
	present := make(map[string]bool, len(entries))
	for _, entry := range entries {
		present[entry.name] = true
	}
	for name := range results {
		if !present[name] {
			delete(results, name)
		}
	}
	data, err := json.Marshal(results)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dataDir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.dataDir, ".backup-verifications-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, filepath.Join(s.dataDir, backupVerificationsFile))
}

// BackupHealthSummary 是数据体检「备份」项要的三件事（4.3）：最近一次成功备份、最近一次校验结果、
// 没有清单的旧格式备份数。
type BackupHealthSummary struct {
	LastSuccessAt    *time.Time
	LastVerification *BackupVerification
	Total            int
	LegacyCount      int
}

// DataHealthSummary 只读设置与备份目录，不算哈希。
func (s *BackupService) DataHealthSummary(ctx context.Context) (BackupHealthSummary, error) {
	settings, err := (&SettingsService{}).GetSettingsContext(ctx)
	if err != nil {
		return BackupHealthSummary{}, err
	}
	directory := s.resolveDirectory(settings.BackupDirectory)
	entries, err := s.scanBackupEntries(directory)
	if err != nil {
		return BackupHealthSummary{}, err
	}
	summary := BackupHealthSummary{LastSuccessAt: settings.BackupLastSuccessAt, Total: len(entries)}
	verifications := s.loadVerifications()
	for _, entry := range entries {
		if _, err := os.Lstat(filepath.Join(directory, backupManifestName(entry.name))); os.IsNotExist(err) {
			summary.LegacyCount++
		}
		if verification, ok := verifications[entry.name]; ok {
			if summary.LastVerification == nil || verification.VerifiedAt.After(summary.LastVerification.VerifiedAt) {
				summary.LastVerification = &verification
			}
		}
	}
	return summary, nil
}
