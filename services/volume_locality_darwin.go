//go:build darwin

package services

import (
	"path/filepath"
	"strings"
	"syscall"
)

const darwinMountLocalFlag uint32 = 0x00001000 // MNT_LOCAL in <sys/mount.h>

func isObviouslyExternalPath(cleaned string) bool {
	return cleaned == "/Volumes" || strings.HasPrefix(cleaned, "/Volumes/")
}

func probeVolumeLocality(cleaned string) bool {
	dir := filepath.Dir(cleaned)
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return false
	}
	return (st.Flags & darwinMountLocalFlag) == 0
}
