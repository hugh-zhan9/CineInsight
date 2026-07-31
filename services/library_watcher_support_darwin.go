//go:build darwin

package services

import (
	"strings"
	"syscall"
)

func classifyLibraryWatchRoot(path string) (bool, string, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false, "", err
	}
	name := make([]byte, 0, len(stat.Fstypename))
	for _, value := range stat.Fstypename {
		if value == 0 {
			break
		}
		name = append(name, byte(value))
	}
	filesystem := string(name)
	switch strings.ToLower(filesystem) {
	case "nfs", "smbfs", "webdav", "osxfuse", "macfuse", "fusefs":
		return false, "此文件系统不支持可靠的实时监听", nil
	default:
		return true, "", nil
	}
}
