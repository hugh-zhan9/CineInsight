//go:build !darwin && !linux

package services

import (
	"errors"
	"os"
)

func replacementRenameUnsupported(err error) bool  { return errors.Is(err, errReplacementNoReplace) }
func replacementNativeRename(string, string) error { return errReplacementNoReplace }

func replacementInvalidRenameArgument(error) bool         { return false }
func replacementSameVolume(os.FileInfo, os.FileInfo) bool { return false }
