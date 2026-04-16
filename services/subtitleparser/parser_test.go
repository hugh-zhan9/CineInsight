package subtitleparser

import (
	"os"
	"path/filepath"
	"strings"
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

func TestParseSkipsMalformedBlocks(t *testing.T) {
	content := strings.Join([]string{
		"1",
		"00:00:00,000 --> 00:00:01,000",
		"good first",
		"",
		"bad-index",
		"00:00:01 --> 00:00:02,000",
		"broken",
		"",
		"3",
		"00:00:02,000 --> 00:00:03,000",
		"good second",
	}, "\n")

	segments, err := Parse(content)
	if err != nil {
		t.Fatalf("解析 SRT 失败: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("期望跳过坏块后剩余 2 个 segment，实际 %d", len(segments))
	}
	if segments[0].Text != "good first" || segments[1].Text != "good second" {
		t.Fatalf("跳过坏块后的文本不符合预期: %#v", segments)
	}
}

func TestParseAcceptsCueSettingsAndMissingIndex(t *testing.T) {
	content := "00:00:01.000 --> 00:00:02.500 align:start position:0%\nhello world\n"

	segments, err := Parse(content)
	if err != nil {
		t.Fatalf("解析带 cue settings 的 SRT 失败: %v", err)
	}
	if len(segments) != 1 {
		t.Fatalf("期望 1 个 segment，实际 %d", len(segments))
	}
	if segments[0].Index != 1 {
		t.Fatalf("缺失 index 时应回退到块序号: got=%d want=1", segments[0].Index)
	}
	if segments[0].StartTimeMs != 1000 || segments[0].EndTimeMs != 2500 {
		t.Fatalf("时间解析错误: got=%d-%d want=1000-2500", segments[0].StartTimeMs, segments[0].EndTimeMs)
	}
}

func TestSRTPathForVideo(t *testing.T) {
	videoPath := filepath.Join("/tmp", "demo.video.mp4")
	if got := SRTPathForVideo(videoPath); got != filepath.Join("/tmp", "demo.video.srt") {
		t.Fatalf("字幕路径推导错误: got=%q", got)
	}
}
