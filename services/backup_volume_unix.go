//go:build !windows

package services

import (
	"fmt"
	"os"
	"syscall"
)

// backupVolumeID 返回 path 所在卷的标识（跟随符号链接，取设备号）：同一个卷上的空间需求要合在一起算。
func backupVolumeID(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", pathlessError(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("无法识别磁盘")
	}
	return fmt.Sprintf("dev:%d", stat.Dev), nil
}
