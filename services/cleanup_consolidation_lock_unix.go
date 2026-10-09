//go:build darwin || linux

package services

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func acquireConsolidationLock(dataDir, previewID string) (func(), error) {
	path := consolidationLockPath(dataDir, previewID)
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("集中整理锁文件无效: %s", path)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrConsolidationBusy
		}
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = file.Close() }, nil
}
