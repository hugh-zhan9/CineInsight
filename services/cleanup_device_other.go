//go:build !unix

package services

import "os"

// cleanupFileDevice 在没有 st_dev 的平台上报告"卷未知"，调用方按最保守的并发处理。
func cleanupFileDevice(os.FileInfo) (uint64, bool) {
	return 0, false
}
