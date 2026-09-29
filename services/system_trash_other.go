//go:build !darwin || !cgo

package services

// 非 macOS（或没有 cgo 的 darwin 构建）没有系统废纸篓：一律报「不支持」，由调用方按
// D-PC02 让用户在「永久删除」与「只从片库移除」之间选择，不做任何降级（G-6）。
func moveToSystemTrash(path string) (string, error) {
	return "", ErrTrashUnsupportedVolume
}
