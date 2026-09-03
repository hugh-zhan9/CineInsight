package services

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// DesktopNotifier 是桌面通知与 Dock 角标的最小表面（D-012）。
// 只有 darwin 有真实实现（NSUserNotificationCenter + NSApp.dockTile），
// 其他平台是空实现——这一层存在的意义就是让长任务服务不必知道自己跑在哪。
type DesktopNotifier interface {
	// Notify 投递一条系统通知；调用立即返回，投递本身是异步的。
	Notify(title, body string)
	// SetBadge 设置 Dock 角标文字，空串清空角标。
	SetBadge(label string)
}

// NewDesktopNotifier 返回当前平台的实现（darwin 之外为空实现）。
func NewDesktopNotifier() DesktopNotifier {
	return newPlatformDesktopNotifier()
}

// BadgeLabelForCount 把运行中任务数翻成角标文字（D-014）：0 个任务角标为空串。
func BadgeLabelForCount(count int) string {
	if count <= 0 {
		return ""
	}
	return strconv.Itoa(count)
}

// DesktopNotificationCenter 决定一条通知发不发（D-013）。
//
// 两条抑制规则：窗口在前台时不发（应用内 AppFeedback 已经提示过同一件事），
// 设置里的开关关掉时不发。角标不受这两条影响——它表达的是"现在有几个任务在跑"
// 这个客观事实，跟用户在看哪个窗口无关。
//
// foreground 的零值是 false：前端还没上报过就当作后台，宁可多一条通知也不少
// （设计 4.3.4「前端未上报前后台」）。
type DesktopNotificationCenter struct {
	notifier DesktopNotifier
	enabled  func() bool

	mu         sync.RWMutex
	foreground bool
}

// NewDesktopNotificationCenter 组装通知中心。enabled 为 nil 时一律不发通知：
// 读不到开关就当用户没同意，比猜一个默认值更安全。
func NewDesktopNotificationCenter(notifier DesktopNotifier, enabled func() bool) *DesktopNotificationCenter {
	return &DesktopNotificationCenter{notifier: notifier, enabled: enabled}
}

// SetForeground 记录窗口当前是否在前台（前端 focus / blur / visibilitychange 上报）。
func (c *DesktopNotificationCenter) SetForeground(foreground bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.foreground = foreground
	c.mu.Unlock()
}

// Foreground 返回最近一次上报的前后台标记。
func (c *DesktopNotificationCenter) Foreground() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.foreground
}

// Notify 在开关开着且窗口不在前台时投递系统通知。
func (c *DesktopNotificationCenter) Notify(title, body string) {
	if c == nil || c.notifier == nil {
		return
	}
	if c.Foreground() {
		return
	}
	if c.enabled == nil || !c.enabled() {
		return
	}
	c.notifier.Notify(title, body)
}

// SetBadge 直通到平台实现：角标不受开关与前后台影响。
func (c *DesktopNotificationCenter) SetBadge(label string) {
	if c == nil || c.notifier == nil {
		return
	}
	c.notifier.SetBadge(label)
}

// notifyDesktop 是各长任务服务的统一入口：没接通知器就什么都不做。
func notifyDesktop(notifier DesktopNotifier, title, body string) {
	if notifier == nil {
		return
	}
	notifier.Notify(title, body)
}

// notificationMediaName 取通知文案里的媒体显示名。
// 通知里只出现显示名，绝不出现绝对路径（通知会进系统通知中心，那是另一份留存）。
func notificationMediaName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "未命名视频"
	}
	return trimmed
}

// notifySemanticIndexTerminal 是视频与图片语义索引共用的终态文案（D-013）。
// 取消不发：那是用户自己按的。
func notifySemanticIndexTerminal(notifier DesktopNotifier, taskName string, cancelled bool, succeeded, failed int) {
	if notifier == nil || cancelled {
		return
	}
	if failed > 0 {
		notifyDesktop(notifier, taskName+"失败", fmt.Sprintf("%d 项索引失败，已成功 %d 项", failed, succeeded))
		return
	}
	notifyDesktop(notifier, taskName+"完成", fmt.Sprintf("已索引 %d 项", succeeded))
}
