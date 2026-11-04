//go:build darwin || linux

package services

import (
	"errors"
	"syscall"
)

func processingOwnerGone(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }
