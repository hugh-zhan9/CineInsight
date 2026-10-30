//go:build darwin || linux

package services

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// BTFileCredentialLock is shared by all managers/processes for one Keychain scope.
// Lock files are never removed: deleting the inode would allow concurrent owners.
type BTFileCredentialLock struct{ Path string }

func (l BTFileCredentialLock) TryLock() (func(), error) {
	if l.Path == "" {
		return nil, ErrBTCredentials
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0700); err != nil {
		return nil, ErrBTCredentials
	}
	fd, err := unix.Open(l.Path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, ErrBTCredentials
	}
	file := os.NewFile(uintptr(fd), l.Path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		file.Close()
		return nil, ErrBTCredentials
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return nil, ErrBTCredentialsBusy
		}
		return nil, ErrBTCredentials
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = file.Close() }, nil
}
