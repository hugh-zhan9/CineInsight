package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"video-master/database"
)

// BackupReasonFilesInvalid：清单说有文件包，但文件包缺失、大小或哈希对不上、条目读不出（恢复前检查，4.2）。
const BackupReasonFilesInvalid = "backup_files_invalid"

// errBackupFilesNotRestored 是文件包还原失败时交给界面的话（4.2）：数据库已经替换，不回滚；
// 改名留下的 .pre-restore-* 目录保留不删。不带底层错误原文与路径（G-3），原文只进日志。
var errBackupFilesNotRestored = errors.New("数据库已恢复，整理过的文件未能还原，可从恢复前的安全备份中取回")

var errBackupFilesInvalid = fmt.Errorf("%s: 这份备份的文件包缺失或已损坏，数据库未被修改；可先「校验」确认，或换一份备份", BackupReasonFilesInvalid)

const (
	backupFilesStagingPrefix = ".cineinsight-restore-"
	backupFilesAsideMarker   = ".pre-restore-"
)

// restorableManifest 读出待恢复备份的清单并做恢复前的全部检查（4.2），都在进入维护之前：
//   - 旧格式备份返回 nil（只恢复数据库、不动整理文件目录）；清单坏了拒绝；
//   - 清单 models 里有当前版本不认识的表或列 → backup_from_newer_version；
//   - 文件包缺失、大小或哈希与清单不符、条目读不出 → backup_files_invalid；
//   - 解包所需空间不够 → backup_insufficient_space。
//
// 任何一条不过都不碰数据库与文件，用户拿到的是一句说得清的话。
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
	unknown, err := backupSchemaUnknown(manifest)
	if err != nil {
		return nil, fmt.Errorf("读取当前表结构失败，数据库未被修改: %w", err)
	}
	if len(unknown) > 0 {
		if len(unknown) > 5 {
			unknown = append(unknown[:5], "…")
		}
		return nil, fmt.Errorf("%s: 这份备份来自更新版本的应用（当前版本不认识：%s），数据库未被修改",
			BackupReasonFromNewerVersion, strings.Join(unknown, "、"))
	}
	if err := s.checkFilesPackBeforeRestore(directory, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// checkFilesPackBeforeRestore 在动任何东西之前确认文件包可用：存在、大小与哈希与清单一致、每个条目都能读出且
// 都在清单里（与「校验」同一套读法），并且每个解包落点所在的卷放得下全部条目。
func (s *BackupService) checkFilesPackBeforeRestore(directory string, manifest *BackupManifest) error {
	packPath := filepath.Join(directory, manifest.Files.Name)
	info, err := os.Lstat(packPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != manifest.Files.Size {
		return errBackupFilesInvalid
	}
	packHash, entryErr, ioErr := readBackupFilesPack(packPath, manifest.Files, nil)
	if ioErr != nil || entryErr != nil || packHash != manifest.Files.SHA256 {
		return errBackupFilesInvalid
	}
	var need int64
	for _, entry := range manifest.Files.Entries {
		need += entry.Size
	}
	if need == 0 {
		return nil
	}
	diskFree := s.diskFree
	if diskFree == nil {
		diskFree = enhancementDiskFree
	}
	// 暂存目录建在各收录目录的同级（见 swapInBackupFiles）：media-details 可能是指向另一块盘的链接，
	// 两个落点都查；每处都按全部条目算，宁可多要一点。
	checked := map[string]bool{}
	for _, dir := range backupFilesDirectories {
		parent := s.existingAncestor(filepath.Dir(filepath.Join(s.dataDir, filepath.FromSlash(dir))))
		if checked[parent] {
			continue
		}
		checked[parent] = true
		free, err := diskFree(parent)
		if err != nil {
			return errors.New("无法检查数据目录所在磁盘的剩余空间，数据库未被修改")
		}
		if free < uint64(need) {
			return fmt.Errorf("%s: 数据目录所在磁盘剩余空间不足以还原整理过的文件（需要约 %s，可用 %s），数据库未被修改",
				BackupReasonInsufficientSpace, formatEnhancementBytes(uint64(need)), formatEnhancementBytes(free))
		}
	}
	return nil
}

// existingAncestor 返回 dir 自身或最近一个存在的上级目录（还没建出来的 media-details 落在数据目录上）。
func (s *BackupService) existingAncestor(dir string) string {
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Stat(current); err == nil || current == filepath.Dir(current) {
			return current
		}
	}
}

func (s *BackupService) renamePath(from, to string) error {
	if s.rename != nil {
		return s.rename(from, to)
	}
	return os.Rename(from, to)
}

// restoreBackupFiles 在维护围栏内还原文件包（4.2）。manifest 为 nil（旧格式备份）时不动这些目录。
// 失败时原文只进日志，交给调用方的是不带路径的固定文案。
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

// swapInBackupFiles：每个收录目录在它的同级建暂存目录（同一父目录即同一个卷，改名不会跨盘失败），
// 文件包先完整解进这些暂存目录并逐条校验；然后逐个目录「原目录改名为 <目录>.pre-restore-<时间>，
// 暂存目录改名就位」；就位失败时把刚改走的原目录改回来，不让正在用的目录消失。全部就位后删掉改名留下的旧目录
// （安全备份里已经有它们）。任何一步失败都停下：已经换好的目录不回滚，它们的 .pre-restore-* 保留。
func (s *BackupService) swapInBackupFiles(directory string, manifest *BackupManifest) error {
	s.RecoverInterruptedFilesRestore()
	stamp := s.now().Format("20060102-150405.000000000")
	staged := make(map[string]string, len(backupFilesDirectories))
	for _, dir := range backupFilesDirectories {
		target := filepath.Join(s.dataDir, filepath.FromSlash(dir))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		staging := filepath.Join(filepath.Dir(target), backupFilesStagingPrefix+filepath.Base(target)+"-"+stamp)
		if err := os.MkdirAll(staging, 0755); err != nil {
			return err
		}
		defer os.RemoveAll(staging)
		staged[dir] = staging
	}
	extractTo := func(name string) string {
		dir := backupPackDirectoryOf(name)
		return filepath.Join(staged[dir], filepath.FromSlash(strings.TrimPrefix(name, dir+"/")))
	}
	packHash, entryErr, ioErr := readBackupFilesPack(filepath.Join(directory, manifest.Files.Name), manifest.Files, extractTo)
	switch {
	case ioErr != nil:
		return ioErr
	case entryErr != nil:
		return entryErr
	case packHash != manifest.Files.SHA256:
		return errors.New("文件包的哈希与清单不一致")
	}
	asides := []string{}
	for _, dir := range backupFilesDirectories {
		target := filepath.Join(s.dataDir, filepath.FromSlash(dir))
		aside := ""
		if _, err := os.Lstat(target); err == nil {
			aside = target + backupFilesAsideMarker + stamp
			if err := s.renamePath(target, aside); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := s.renamePath(staged[dir], target); err != nil {
			if aside != "" {
				if backErr := s.renamePath(aside, target); backErr != nil {
					log.Printf("文件包就位失败后改回原目录也失败 dir=%s err=%v", dir, backErr)
				}
			}
			return err
		}
		if aside != "" {
			asides = append(asides, aside)
		}
	}
	for _, aside := range asides {
		if err := os.RemoveAll(aside); err != nil {
			log.Printf("删除恢复前的旧目录失败 err=%v", err)
		}
	}
	return nil
}

// backupPackDirectoryOf 返回包内路径所属的收录目录（validBackupPackPath 已保证有一个）。
func backupPackDirectoryOf(name string) string {
	for _, dir := range backupFilesDirectories {
		if strings.HasPrefix(name, dir+"/") {
			return dir
		}
	}
	return path.Dir(name)
}

// RecoverInterruptedFilesRestore 收拾上一次文件还原中途崩溃留下的现场：某个收录目录不见了、而同级有
// <目录>.pre-restore-* 时，把最新的一个改回原名并记日志；顺带删掉崩溃留下的暂存目录。启动时与每次
// 还原文件包之前调用。目录都在时什么也不做（成功还原后留下的 .pre-restore-* 由还原自己删掉）。
func (s *BackupService) RecoverInterruptedFilesRestore() {
	for _, dir := range backupFilesDirectories {
		target := filepath.Join(s.dataDir, filepath.FromSlash(dir))
		parent, base := filepath.Dir(target), filepath.Base(target)
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		asides := []string{}
		for _, entry := range entries {
			switch {
			case strings.HasPrefix(entry.Name(), backupFilesStagingPrefix+base+"-") && entry.IsDir():
				_ = os.RemoveAll(filepath.Join(parent, entry.Name()))
			case strings.HasPrefix(entry.Name(), base+backupFilesAsideMarker) && entry.IsDir():
				asides = append(asides, entry.Name())
			}
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) || len(asides) == 0 {
			continue
		}
		sort.Strings(asides) // 时间戳定长，按名字排序即按时间排序
		newest := filepath.Join(parent, asides[len(asides)-1])
		if err := s.renamePath(newest, target); err != nil {
			log.Printf("恢复中断后的目录改回失败 dir=%s err=%v", dir, err)
			continue
		}
		log.Printf("上次还原整理文件中途中断：已把 %s 改回恢复前的内容（%s）", dir, asides[len(asides)-1])
	}
}
