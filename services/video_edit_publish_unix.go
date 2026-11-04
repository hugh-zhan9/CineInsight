//go:build darwin || linux

package services

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// editRenameNoReplace 优先使用排他 rename；目标卷不支持时排他复制并刷盘，完成后才删除暂存源。
// 复制期间目标可见，异常退出可能留部分副本；两条路径均不覆盖已有目标。
func editRenameNoReplace(source, destination string) error {
	return editRenameNoReplaceWith(source, destination, publishConsolidationNoReplace)
}

func editRenameNoReplaceWith(source, destination string, rename func(string, string) error) error {
	err := rename(source, destination)
	if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	if err := copyFileNoReplace(source, destination); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil {
		return errors.Join(err, os.Remove(destination))
	}
	return nil
}
