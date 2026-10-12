package services

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// RecoverInterruptedFilesRestore 收拾上一次文件还原中途崩溃留下的现场（启动时与每次恢复开始前调用）。
// 有标记时按标记处理（见 backup_restore_marker.go），全部处理完才删标记；改回失败时标记留着、现场一概不动，下次再试，
// 期间新的恢复会被拒绝（backup_restore_recovery_pending）。没有（或已处理完）标记时，清掉散落的暂存目录。
// 处理过现场时给一次桌面通知，设置页的备份状态也显示说明。
func (s *BackupService) RecoverInterruptedFilesRestore() {
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
			return
		}
		if !s.recoverFromFilesRestoreMarker(marker) {
			return // 标记还在（有目录没能改回）：旧目录留着、散落的暂存目录也不清扫，等下次
		}
		s.removeFilesRestoreMarker()
	}
	s.sweepOrphanStaging()
}

// recoverFromFilesRestoreMarker 按标记收拾现场，返回是否全部处理完。
func (s *BackupService) recoverFromFilesRestoreMarker(marker filesRestoreMarker) bool {
	done, recovered := true, 0
	for _, target := range marker.Targets {
		if !target.valid() {
			log.Printf("整理文件还原标记里有对不上的条目，未动 dir=%q", target.Dir)
			continue
		}
		_ = os.RemoveAll(filepath.Join(target.Parent, target.Staging))
		if marker.State == filesRestoreStaging || target.Aside == "" {
			continue
		}
		original := filepath.Join(target.Parent, target.Base)
		aside := filepath.Join(target.Parent, target.Aside)
		if _, err := os.Lstat(original); !os.IsNotExist(err) {
			continue
		}
		if _, err := os.Lstat(aside); err != nil {
			continue
		}
		if err := s.renamePath(aside, original); err != nil {
			log.Printf("恢复中断后的目录改回失败 dir=%s err=%v", target.Dir, err)
			done = false
			continue
		}
		recovered++
		log.Printf("上次还原整理文件中途中断：已把 %s 改回恢复前的内容", target.Dir)
	}
	notice := ""
	switch {
	case marker.State == filesRestoreStaging:
		notice = "上次恢复在还原整理过的文件时中断：数据库已恢复，整理过的文件保持原样（没有换成备份里的版本）"
	case recovered > 0:
		notice = fmt.Sprintf("上次从备份还原整理过的文件时中途中断，已把 %d 个目录改回恢复前的内容；数据库可能已是备份里的状态，必要时请重新恢复", recovered)
	}
	if notice != "" {
		s.registryMu.Lock()
		s.notice = notice
		s.registryMu.Unlock()
		notifyDesktop(s.desktopNotifier(), "整理过的文件已复原", notice)
	}
	return done
}

// sweepOrphanStaging 清掉没有标记引用的暂存目录（此时已没有标记）：只认收录目录真实位置（与链接所在位置）同级、
// 名字以 .cineinsight-restore-<名字>- 开头的目录。
func (s *BackupService) sweepOrphanStaging() {
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
				path := filepath.Join(parent, entry.Name())
				if entry.IsDir() && strings.HasPrefix(entry.Name(), backupFilesStagingPrefix+base+"-") {
					_ = os.RemoveAll(path)
				}
			}
		}
	}
}
