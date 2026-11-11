//go:build darwin || linux

package services

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"syscall"
)

func replacementRenameUnsupported(err error) bool {
	return errors.Is(err, errReplacementNoReplace) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS)
}

func replacementNativeRename(a, b string) error { return publishConsolidationNoReplace(a, b) }

func replacementInvalidRenameArgument(err error) bool { return errors.Is(err, unix.EINVAL) }
func replacementSameVolume(source, parent os.FileInfo) bool {
	a, ok := source.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	b, ok := parent.Sys().(*syscall.Stat_t)
	return ok && a.Dev == b.Dev
}
