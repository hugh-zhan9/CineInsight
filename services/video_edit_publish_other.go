//go:build !darwin && !linux

package services

import (
	"errors"
	"os"
)

// editRenameNoReplace 在没有排他 rename 的平台（Windows 等）上，于调用方持有的片库路径写锁内
// 先 Lstat 再 rename，与超分发布同一保证级别：锁外第三方程序在两步之间创建同名文件的窄窗口无法关闭。
func editRenameNoReplace(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}
