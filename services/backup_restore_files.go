package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"video-master/database"
)

// errBackupFilesNotRestored 是文件包还原失败时交给界面的话（4.2）：数据库已经替换，不回滚；
// 改名留下的 .pre-restore-* 目录保留不删。不带底层错误原文与路径（G-3），原文只进日志。
var errBackupFilesNotRestored = errors.New("数据库已恢复，整理过的文件未能还原，可从恢复前的安全备份中取回")

// backupFilesStagingPattern 是文件包解到数据目录里的暂存目录名。
const backupFilesStagingPattern = ".cineinsight-restore-files-*"

// restorableManifest 读出待恢复备份的清单并做恢复前的结构检查（4.2）。旧格式备份返回 nil
// （只恢复数据库、不动整理文件目录）；清单坏了拒绝恢复；清单里有当前版本不认识的表或列时
// 以 backup_from_newer_version 拒绝。这些都发生在进入维护之前，数据库原样可用。
func (s *BackupService) restorableManifest(ctx context.Context, directory, name string, backend database.Backend) (*BackupManifest, error) {
	if filepath.Base(name) != name || !isBackupFileName(name) {
		return nil, nil // 文件名由 copyVerifiedBackup 统一拒绝
	}
	manifest, err := loadBackupManifest(directory, name)
	if errors.Is(err, errBackupManifestMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s，数据库未被修改", err.Error())
	}
	known, err := knownBackupSchema(ctx, database.DB)
	if err != nil {
		return nil, fmt.Errorf("读取当前表结构失败，数据库未被修改: %w", err)
	}
	if unknown := unknownBackupSchema(manifest.Schema, known, backend == database.BackendPostgres); len(unknown) > 0 {
		if len(unknown) > 5 {
			unknown = append(unknown[:5], "…")
		}
		return nil, fmt.Errorf("%s: 这份备份来自更新版本的应用（当前版本不认识：%s），数据库未被修改",
			BackupReasonFromNewerVersion, strings.Join(unknown, "、"))
	}
	return manifest, nil
}

// restoreBackupFiles 在维护围栏内还原文件包（4.2）：先完整解到数据目录里的暂存目录并逐条校验，
// 然后对每个收录目录「原目录改名为 <目录>.pre-restore-<时间>，新目录改名就位」，全部就位后
// 删掉改名留下的旧目录（安全备份里已经有它们）。manifest 为 nil（旧格式备份）时不动这些目录。
// 任何一步失败都停下：已改名的 .pre-restore-* 目录保留，暂存目录删除。
func (s *BackupService) restoreBackupFiles(directory string, manifest *BackupManifest) error {
	if manifest == nil {
		return nil
	}
	err := s.swapInBackupFiles(directory, manifest)
	if err != nil {
		log.Printf("还原备份文件包失败 backup=%s err=%v", manifest.Snapshot.Name, err)
	}
	return err
}

func (s *BackupService) swapInBackupFiles(directory string, manifest *BackupManifest) error {
	if err := os.MkdirAll(s.dataDir, 0700); err != nil {
		return err
	}
	// 上一次恢复中途崩溃留下的暂存目录先清掉（恢复持 s.mu 串行，此刻没有别的恢复在用它们）。
	if leftovers, err := filepath.Glob(filepath.Join(s.dataDir, backupFilesStagingPattern)); err == nil {
		for _, leftover := range leftovers {
			_ = os.RemoveAll(leftover)
		}
	}
	staging, err := os.MkdirTemp(s.dataDir, backupFilesStagingPattern)
	if err != nil {
		return fmt.Errorf("创建暂存目录失败: %w", err)
	}
	defer os.RemoveAll(staging)
	packHash, entryErr, ioErr := readBackupFilesPack(filepath.Join(directory, manifest.Files.Name), manifest.Files, staging)
	switch {
	case ioErr != nil:
		return ioErr
	case entryErr != nil:
		return entryErr
	case packHash != manifest.Files.SHA256:
		return errors.New("文件包的哈希与清单不一致")
	}
	stamp := s.now().Format("20060102-150405.000000000")
	asides := []string{}
	for _, dir := range backupFilesDirectories {
		target := filepath.Join(s.dataDir, filepath.FromSlash(dir))
		staged := filepath.Join(staging, filepath.FromSlash(dir))
		// 备份里这个目录是空的：还原成空目录。
		if err := os.MkdirAll(staged, 0755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if _, err := os.Lstat(target); err == nil {
			aside := target + ".pre-restore-" + stamp
			if err := os.Rename(target, aside); err != nil {
				return err
			}
			asides = append(asides, aside)
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(staged, target); err != nil {
			return err
		}
	}
	for _, aside := range asides {
		if err := os.RemoveAll(aside); err != nil {
			log.Printf("删除恢复前的旧目录失败 err=%v", err)
		}
	}
	return nil
}
