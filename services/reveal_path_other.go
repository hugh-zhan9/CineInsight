//go:build !windows

package services

import "syscall"

// revealSysProcAttr 只有 Windows 需要自定义命令行；其他平台返回 nil 走常规参数传递。
func revealSysProcAttr(string) *syscall.SysProcAttr { return nil }
