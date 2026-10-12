package services

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// 文件包还原的进行中标记（4.2，审阅 R3 / I-2）：在建暂存目录、解包之前就写下（state=staging），开始改名前改成
// state=swapping；全部就位（或失败后已改回）才删除。记下每个收录目录的真实位置（已解析符号链接的上级 + 名字）、
// 暂存目录名与改名后的旧目录名。应用中途崩溃时标记留着，下次启动按它收拾：
//   - staging：只删记下的暂存目录，原目录没动过，保持原样；
//   - swapping：缺失的收录目录由记下的旧目录改回原名。
// 只动标记里记的、前缀对得上的名字，不按时间猜，也不碰别的东西。

const (
	backupFilesRestoreMarker = ".cineinsight-restore-in-progress.json"
	filesRestoreStaging      = "staging"
	filesRestoreSwapping     = "swapping"
)

// BackupReasonRestoreRecoveryPending：上次还原整理文件的现场没能收拾完（标记还在），这次恢复不开始。
const BackupReasonRestoreRecoveryPending = "backup_restore_recovery_pending"

var errFilesRestoreRecoveryPending = fmt.Errorf("%s: 上次还原整理过的文件没有收拾完（有目录没能改回原名），为免覆盖现场，这次恢复没有开始，数据库未被修改；请重启应用后再试，或检查数据目录的权限",
	BackupReasonRestoreRecoveryPending)

type filesRestoreMarker struct {
	State   string               `json:"state"`
	Stamp   string               `json:"stamp"`
	Targets []filesRestoreTarget `json:"targets"`
}

// filesRestoreTarget：Dir 是收录目录（相对数据目录）；Parent + Base 是它的真实位置；Staging 与 Aside 是与真实目录
// 同级的暂存目录名与旧目录名（只是名字）。原目录不存在时 Aside 为空。
type filesRestoreTarget struct {
	Dir     string `json:"dir"`
	Parent  string `json:"parent"`
	Base    string `json:"base"`
	Aside   string `json:"aside"`
	Staging string `json:"staging"`
}

// valid 是防篡改的名字检查（审阅 M1）：收录目录要认识，上级是规整的绝对路径，名字是不含路径的单段，
// 暂存目录名以 .cineinsight-restore-<名字>- 开头，旧目录名以 <名字>.pre-restore- 开头。对不上的条目一律不动。
func (t filesRestoreTarget) valid() bool {
	plain := func(name string) bool {
		return name != "" && filepath.Base(name) == name && name != "." && name != ".."
	}
	return validBackupDirectory(t.Dir) && filepath.IsAbs(t.Parent) && filepath.Clean(t.Parent) == t.Parent && plain(t.Base) &&
		plain(t.Staging) && strings.HasPrefix(t.Staging, backupFilesStagingPrefix+t.Base+"-") &&
		(t.Aside == "" || (plain(t.Aside) && strings.HasPrefix(t.Aside, t.Base+backupFilesAsideMarker)))
}

func validBackupDirectory(dir string) bool {
	for _, known := range backupFilesDirectories {
		if dir == known {
			return true
		}
	}
	return false
}

func (s *BackupService) filesRestoreMarkerPath() string {
	return filepath.Join(s.dataDir, backupFilesRestoreMarker)
}

func (s *BackupService) filesRestoreMarkerExists() bool {
	_, err := os.Lstat(s.filesRestoreMarkerPath())
	return err == nil
}

// writeFilesRestoreMarker 原子写入标记（临时文件 → fsync → 改名）。
func (s *BackupService) writeFilesRestoreMarker(marker filesRestoreMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.dataDir, ".cineinsight-restore-marker-*.tmp")
	if err != nil {
		return pathlessError(err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return pathlessError(err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return pathlessError(err)
	}
	if err := temp.Close(); err != nil {
		return pathlessError(err)
	}
	return pathlessFileError(os.Rename(tempPath, s.filesRestoreMarkerPath()))
}

func (s *BackupService) removeFilesRestoreMarker() {
	if err := os.Remove(s.filesRestoreMarkerPath()); err != nil && !os.IsNotExist(err) {
		log.Printf("删除整理文件还原标记失败 err=%v", err)
	}
}

// ensureNoPendingFilesRestore 在恢复开始前（进入维护之前）先收拾上次的现场；收拾不完（标记还在）就拒绝这次恢复，
// 不覆盖标记（审阅 M2）。
func (s *BackupService) ensureNoPendingFilesRestore() error {
	s.RecoverInterruptedFilesRestore()
	if s.filesRestoreMarkerExists() {
		return errFilesRestoreRecoveryPending
	}
	return nil
}
