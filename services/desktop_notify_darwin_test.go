//go:build darwin

package services

import "testing"

// TestDesktopNotifierDarwinCompiles 把 cgo 那一段钉在测试里：只要这条用例还在，
// darwin 上的 objective-c 就必须能编过、能被真的调一次而不崩。
//
// 测试进程不是 .app bundle，也没有 NSApplication 实例，NSApp 是 nil——实现里的判空
// 分支正好在这里被覆盖。dispatch 到主队列的 block 在测试进程里永远不会执行
// （Go 不跑 NSRunLoop），所以这条用例不会真的改动任何 Dock 角标。
// 通知的视觉效果没法在单测里验证，留给真机验收（TC-04）。
func TestDesktopNotifierDarwinCompiles(t *testing.T) {
	NewDesktopNotifier().SetBadge("")
}
