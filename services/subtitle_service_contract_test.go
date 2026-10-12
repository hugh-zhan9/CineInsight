package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSRTReturnsTypedValidationError(t *testing.T) {
	svc := NewSubtitleService(t.TempDir())
	srtPath := filepath.Join(t.TempDir(), "hallucination.srt")
	content := "1\n00:00:00,000 --> 00:00:01,000\nhello\n\n2\n00:00:01,000 --> 00:00:02,000\nhello\n\n3\n00:00:02,000 --> 00:00:03,000\nhello\n\n4\n00:00:03,000 --> 00:00:04,000\nhello\n\n5\n00:00:04,000 --> 00:00:05,000\nhello\n\n6\n00:00:05,000 --> 00:00:06,000\nhello\n"
	if err := os.WriteFile(srtPath, []byte(content), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}
	err := svc.validateSRT(srtPath)
	if err == nil {
		t.Fatalf("期望返回校验失败错误")
	}
	var validationErr *SubtitleValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("期望返回 SubtitleValidationError，实际 %T", err)
	}
	if validationErr.Code != SubtitleValidationCodeHallucinationDetected {
		t.Fatalf("校验码错误: got=%s", validationErr.Code)
	}
	if !validationErr.ForceEligible {
		t.Fatalf("期望当前校验失败可进入强制生成")
	}
}

func TestValidateSRTKeepsRejectedFileForForceGenerate(t *testing.T) {
	svc := NewSubtitleService(t.TempDir())
	srtPath := filepath.Join(t.TempDir(), "force-eligible.srt")
	content := "1\n00:00:00,000 --> 00:00:01,000\nsame line\n\n2\n00:00:01,000 --> 00:00:02,000\nsame line\n\n3\n00:00:02,000 --> 00:00:03,000\nsame line\n\n4\n00:00:03,000 --> 00:00:04,000\nsame line\n"
	if err := os.WriteFile(srtPath, []byte(content), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}

	err := svc.validateSRT(srtPath)
	if err == nil {
		t.Fatalf("期望返回校验失败错误")
	}
	if _, statErr := os.Stat(srtPath); statErr != nil {
		t.Fatalf("校验失败文件应保留用于强制生成，stat err=%v", statErr)
	}
}

func TestMergeBilingualSRTAtomicallyReplacesOriginal(t *testing.T) {
	svc := NewSubtitleService(t.TempDir())
	root := t.TempDir()
	originalPath := filepath.Join(root, "movie.srt")
	translatedPath := filepath.Join(root, "translated.srt")
	if err := os.WriteFile(originalPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nHello\n\n"), 0644); err != nil {
		t.Fatalf("写入原字幕失败: %v", err)
	}
	if err := os.WriteFile(translatedPath, []byte("1\n00:00:00,000 --> 00:00:01,000\n你好\n\n"), 0644); err != nil {
		t.Fatalf("写入翻译字幕失败: %v", err)
	}

	if err := svc.mergeBilingualSRT(originalPath, translatedPath, originalPath); err != nil {
		t.Fatalf("合并双语字幕失败: %v", err)
	}
	data, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("读取合并字幕失败: %v", err)
	}
	if got := string(data); !strings.Contains(got, "Hello\n你好") {
		t.Fatalf("双语字幕内容错误: %q", got)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".movie.srt.tmp-*"))
	if err != nil {
		t.Fatalf("检查临时字幕失败: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("原子替换后不应残留临时字幕: %v", matches)
	}
}

func TestNormalizeSubtitleSourceLangForASR(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "blank defaults to auto", input: "", want: "auto"},
		{name: "auto stays auto", input: "auto", want: "auto"},
		{name: "chinese display value becomes whisper code", input: "Chinese", want: "zh"},
		{name: "english display value becomes whisper code", input: "english", want: "en"},
		{name: "japanese display value becomes whisper code", input: " japanese ", want: "ja"},
		{name: "language code remains normalized", input: "KO", want: "ko"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeSubtitleSourceLangForASR(tt.input); got != tt.want {
				t.Fatalf("normalizeSubtitleSourceLangForASR(%q)=%q want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateSRTRejectsTokenizedZeroDurationSubtitles(t *testing.T) {
	svc := NewSubtitleService(t.TempDir())
	srtPath := filepath.Join(t.TempDir(), "tokenized.srt")
	content := ""
	for i := 1; i <= 40; i++ {
		content += fmt.Sprintf("%d\n00:02:16,160 --> 00:02:16,160\n%c\n\n", i, rune(0x4e00+i))
	}
	if err := os.WriteFile(srtPath, []byte(content), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}

	err := svc.validateSRT(srtPath)
	if err == nil {
		t.Fatalf("期望逐字零时长字幕校验失败")
	}
	var validationErr *SubtitleValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("期望返回 SubtitleValidationError，实际 %T", err)
	}
	if validationErr.Code != SubtitleValidationCodeHallucinationDetected {
		t.Fatalf("校验码错误: got=%s", validationErr.Code)
	}
}
