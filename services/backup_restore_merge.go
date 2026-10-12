package services

import (
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// 链接过去的收录目录按合并还原（4.2，集成方裁决 2026-10-12，见 backup_restore_placement.go）。
// 链接指向的目录里只会出现两样东西：备份里的文件（同目录临时文件写好后改名覆盖同名文件），以及崩溃时可能残留的、
// 名字以 .cineinsight-restore-tmp- 开头的临时文件。已有的其他文件、点文件、子目录里的符号链接一律不动。

const (
	backupFilesMergeTempPrefix    = ".cineinsight-restore-tmp-"
	backupFilesMergeStagingPrefix = ".cineinsight-restore-merge-"
)

func (p filesPlacement) mergeStagingName(stamp string) string {
	return backupFilesMergeStagingPrefix + p.Base + "-" + stamp
}

// probeLinkedTarget 在恢复开始前确认链接指向的目录写得进去：在它（还不存在时在最近一个存在的上级）里建一个
// 固定前缀的临时文件再删掉。写不进去就按「目标不可用」拒绝，什么都还没动。
func (s *BackupService) probeLinkedTarget(dir string) bool {
	location := s.existingAncestor(filepath.Join(s.dataDir, filepath.FromSlash(dir)))
	probe, err := os.CreateTemp(location, backupFilesMergeTempPrefix+"probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	return os.Remove(name) == nil
}

// mergeLinkedRoot 把一个收录目录解好的暂存文件逐个写进链接指向的真实目录，返回是否全部写成。
// 写不了的文件（中间目录是符号链接或不是目录、目标是链接或目录、读写出错）跳过并记为不完整，不影响其他文件。
func (s *BackupService) mergeLinkedRoot(dir, staged string) bool {
	link := filepath.Join(s.dataDir, filepath.FromSlash(dir))
	if err := os.MkdirAll(link, 0755); err != nil {
		log.Printf("合并还原：建目录失败 dir=%s err=%v", dir, pathlessError(err))
		return false
	}
	root, err := filepath.EvalSymlinks(link)
	if err != nil {
		return false
	}
	complete := true
	walkErr := filepath.WalkDir(staged, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(staged, path)
		if err != nil {
			return err
		}
		if !mergeOneFile(root, rel, path) {
			complete = false
		}
		return nil
	})
	if walkErr != nil {
		log.Printf("合并还原：读暂存目录失败 dir=%s err=%v", dir, pathlessError(walkErr))
		return false
	}
	return complete
}

// mergeOneFile 把 source 写到 root/rel：缺的中间目录逐级建出来，遇到符号链接或非目录就不往里写；目标已存在且不是
// 普通文件时也不碰。写法是同目录临时文件 → 落盘 → 改名覆盖。
func mergeOneFile(root, rel, source string) bool {
	parts := strings.Split(rel, string(filepath.Separator))
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		switch {
		case os.IsNotExist(err):
			if err := os.Mkdir(current, 0755); err != nil {
				return false
			}
		case err != nil, info.Mode()&os.ModeSymlink != 0, !info.IsDir():
			return false
		}
	}
	dest := filepath.Join(root, rel)
	if info, err := os.Lstat(dest); err == nil && !info.Mode().IsRegular() {
		return false
	} else if err != nil && !os.IsNotExist(err) {
		return false
	}
	return copyFileOver(source, dest) == nil
}

func copyFileOver(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(dest), backupFilesMergeTempPrefix+"*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	done := false
	defer func() {
		if !done {
			_ = temp.Close()
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := io.Copy(temp, in); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	done = true
	if err := os.Rename(tempPath, dest); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

// removeMergeTempFiles 清掉崩溃时残留在链接指向目录里的临时文件（只认固定前缀的普通文件）。
func (s *BackupService) removeMergeTempFiles(dir string) {
	root, err := filepath.EvalSymlinks(filepath.Join(s.dataDir, filepath.FromSlash(dir)))
	if err != nil {
		return
	}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), backupFilesMergeTempPrefix) {
			_ = os.Remove(path)
		}
		return nil
	})
}
