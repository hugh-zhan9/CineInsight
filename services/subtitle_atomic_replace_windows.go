//go:build windows

package services

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func replaceSubtitleFileAtomically(temporaryPath, targetPath string) error {
	from, err := windows.UTF16PtrFromString(temporaryPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncSubtitleParentDirectory(path string) error {
	_ = filepath.Clean(path)
	return nil
}
