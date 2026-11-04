//go:build !darwin && !linux

package services

// 不能证明原进程已退出的平台不自动删除旧目录。
func processingOwnerGone(int) bool { return false }
