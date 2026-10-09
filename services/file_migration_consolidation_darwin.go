//go:build darwin

package services

import "golang.org/x/sys/unix"

// RENAME_EXCL 在同一系统调用内拒绝已有目标，也适用于不支持硬链接的目标卷。
func publishConsolidationNoReplace(source, destination string) error {
	return unix.RenamexNp(source, destination, unix.RENAME_EXCL)
}
