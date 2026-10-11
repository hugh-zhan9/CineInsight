//go:build unix

package services

import (
	"os"
	"syscall"
)

// cleanupFileDevice 返回文件所在卷的设备号（stat 的 st_dev），用来按卷限制并发读取。
func cleanupFileDevice(info os.FileInfo) (uint64, bool) {
	if info == nil {
		return 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Dev), true
}
