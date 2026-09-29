//go:build !unix

package services

import "os"

// fileLinkCount 在非 unix 平台上读不到硬链接数：返回 false，旧版 trash/ 的残留名字一律不删（m1、修复 I m-f）。
func fileLinkCount(os.FileInfo) (uint64, bool) { return 0, false }
