//go:build windows

package services

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// applyEnhancementProcessGroup 让 sidecar 独占一个进程组。Windows 没有 POSIX 进程组语义，
// 使用 CREATE_NEW_PROCESS_GROUP 取得最接近的效果。
//
// 超分能力本身仅面向 macOS（见 docs/loopx/design/2026-08-04-video-super-resolution），
// 本文件只保证 Windows 可编译，未在 Windows 上实测。
func applyEnhancementProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killEnhancementProcessGroup 终止 sidecar 进程。Windows 无法用信号终止整个进程组，
// 这里直接终止主进程；其子进程由 Windows 的 job 回收机制处理。
func killEnhancementProcessGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = command.Process.Kill()
}

// enhancementDiskFree 返回 path 所在卷对当前用户可用的字节数。
func enhancementDiskFree(path string) (uint64, error) {
	target, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(target, &freeToCaller, &total, &totalFree); err != nil {
		return 0, err
	}
	return freeToCaller, nil
}
