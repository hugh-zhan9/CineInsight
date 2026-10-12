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
	mergeTempLeft    bool // 合并中断、而真实目录现在够不着，没能清掉里面的临时文件
}

// recoverFromFilesRestoreMarker 按标记收拾现场，返回是否全部处理完；并给出一次说明（桌面通知 + 备份状态）。
func (s *BackupService) recoverFromFilesRestoreMarker(marker filesRestoreMarker) bool {
	outcome := filesRecoveryOutcome{done: true}
	dataRoot, err := s.resolvedDataDir()
	if err != nil {
		outcome.done = false
	} else {
		targets := make([]filesRestoreTarget, 0, len(marker.Targets))
		for _, target := range marker.Targets {
			if !target.valid(dataRoot) {
				log.Printf("整理文件还原标记里有对不上的条目，未动 dir=%q", target.Dir)
				continue
			}
			// 先推断再动手（审阅第五轮 E2a）：还在 pending 的换入目录，暂存目录已经不在、原目录与旧目录都在，说明两次改名
			// 都做完了（暂存目录被改名就位），只是没来得及记状态——按已换好处理。
			if marker.State == filesRestoreSwapping && target.Mode != filesRestoreModeMerge && target.State == filesTargetPending &&
				target.Aside != "" && filesSwapAppearsDone(target) {
				target.State = filesTargetSwapped
			}
			targets = append(targets, target)
		}
		// 整个还原是否走完（换入的都换好、合并的都合并完，审阅第五轮 M3）：只有走完了，换好的目录留下的旧目录才删。
		finished := true
		for _, target := range targets {
			switch {
			case target.Mode == filesRestoreModeMerge && target.State != filesTargetMerged,
				target.Mode != filesRestoreModeMerge && target.State != filesTargetSwapped && target.State != filesTargetAsideDeleted:
				finished = false
			}
		}
		for _, target := range targets {
			s.recoverFilesTarget(marker.State, target, finished, &outcome)
		}
	}
	s.setRestoreNotice(describeFilesRecovery(marker.State, outcome))
	return outcome.done
}

func filesSwapAppearsDone(target filesRestoreTarget) bool {
	_, stagingErr := os.Lstat(filepath.Join(target.Parent, target.Staging))
	_, originalErr := os.Lstat(filepath.Join(target.Parent, target.Base))
	_, asideErr := os.Lstat(filepath.Join(target.Parent, target.Aside))
	return os.IsNotExist(stagingErr) && originalErr == nil && asideErr == nil
}

// recoverFilesTarget 处理一个目录。暂存目录只在做完判断之后才删；判断不了的情形连暂存目录一起留着。
func (s *BackupService) recoverFilesTarget(markerState string, target filesRestoreTarget, finished bool, outcome *filesRecoveryOutcome) {
	staging := filepath.Join(target.Parent, target.Staging)
	if markerState == filesRestoreStaging {
		_ = os.RemoveAll(staging) // 还没开始改名或合并：原目录没动过
		return
	}
	if target.Mode == filesRestoreModeMerge {
		_ = os.RemoveAll(staging) // 合并用的暂存目录在数据目录里
		if target.State != filesTargetMerged {
			outcome.mergeInterrupted = true
			if !s.removeMergeTempFiles(target.Dir) {
				outcome.mergeTempLeft = true // 真实目录够不着（盘没挂）：临时文件前缀固定、无害，不挡住标记删除（M4）
			}
		}
		return
	}
	// 没有要撤回的（原来就没有目录、旧目录已删）先处理，不看上级目录在不在，免得无谓地挡住（E2a）。
	if target.Aside == "" || target.State == filesTargetAsideDeleted {
		_ = os.RemoveAll(staging)
		return
	}
	aside := filepath.Join(target.Parent, target.Aside)
	if target.State == filesTargetSwapped {
		_ = os.RemoveAll(staging)
		if !finished {
			outcome.keptAsides++
			return
		}
		if err := os.RemoveAll(aside); err != nil {
			outcome.keptAsides++
		}
		return
	}
	if _, err := os.Lstat(target.Parent); err != nil {
		outcome.done = false // 上级目录不在或读不到：现场对不上，留着标记
		return
	}
	original := filepath.Join(target.Parent, target.Base)
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
		_ = os.RemoveAll(staging)
		outcome.renamed++
		log.Printf("上次还原整理文件中途中断：已把 %s 改回恢复前的内容", target.Dir)
	case originalErr == nil && asideErr == nil:
		outcome.done = false // 暂存目录还在、原目录与旧目录又都在：分不清，留着让人看
		outcome.keptAsides++
	case os.IsNotExist(originalErr) && os.IsNotExist(asideErr):
		outcome.done = false
	default:
		_ = os.RemoveAll(staging) // 原目录在、旧目录不在：第一次改名还没发生，原目录没动过
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
			parts = append(parts, "按合并还原的目录可能只写进了一部分备份里的文件")
		}
		if outcome.mergeTempLeft {
			parts = append(parts, "其中有目录现在够不着（磁盘没挂），里面可能留有名字以 .cineinsight-restore-tmp- 开头的临时文件，可以直接删掉")
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

// sweepOrphanStaging 清掉没有标记引用的暂存目录（此时已没有标记），只在数据目录里找：整目录换入用的（与收录目录
// 真实位置、链接所在位置同级、名字以 .cineinsight-restore-<名字>- 开头），以及合并还原用的（以 .cineinsight-restore-merge- 开头）。
func (s *BackupService) sweepOrphanStaging() {
	dataRoot, err := s.resolvedDataDir()
	if err != nil {
		return
	}
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
		// 只要真实位置，不用 placeCuratedRoot：它会为判定换入/合并把整棵目录走一遍，启动时没必要。
		if state, resolved := curatedRootState(s.dataDir, dir); state == curatedRootPresent {
			locations[filepath.Dir(resolved)] = filepath.Base(resolved)
		}
		for parent, base := range locations {
			if !insideDirectory(dataRoot, parent) {
				continue // 只在数据目录里清扫，链接指向的地方一概不碰（审阅第五轮 M5）
			}
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
