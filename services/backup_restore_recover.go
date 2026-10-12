package services

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// RecoverInterruptedFilesRestore 收拾上一次文件还原中途崩溃留下的现场（启动时调用；恢复开始前由
// ensureNoPendingFilesRestore 在同一把锁里调用）。拿备份服务的锁，不与正在进行的恢复抢现场（审阅第四轮 D3）。
func (s *BackupService) RecoverInterruptedFilesRestore() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoverInterruptedFilesRestoreLocked()
}

// recoverInterruptedFilesRestoreLocked：有标记时按标记处理，全部处理完才删标记；有任何一条处理不了（上级目录不在、
// 读不到、原目录与旧目录同时都在、两个都不在……）就留着标记、留着现场并说明，新的恢复会被拒绝
// （backup_restore_recovery_pending，审阅第四轮 D2）。没有（或已处理完）标记时，清掉散落的暂存目录。
func (s *BackupService) recoverInterruptedFilesRestoreLocked() {
	data, err := os.ReadFile(s.filesRestoreMarkerPath())
	switch {
	case os.IsNotExist(err):
	case err != nil:
		log.Printf("整理文件还原标记读不出，未做任何改动 err=%v", err)
		return
	default:
		var marker filesRestoreMarker
		if json.Unmarshal(data, &marker) != nil {
			log.Printf("整理文件还原标记已损坏，未做任何改动")
			s.setRestoreNotice("上次还原整理过的文件留下的进行中标记已损坏，没有自动收拾；新的恢复会被拒绝，请检查数据目录")
			return
		}
		if !s.recoverFromFilesRestoreMarker(marker) {
			return
		}
		s.removeFilesRestoreMarker()
	}
	s.sweepOrphanStaging()
}

type filesRecoveryOutcome struct {
	done             bool
	renamed          int
	keptAsides       int
	mergeInterrupted bool
}

// recoverFromFilesRestoreMarker 按标记收拾现场，返回是否全部处理完；并给出一次说明（桌面通知 + 备份状态）。
func (s *BackupService) recoverFromFilesRestoreMarker(marker filesRestoreMarker) bool {
	outcome := filesRecoveryOutcome{done: true}
	dataRoot, err := s.resolvedDataDir()
	if err != nil {
		outcome.done = false
	} else {
		swapFinished := true // 换入阶段是否已走完（所有整目录换入的目录都已换好）
		for _, target := range marker.Targets {
			if target.Mode == filesRestoreModeSwap && target.State != filesTargetSwapped && target.State != filesTargetAsideDeleted {
				swapFinished = false
			}
		}
		for _, target := range marker.Targets {
			if !target.valid(dataRoot) {
				log.Printf("整理文件还原标记里有对不上的条目，未动 dir=%q", target.Dir)
				continue
			}
			s.recoverFilesTarget(marker.State, target, swapFinished, &outcome)
		}
	}
	s.setRestoreNotice(describeFilesRecovery(marker.State, outcome))
	return outcome.done
}

