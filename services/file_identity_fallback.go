//go:build !darwin && !linux

package services

import "os"

func stableFileIdentity(os.FileInfo) string {
	return ""
}
