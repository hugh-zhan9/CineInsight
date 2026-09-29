//go:build unix

package services

import (
	"os"
	"syscall"
)

// fileLinkCount 返回文件的硬链接数（Stat_t.Nlink，darwin 与 linux 等 unix 平台都有，修复 I m-f）。清理旧版 trash/
// 残留名字之前用它确认原路径与残留是同一个普通文件的两个名字（hardLinkedRegularNames，m1）；读不到时返回 false，
// 调用方不删。
func fileLinkCount(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Nlink), true
}
