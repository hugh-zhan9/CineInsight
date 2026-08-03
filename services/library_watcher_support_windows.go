//go:build windows

package services

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func classifyLibraryWatchRoot(path string) (bool, string, error) {
	if strings.HasPrefix(path, `\\`) {
		return false, "网络目录不支持可靠的实时监听", nil
	}
	volume := filepath.VolumeName(path)
	if volume == "" {
		return true, "", nil
	}
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return false, "", err
	}
	if windows.GetDriveType(root) == windows.DRIVE_REMOTE {
		return false, "网络目录不支持可靠的实时监听", nil
	}
	return true, "", nil
}
