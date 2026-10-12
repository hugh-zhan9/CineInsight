package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// 文件包还原的进行中标记（4.2，审阅 R3 / I-2 / 第四轮）：在建暂存目录、解包之前就写下（state=staging），开始改名或
// 合并前改成 state=swapping；每个目录做完都更新它自己的状态（swap：pending → swapped → aside_deleted；
// merge：pending → merged），全部完成（或失败后已改回）才删除。标记只记数据目录里的位置：整目录换入的目录记真实上级
// （必须在数据目录下）、名字、暂存目录名与旧目录名；合并还原的目录只记它在数据目录里的暂存目录。应用中途崩溃时标记
// 留着，下次启动按它收拾（backup_restore_recover.go）。只动标记里记的、前缀对得上、位置在数据目录里的名字。

const (
	backupFilesRestoreMarker = ".cineinsight-restore-in-progress.json"
	filesRestoreStaging      = "staging"
	filesRestoreSwapping     = "swapping"

	filesRestoreModeSwap  = "swap"
	filesRestoreModeMerge = "merge"

	filesTargetPending      = "pending"
	filesTargetSwapped      = "swapped"
	filesTargetAsideDeleted = "aside_deleted"
	filesTargetMerged       = "merged"
)

// BackupReasonRestoreRecoveryPending：上次还原整理文件的现场没能收拾完（标记还在），这次恢复不开始。
const BackupReasonRestoreRecoveryPending = "backup_restore_recovery_pending"

var errFilesRestoreRecoveryPending = fmt.Errorf("%s: 上次还原整理过的文件没有收拾完（有目录没能改回原名或现场对不上），为免覆盖现场，这次恢复没有开始，数据库未被修改；"+
	"可在设置页「数据库备份」里看留下了哪些副本，确认后点「放弃收拾，允许恢复」（不会动任何文件夹）再恢复",
	BackupReasonRestoreRecoveryPending)

type filesRestoreMarker struct {
	State   string               `json:"state"`
	Stamp   string               `json:"stamp"`
	Targets []filesRestoreTarget `json:"targets"`
}

// filesRestoreTarget：Dir 是收录目录（相对数据目录）；Mode 是 swap 或 merge；Parent + Base 是 swap 目录的真实位置
// （在数据目录下），merge 目录的 Parent 是数据目录本身；Staging 与 Aside 是与之同级的暂存目录名与旧目录名（只是名字）。
// 原目录不存在、或按合并还原时 Aside 为空。State 是这个目录做到哪一步。
type filesRestoreTarget struct {
	Dir     string `json:"dir"`
	Mode    string `json:"mode"`
	Parent  string `json:"parent"`
	Base    string `json:"base"`
	Aside   string `json:"aside"`
	Staging string `json:"staging"`
	State   string `json:"state"`
}

// valid 是防篡改的检查（审阅 M1 / 第四轮）：收录目录要认识；上级是规整的绝对路径、且在数据目录里（合并还原的就是
// 数据目录本身）；名字是不含路径的单段；暂存目录名、旧目录名的前缀都对得上。对不上的条目一律不动。
func (t filesRestoreTarget) valid(dataRoot string) bool {
	plain := func(name string) bool {
		return name != "" && filepath.Base(name) == name && name != "." && name != ".."
	}
	// 「在数据目录里」同时排除了相对路径（数据目录本身是解析过的绝对路径）。
	if !validBackupDirectory(t.Dir) || filepath.Clean(t.Parent) != t.Parent ||
		!insideDirectory(dataRoot, t.Parent) || !plain(t.Base) || !plain(t.Staging) {
		return false
	}
	if t.Mode == filesRestoreModeMerge {
		return t.Parent == dataRoot && t.Base == filepath.Base(filepath.FromSlash(t.Dir)) && t.Aside == "" &&
			strings.HasPrefix(t.Staging, backupFilesMergeStagingPrefix+t.Base+"-")
	}
	// 没有 mode 的是更早的开发版写的标记，当作整目录换入。
	return (t.Mode == filesRestoreModeSwap || t.Mode == "") && strings.HasPrefix(t.Staging, backupFilesStagingPrefix+t.Base+"-") &&
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
	s.recoverInterruptedFilesRestoreLocked() // 调用方（恢复）已持有 s.mu
	if s.filesRestoreMarkerExists() {
		return errFilesRestoreRecoveryPending
	}
	return nil
}

// curatedFolderLabels 是给用户看的收录目录叫法（不含绝对路径）。
var curatedFolderLabels = map[string]string{
	"media-details/people":      "人物头像",
	"media-details/collections": "作品集封面",
	"media-details/videos":      "海报副本",
	"media-details/watchlist":   "想看封面",
	"wallpapers":                "壁纸",
	"subtitle-backups":          "字幕备份",
}

// pendingFilesRestoreLeftovers 报告是否还留着进行中标记，以及哪些收录目录旁边还有恢复前的副本或暂存文件
// （按「叫法（相对数据目录的位置）」列出，不含绝对路径）。设置页的备份状态读它。
func (s *BackupService) pendingFilesRestoreLeftovers() (bool, []string) {
	data, err := os.ReadFile(s.filesRestoreMarkerPath())
	if os.IsNotExist(err) {
		return false, nil
	}
	leftovers := []string{}
	var marker filesRestoreMarker
	if err != nil || json.Unmarshal(data, &marker) != nil {
		return true, leftovers
	}
	dataRoot, err := s.resolvedDataDir()
	if err != nil {
		return true, leftovers
	}
	for _, target := range marker.Targets {
		if !target.valid(dataRoot) {
			continue
		}
		left := false
		for _, name := range []string{target.Aside, target.Staging} {
			if name == "" {
				continue
			}
			if _, err := os.Lstat(filepath.Join(target.Parent, name)); err == nil {
				left = true
			}
		}
		if left {
			leftovers = append(leftovers, curatedFolderLabels[target.Dir]+"（"+target.Dir+"）")
		}
	}
	return true, leftovers
}

// DiscardInterruptedFilesRestore 放弃收拾上次没收拾完的整理文件还原（审阅第五轮 E2b）：只删进行中标记，不碰任何文件夹——
// 留下的 .pre-restore-* 副本与暂存目录原样留着，用户可以自己查看、处理。之后恢复不再被 backup_restore_recovery_pending 挡住。
func (s *BackupService) DiscardInterruptedFilesRestore() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.filesRestoreMarkerPath()); err != nil && !os.IsNotExist(err) {
		log.Printf("删除整理文件还原标记失败 err=%v", err)
		return errors.New("没能删掉进行中标记，请检查数据目录的权限")
	}
	s.registryMu.Lock()
	s.notice = ""
	s.registryMu.Unlock()
	log.Printf("用户放弃收拾上次中断的整理文件还原，已删掉进行中标记")
	return nil
}