func (s *BackupService) recoverFilesTarget(markerState string, target filesRestoreTarget, swapFinished bool, outcome *filesRecoveryOutcome) {
	_ = os.RemoveAll(filepath.Join(target.Parent, target.Staging))
	if markerState == filesRestoreStaging {
		return // 还没开始改名或合并：原目录没动过
	}
	if target.Mode == filesRestoreModeMerge {
		if target.State != filesTargetMerged {
			outcome.mergeInterrupted = true
			s.removeMergeTempFiles(target.Dir)
		}
		return
	}
	if _, err := os.Lstat(target.Parent); err != nil {
		outcome.done = false // 上级目录不在或读不到：现场对不上，留着标记
		return
	}
	original := filepath.Join(target.Parent, target.Base)
	aside := filepath.Join(target.Parent, target.Aside)
	switch {
	case target.Aside == "" || target.State == filesTargetAsideDeleted:
		return
	case target.State == filesTargetSwapped:
		// 只有整个换入阶段都走完了，换好的目录留下的旧目录才删；否则留着，并在说明里提到。
		if !swapFinished {
			outcome.keptAsides++
			return
		}
		if err := os.RemoveAll(aside); err != nil {
			outcome.keptAsides++
		}
		return
	}
	_, originalErr := os.Lstat(original)
	_, asideErr := os.Lstat(aside)
	switch {
	case (originalErr != nil && !os.IsNotExist(originalErr)) || (asideErr != nil && !os.IsNotExist(asideErr)):
		outcome.done = false
	case os.IsNotExist(originalErr) && asideErr == nil:
		if err := s.renamePath(aside, original); err != nil {
			log.Printf("恢复中断后的目录改回失败 dir=%s err=%v", target.Dir, err)
			outcome.done = false
			return
		}
		outcome.renamed++
		log.Printf("上次还原整理文件中途中断：已把 %s 改回恢复前的内容", target.Dir)
	case originalErr == nil && asideErr == nil:
		outcome.done = false // 原目录与旧目录都在：分不清哪份是恢复前的，留着让人看
		outcome.keptAsides++
	case os.IsNotExist(originalErr) && os.IsNotExist(asideErr):
		outcome.done = false
	}
}

// describeFilesRecovery 是给用户的一次性说明（不含路径）。swapping 状态下即使没有目录要改回也说明（审阅第四轮 D4）。
func describeFilesRecovery(markerState string, outcome filesRecoveryOutcome) string {
	var parts []string
	if markerState == filesRestoreStaging {
		parts = append(parts, "上次恢复在还原整理过的文件时中断：数据库已恢复，整理过的文件保持原样（没有换成备份里的版本）")
	} else {
		parts = append(parts, "上次从备份还原整理过的文件时中途中断；数据库可能已是备份里的状态")
		if outcome.renamed > 0 {
			parts = append(parts, fmt.Sprintf("已把 %d 个目录改回恢复前的内容", outcome.renamed))
		}
		if outcome.keptAsides > 0 {
			parts = append(parts, fmt.Sprintf("%d 个目录的恢复前内容保留在同级、名字以 .pre-restore- 结尾的目录里", outcome.keptAsides))
		}
		if outcome.mergeInterrupted {
			parts = append(parts, "链接到别处的目录可能只写进了一部分备份里的文件")
		}
	}
	if !outcome.done {
		parts = append(parts, "还有目录没能自动收拾，新的恢复会被拒绝，请检查数据目录与外接磁盘")
	}
	return strings.Join(parts, "；")
}

func (s *BackupService) setRestoreNotice(notice string) {
	s.registryMu.Lock()
	s.notice = notice
	s.registryMu.Unlock()
	notifyDesktop(s.desktopNotifier(), "整理过的文件还原中断", notice)
}

// sweepOrphanStaging 清掉没有标记引用的暂存目录（此时已没有标记）：整目录换入用的（与收录目录真实位置、链接所在位置
// 同级、名字以 .cineinsight-restore-<名字>- 开头），以及合并还原用的（数据目录里、以 .cineinsight-restore-merge- 开头）。
func (s *BackupService) sweepOrphanStaging() {
	if entries, err := os.ReadDir(s.dataDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), backupFilesMergeStagingPrefix) {
				_ = os.RemoveAll(filepath.Join(s.dataDir, entry.Name()))
			}
		}
	}
	for _, dir := range backupFilesDirectories {
		locations := map[string]string{} // 已解析符号链接的上级 → 名字
		link := filepath.Join(s.dataDir, filepath.FromSlash(dir))
		if parent, err := filepath.EvalSymlinks(filepath.Dir(link)); err == nil {
			locations[parent] = filepath.Base(link)
		}
		if placement, state, err := s.placeCuratedRoot(dir, false); err == nil && state == curatedRootPresent {
			locations[placement.Parent] = placement.Base
		}
		for parent, base := range locations {
			entries, err := os.ReadDir(parent)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() && strings.HasPrefix(entry.Name(), backupFilesStagingPrefix+base+"-") {
					_ = os.RemoveAll(filepath.Join(parent, entry.Name()))
				}
			}
		}
	}
}
