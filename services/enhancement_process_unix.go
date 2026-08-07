//go:build !windows

package services

import (
	"os/exec"
	"syscall"
)

// applyEnhancementProcessGroup 让 sidecar 独占一个进程组，便于取消时连同其解码线程一起终止。
func applyEnhancementProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killEnhancementProcessGroup 终止整个子进程组。
func killEnhancementProcessGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}

// enhancementDiskFree 返回 path 所在文件系统对非特权用户可用的字节数。
func enhancementDiskFree(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
