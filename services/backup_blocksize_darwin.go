//go:build darwin

package services

import "syscall"

// backupBlockSize 返回 path 所在文件系统的分配块大小（Statfs 的 f_bsize，即基本块大小）；取不到时为 0。
func backupBlockSize(path string) int64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0
	}
	return int64(stat.Bsize)
}
