//go:build darwin || linux

package services

// editRenameNoReplace 在支持排他 rename 的平台（darwin RENAME_EXCL / linux RENAME_NOREPLACE）上
// 原子地拒绝覆盖：目标已存在时返回 os.ErrExist 系错误。
func editRenameNoReplace(source, destination string) error {
	return publishConsolidationNoReplace(source, destination)
}
