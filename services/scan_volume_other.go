//go:build !darwin

package services

import "os"

// Other platforms still validate the configured root's existence and identity.
func scanVolumeAvailable(string) error { return nil }

// fileLinkCount 在非 macOS 平台上读不到硬链接数：返回 false，旧版 trash/ 的残留名字一律不删（m1）。
func fileLinkCount(os.FileInfo) (uint64, bool) { return 0, false }
