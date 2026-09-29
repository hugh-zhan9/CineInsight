//go:build darwin

package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// macOS can leave an empty mount-point directory behind after ejecting a disk.
// Existence alone is therefore not proof that a /Volumes disk is mounted.
func scanVolumeAvailable(path string) error {
	path = filepath.Clean(path)
	if !strings.HasPrefix(path, "/Volumes/") {
		return nil
	}
	volume := "/Volumes/" + strings.SplitN(strings.TrimPrefix(path, "/Volumes/"), "/", 2)[0]
	resolved, err := filepath.EvalSymlinks(volume)
	if err != nil {
		return err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(resolved, &stat); err != nil {
		return err
	}
	var mounted strings.Builder
	for _, ch := range stat.Mntonname {
		if ch == 0 {
			break
		}
		mounted.WriteByte(byte(ch))
	}
	if filepath.Clean(mounted.String()) != resolved {
		return fmt.Errorf("磁盘未挂载 %s: %w", volume, os.ErrNotExist)
	}
	return nil
}

// fileLinkCount 返回文件的硬链接数。清理旧版 trash/ 残留名字之前用它确认原路径与残留是同一个普通文件的
// 两个名字（hardLinkedRegularNames，m1）；读不到时返回 false，调用方不删。
func fileLinkCount(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Nlink), true
}
