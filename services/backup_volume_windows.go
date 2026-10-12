//go:build windows

package services

import (
	"path/filepath"
	"strings"
)

// backupVolumeID 返回 path 所在卷的标识（盘符或 UNC 共享）：同一个卷上的空间需求要合在一起算。
func backupVolumeID(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", pathlessError(err)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", pathlessError(err)
	}
	return strings.ToLower(filepath.VolumeName(absolute)), nil
}
