package services

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// G-3：恢复失败的文案不带绝对路径，但错误链与前缀语义都保留（APP-01）。
func TestWithoutAbsolutePathsKeepsErrorChainAPP01(t *testing.T) {
	if WithoutAbsolutePaths(nil) != nil {
		t.Fatalf("nil 应原样返回")
	}
	plain := errors.New("备份文件校验失败，数据库未被修改")
	if WithoutAbsolutePaths(plain) != plain {
		t.Fatalf("没有路径的错误应原样返回")
	}

	linkErr := &os.LinkError{Op: "rename", Old: "/Users/someone/.CineInsight/library.db.tmp", New: "/Users/someone/.CineInsight/library.db", Err: os.ErrPermission}
	wrapped := fmt.Errorf("%s: 替换库文件失败: %w", DatabaseRestoreReasonFatal, linkErr)
	got := WithoutAbsolutePaths(wrapped)
	if strings.Contains(got.Error(), "/Users/") {
		t.Fatalf("文案仍含绝对路径: %s", got.Error())
	}
	if !strings.HasPrefix(got.Error(), DatabaseRestoreReasonFatal+":") || !strings.Contains(got.Error(), "替换库文件失败") {
		t.Fatalf("前缀或原因丢了: %s", got.Error())
	}
	if !errors.Is(got, os.ErrPermission) {
		t.Fatalf("errors.Is 应沿原错误链判断")
	}
	// 不是路径的斜杠原样保留。
	for _, text := range []string{"数据库正在恢复 / 切换后端", "SQLite/PG 都支持", "进度 5/10"} {
		if got := scrubAbsolutePaths(text); got != text {
			t.Fatalf("非路径斜杠被误擦: %q -> %q", text, got)
		}
	}
	if got := scrubAbsolutePaths("lstat /Volumes/Movies/a.mkv: permission denied"); got != "lstat <path>: permission denied" {
		t.Fatalf("路径与尾随冒号处理不对: %q", got)
	}
	var asLink *os.LinkError
	if !errors.As(got, &asLink) || asLink != linkErr {
		t.Fatalf("errors.As 应拿到原错误")
	}
}
