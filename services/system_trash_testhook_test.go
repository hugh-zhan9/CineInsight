package services

import (
	"fmt"
	"os"
	"path/filepath"
)

// 测试进程里的系统废纸篓一律换成替身：把文件重命名进「原目录/.Trash」（同卷重命名，inode 与 mtime 不变，
// 与真实废纸篓一致）。这样任何测试调用删除路径都不会碰用户真实的 ~/.Trash。
// 需要真实系统废纸篓的用例见 system_trash_darwin_test.go，它直接调用 moveToSystemTrash。
func init() {
	systemTrashMove = fakeSystemTrashMove
	trashLookupDirs = fakeTrashLookupDirs
}

func fakeTrashLookupDirs(originalPath string) []string {
	return []string{filepath.Join(filepath.Dir(originalPath), ".Trash")}
}

func fakeSystemTrashMove(path string) (string, error) {
	dir := filepath.Join(filepath.Dir(path), ".Trash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := base[:len(base)-len(ext)]
	target := filepath.Join(dir, base)
	for attempt := 2; ; attempt++ {
		if _, err := os.Lstat(target); os.IsNotExist(err) {
			break
		}
		target = filepath.Join(dir, fmt.Sprintf("%s %d%s", name, attempt, ext))
	}
	if err := os.Rename(path, target); err != nil {
		return "", err
	}
	return target, nil
}
