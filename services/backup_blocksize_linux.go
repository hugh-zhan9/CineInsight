//go:build linux

package services

import "syscall"

// backupBlockSize 返回 path 所在文件系统的分配块大小：优先 f_frsize（基本块），没有时用 f_bsize；取不到时为 0。
func backupBlockSize(path string) int64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0
	}
	if stat.Frsize > 0 {
		return int64(stat.Frsize)
	}
	return int64(stat.Bsize)
}
