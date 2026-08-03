//go:build linux

package services

import "syscall"

const (
	nfsSuperMagic  = 0x6969
	cifsSuperMagic = 0xff534d42
	smb2SuperMagic = 0xfe534d42
	fuseSuperMagic = 0x65735546
)

func classifyLibraryWatchRoot(path string) (bool, string, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false, "", err
	}
	switch uint64(stat.Type) {
	case nfsSuperMagic, cifsSuperMagic, smb2SuperMagic, fuseSuperMagic:
		return false, "此文件系统不支持可靠的实时监听", nil
	default:
		return true, "", nil
	}
}
