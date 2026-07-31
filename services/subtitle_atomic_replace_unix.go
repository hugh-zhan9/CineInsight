//go:build !windows

package services

import "os"

func replaceSubtitleFileAtomically(temporaryPath, targetPath string) error {
	return os.Rename(temporaryPath, targetPath)
}

func syncSubtitleParentDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
