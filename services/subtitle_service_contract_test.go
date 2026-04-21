package services

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGetEngineStatusesIncludesWhisperXAndQwen(t *testing.T) {
	svc := NewSubtitleService(t.TempDir())
	statuses, err := svc.GetEngineStatuses()
	if err != nil {
		t.Fatalf("获取字幕引擎状态失败: %v", err)
	}
	if len(statuses) < 2 {
		t.Fatalf("期望至少返回 2 个引擎状态，实际 %d", len(statuses))
	}
	engines := map[SubtitleEngine]SubtitleEngineStatus{}
	for _, status := range statuses {
		engines[status.Engine] = status
	}
	if _, ok := engines[SubtitleEngineWhisperX]; !ok {
		t.Fatalf("缺少 WhisperX 引擎状态")
	}
	qwen, ok := engines[SubtitleEngineQwen]
	if !ok {
		t.Fatalf("缺少 Qwen 引擎状态")
	}
	if runtime.GOOS != "darwin" && qwen.Supported {
		t.Fatalf("非 macOS 环境下 Qwen 不应标记为 supported")
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH != "arm64" && qwen.Supported {
		t.Fatalf("macOS 非 arm64 在当前实现下不应默认启用 Qwen")
	}
}

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
