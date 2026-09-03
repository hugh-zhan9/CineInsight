//go:build !darwin

package services

// 非 darwin 平台没有桌面通知与 Dock 角标（D-012）：系统通知与 Dock 都是 macOS 的
// 概念，这里既没有等价物也不打算引入第三方库。设置页对这个开关标注了「仅 macOS」。

type noopDesktopNotifier struct{}

func newPlatformDesktopNotifier() DesktopNotifier {
	return noopDesktopNotifier{}
}

func (noopDesktopNotifier) Notify(string, string) {}

func (noopDesktopNotifier) SetBadge(string) {}
