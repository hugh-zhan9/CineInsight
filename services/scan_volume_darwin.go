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
