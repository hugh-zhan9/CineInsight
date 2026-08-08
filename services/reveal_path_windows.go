//go:build windows

package services

import "syscall"

// revealSysProcAttr 用原始命令行绕开 Go 的参数转义，让 explorer 能认出 /select 开关。
func revealSysProcAttr(cmdLine string) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CmdLine: "explorer " + cmdLine}
}
