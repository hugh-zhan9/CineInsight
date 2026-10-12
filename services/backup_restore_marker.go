package services

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// 文件包还原的进行中标记（4.2，审阅 R3）：开始改名之前写下每个收录目录的旧目录名与暂存目录名，全部就位（或失败后已
// 改回）才删除。应用中途崩溃时标记留着，下次启动按它把缺失的收录目录改回原名——只动标记里记的名字，不按时间猜。

const backupFilesRestoreMarker = ".cineinsight-restore-in-progress.json"

type filesRestoreMarker struct {
	Stamp   string               `json:"stamp"`
	Targets []filesRestoreTarget `json:"targets"`
}

// filesRestoreTarget：Dir 是收录目录（相对数据目录）；Aside 与 Staging 是与它同级的旧目录名与暂存目录名
// （只是名字，不含路径）。原目录不存在时 Aside 为空。
type filesRestoreTarget struct {
	Dir     string `json:"dir"`
	Aside   string `json:"aside"`
	Staging string `json:"staging"`
}

func (s *BackupService) filesRestoreMarkerPath() string {
	return filepath.Join(s.dataDir, backupFilesRestoreMarker)
}

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

// RecoverInterruptedFilesRestore 收拾上一次文件还原中途崩溃留下的现场（启动时与每次还原文件包之前调用）。
// 只在进行中标记存在时动手：标记里的收录目录不见了、而记下的旧目录还在，就把旧目录改回原名；记下的暂存目录删掉。
// 全部处理完才删标记（改回失败时留着，下次再试）。改回过目录时给一次桌面通知，设置页的备份状态也显示说明。
func (s *BackupService) RecoverInterruptedFilesRestore() {
	data, err := os.ReadFile(s.filesRestoreMarkerPath())
	if os.IsNotExist(err) {
		return
	}
	var marker filesRestoreMarker
	if err != nil || json.Unmarshal(data, &marker) != nil {
		log.Printf("整理文件还原标记读不出，未做任何改动 err=%v", err)
		return
	}
	recovered, failed := 0, false
	for _, entry := range marker.Targets {
		if !validBackupDirectory(entry.Dir) || !validSiblingName(entry.Aside) || !validSiblingName(entry.Staging) {
			log.Printf("整理文件还原标记里有不认识的条目，跳过 dir=%q", entry.Dir)
			continue
		}
		target := filepath.Join(s.dataDir, filepath.FromSlash(entry.Dir))
		parent := filepath.Dir(target)
		if entry.Staging != "" {
			_ = os.RemoveAll(filepath.Join(parent, entry.Staging))
		}
		if entry.Aside == "" {
			continue
		}
		aside := filepath.Join(parent, entry.Aside)
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			continue
		}
		if _, err := os.Lstat(aside); err != nil {
			continue
		}
		if err := s.renamePath(aside, target); err != nil {
			log.Printf("恢复中断后的目录改回失败 dir=%s err=%v", entry.Dir, err)
			failed = true
			continue
		}
		recovered++
		log.Printf("上次还原整理文件中途中断：已把 %s 改回恢复前的内容", entry.Dir)
	}
	if !failed {
		s.removeFilesRestoreMarker()
	}
	if recovered > 0 {
		notice := fmt.Sprintf("上次从备份还原整理过的文件时中途中断，已把 %d 个目录改回恢复前的内容；数据库可能已是备份里的状态，必要时请重新恢复", recovered)
		s.registryMu.Lock()
		s.notice = notice
		s.registryMu.Unlock()
		notifyDesktop(s.desktopNotifier(), "整理过的文件已复原", notice)
	}
}

// validBackupDirectory / validSiblingName 防止被改过的标记把改名指到收录目录之外。
func validBackupDirectory(dir string) bool {
	for _, known := range backupFilesDirectories {
		if dir == known {
			return true
		}
	}
	return false
}

func validSiblingName(name string) bool {
	return name == "" || (filepath.Base(name) == name && name != "." && name != "..")
}
