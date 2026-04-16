package subtitleparser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePreservesMultilineBlocks(t *testing.T) {
	content := "\ufeff1\r\n00:00:01,000 --> 00:00:03,500\r\nfirst line\r\nsecond line\r\n\r\n2\r\n00:00:04,000 --> 00:00:05,250\r\nthird line\r\n"

	segments, err := Parse(content)
	if err != nil {
		t.Fatalf("解析 SRT 失败: %v", err)
	}

	if len(segments) != 2 {
		t.Fatalf("期望解析出 2 个 segment，实际 %d", len(segments))
	}

	first := segments[0]
	if first.Index != 1 {
		t.Fatalf("首个 segment index 错误: got=%d want=1", first.Index)
	}
	if first.StartTimeMs != 1000 || first.EndTimeMs != 3500 {
		t.Fatalf("首个 segment 时间错误: got=%d-%d want=1000-3500", first.StartTimeMs, first.EndTimeMs)
	}
	if first.Text != "first line\nsecond line" {
		t.Fatalf("首个 segment 文本错误: %q", first.Text)
	}
	if len(first.Lines) != 2 || first.Lines[0] != "first line" || first.Lines[1] != "second line" {
		t.Fatalf("首个 segment 行内容错误: %#v", first.Lines)
	}

	second := segments[1]
	if second.Index != 2 {
		t.Fatalf("第二个 segment index 错误: got=%d want=2", second.Index)
	}
	if second.StartTimeMs != 4000 || second.EndTimeMs != 5250 {
		t.Fatalf("第二个 segment 时间错误: got=%d-%d want=4000-5250", second.StartTimeMs, second.EndTimeMs)
	}
	if second.Text != "third line" {
		t.Fatalf("第二个 segment 文本错误: %q", second.Text)
	}
}

func TestParseFileReadsStructuredSegments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.srt")
	content := "1\n00:00:00,000 --> 00:00:01,200\nhello world\n"

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写入测试 SRT 失败: %v", err)
	}

	segments, err := ParseFile(path)
	if err != nil {
		t.Fatalf("从文件解析 SRT 失败: %v", err)
	}

	if len(segments) != 1 {
		t.Fatalf("期望 1 个 segment，实际 %d", len(segments))
	}
	if segments[0].Text != "hello world" {
		t.Fatalf("文本错误: %q", segments[0].Text)
	}
}

func TestParseRejectsMalformedTimestamp(t *testing.T) {
	content := "1\n00:00:01 --> 00:00:02,000\nbroken\n"

	_, err := Parse(content)
	if err == nil {
		t.Fatalf("期望 malformed timestamp 返回错误")
	}
}

func TestSRTPathForVideo(t *testing.T) {
	videoPath := filepath.Join("/tmp", "demo.video.mp4")
	if got := SRTPathForVideo(videoPath); got != filepath.Join("/tmp", "demo.video.srt") {
		t.Fatalf("字幕路径推导错误: got=%q", got)
	}
}
