package services

import (
	"sync"
	"testing"
)

// stubDesktopNotifier 记录投递过的通知与角标，供各触发点断言"发一次且仅一次"。
type stubDesktopNotifier struct {
	mu       sync.Mutex
	messages []stubNotification
	badges   []string
}

type stubNotification struct {
	Title string
	Body  string
}

func (s *stubDesktopNotifier) Notify(title, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, stubNotification{Title: title, Body: body})
}

func (s *stubDesktopNotifier) SetBadge(label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.badges = append(s.badges, label)
}

func (s *stubDesktopNotifier) Notifications() []stubNotification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubNotification(nil), s.messages...)
}

func (s *stubDesktopNotifier) Badges() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.badges...)
}

func alwaysEnabled() bool { return true }

func TestBadgeLabelForCountClearsAtZero(t *testing.T) {
	cases := map[int]string{-1: "", 0: "", 1: "1", 2: "2", 12: "12"}
	for count, want := range cases {
		if got := BadgeLabelForCount(count); got != want {
			t.Fatalf("BadgeLabelForCount(%d) = %q, 期望 %q", count, got, want)
		}
	}
}

// 前端还没上报过前后台时默认视为后台（设计 4.3.4「宁多不少」）。
func TestDesktopNotificationCenterTreatsUnreportedWindowAsBackground(t *testing.T) {
	stub := &stubDesktopNotifier{}
	center := NewDesktopNotificationCenter(stub, alwaysEnabled)

	center.Notify("字幕生成完成", "《a.mp4》字幕已生成")

	if got := stub.Notifications(); len(got) != 1 {
		t.Fatalf("未上报前后台时应照常投递一条，实际 %+v", got)
	}
}

func TestDesktopNotificationCenterSuppressesWhenForeground(t *testing.T) {
	stub := &stubDesktopNotifier{}
	center := NewDesktopNotificationCenter(stub, alwaysEnabled)

	center.SetForeground(true)
	center.Notify("字幕生成完成", "《a.mp4》字幕已生成")
	if got := stub.Notifications(); len(got) != 0 {
		t.Fatalf("窗口在前台时不该发系统通知，实际 %+v", got)
	}

	// 切回后台后恢复投递。
	center.SetForeground(false)
	center.Notify("字幕生成完成", "《a.mp4》字幕已生成")
	if got := stub.Notifications(); len(got) != 1 {
		t.Fatalf("切回后台后应投递一条，实际 %+v", got)
	}
}

func TestDesktopNotificationCenterSuppressesWhenSettingDisabled(t *testing.T) {
	stub := &stubDesktopNotifier{}
	enabled := false
	center := NewDesktopNotificationCenter(stub, func() bool { return enabled })

	center.Notify("字幕生成完成", "《a.mp4》字幕已生成")
	if got := stub.Notifications(); len(got) != 0 {
		t.Fatalf("开关关掉时不该发系统通知，实际 %+v", got)
	}

	enabled = true
	center.Notify("字幕生成完成", "《a.mp4》字幕已生成")
	if got := stub.Notifications(); len(got) != 1 {
		t.Fatalf("开关打开后应投递一条，实际 %+v", got)
	}
}

// 读不到开关（数据库异常）时不发：与其猜一个默认值不如安静。
func TestDesktopNotificationCenterWithoutEnabledProviderSendsNothing(t *testing.T) {
	stub := &stubDesktopNotifier{}
	center := NewDesktopNotificationCenter(stub, nil)

	center.Notify("数据库备份失败", "备份未完成")

	if got := stub.Notifications(); len(got) != 0 {
		t.Fatalf("没有开关提供者时不该发通知，实际 %+v", got)
	}
}

// 角标是客观事实：开关关掉、窗口在前台都照常更新（设计 4.3.4）。
func TestDesktopNotificationCenterBadgeIgnoresSwitchAndForeground(t *testing.T) {
	stub := &stubDesktopNotifier{}
	center := NewDesktopNotificationCenter(stub, func() bool { return false })
	center.SetForeground(true)

	center.SetBadge("2")
	center.SetBadge("")

	if got := stub.Badges(); len(got) != 2 || got[0] != "2" || got[1] != "" {
		t.Fatalf("角标应无条件直通，实际 %+v", got)
	}
}

// 角标跟着登记表走（D-014）：两个任务在跑显示 2，都结束后清空。
// 接线方式与 app.startup 里的 SetOnChange 一致。
func TestDockBadgeFollowsBackgroundTaskRegistry(t *testing.T) {
	stub := &stubDesktopNotifier{}
	center := NewDesktopNotificationCenter(stub, alwaysEnabled)
	registry := NewBackgroundTaskRegistry()
	registry.SetOnChange(func(running []string) {
		center.SetBadge(BadgeLabelForCount(len(running)))
	})

	registry.Begin(BackgroundTaskSubtitle)
	registry.Begin(BackgroundTaskEnhancement)
	registry.End(BackgroundTaskEnhancement)
	registry.End(BackgroundTaskSubtitle)

	want := []string{"1", "2", "1", ""}
	got := stub.Badges()
	if len(got) != len(want) {
		t.Fatalf("角标序列长度 = %d，期望 %d：%+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("角标序列 = %+v，期望 %+v", got, want)
		}
	}
}

func TestNotificationMediaNameNeverEmpty(t *testing.T) {
	if got := notificationMediaName("  "); got != "未命名视频" {
		t.Fatalf("空显示名应有兜底文案，实际 %q", got)
	}
	if got := notificationMediaName(" movie.mp4 "); got != "movie.mp4" {
		t.Fatalf("显示名应去掉首尾空白，实际 %q", got)
	}
}
