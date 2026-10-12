//go:build !darwin && !linux

package services

// backupBlockSize 在其他平台（含 Windows）上取不到块大小，返回 0，由调用方按 4 KiB 算。
func backupBlockSize(path string) int64 {
	return 0
}
