package services

import (
	"strings"
	"testing"
)

// 字幕依赖的自动安装只在 macOS 上有实现。非 macOS 平台不该给出一个看起来
// 还会再试一次的占位错误，而要明确说清「本平台不支持自动下载、请手工安装」。
// PrepareEngine 与 downloadFFmpeg 的平台分支都返回这一个哨兵错误，
// 两处不该有两套文案。
func TestErrSubtitleDependencyUnsupportedPlatformStatesManualInstall(t *testing.T) {
	message := ErrSubtitleDependencyUnsupportedPlatform.Error()

	for _, want := range []string{"当前平台不支持自动下载", "手工安装"} {
		if !strings.Contains(message, want) {
			t.Fatalf("错误文案应当包含 %q，实际为 %q", want, message)
		}
	}
}
