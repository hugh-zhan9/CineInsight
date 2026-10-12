package services

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAppleSubtitleSegmentsPreserveTimesAndReadableGrouping(t *testing.T) {
	words := []appleWord{{0, 0.2, "Hello"}, {0.2, 0.6, " world."}, {1.1, 1.3, "今"}, {1.3, 1.5, "天"}, {1.5, 1.8, "好。"}}
	result, err := appleSubtitleSegments(words)
	if err != nil || len(result) != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	if result[0].Text != "Hello world." || result[0].StartTimeMs != 0 || result[0].EndTimeMs != 600 || result[1].Text != "今天好。" || result[1].StartTimeMs != 1100 || result[1].EndTimeMs != 1800 {
		t.Fatalf("bad boundaries: %+v", result)
	}
}
func TestAppleSubtitleSegmentsRejectCorruptAndEmptyOutput(t *testing.T) {
	for _, words := range [][]appleWord{nil, {{0, 0, "zero"}}, {{2, 1, "reverse"}}, {{math.NaN(), 1, "nan"}}, {{0, math.Inf(1), "inf"}}, {{2, 3, "a"}, {1, 2, "b"}}, {{0, 2, "a."}, {1, 3, "b."}}} {
		if _, err := appleSubtitleSegments(words); err == nil {
			t.Fatalf("accepted invalid %+v", words)
		}
	}
}
func TestAppleSubtitleTranslatorUsesSystemDespiteLegacyConfiguration(t *testing.T) {
	s := NewSubtitleService(t.TempDir())
	var received appleAIRequest
	s.appleRunner = func(ctx context.Context, r appleAIRequest) (appleAIResponse, error) {
		received = r
		return appleAIResponse{Translations: []string{"你好"}}, nil
	}
	translator, err := s.subtitleTranslator("llm", SubtitleTranslationConfig{Provider: "llm", BaseURL: "https://must-not-be-called.invalid", APIKey: "obsolete"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := translator.Translate(context.Background(), []string{"Hello"}, "en", "zh")
	if err != nil || !reflect.DeepEqual(result, []string{"你好"}) || received.Operation != "translate" || received.Source != "en" || received.Target != "zh" {
		t.Fatalf("%+v %+v %v", received, result, err)
	}
	s.appleRunner = func(context.Context, appleAIRequest) (appleAIResponse, error) {
		return appleAIResponse{Translations: []string{}}, nil
	}
	if _, err = translator.Translate(context.Background(), []string{"Hello"}, "en", "zh"); err == nil {
		t.Fatal("missing response must fail")
	}
}
func TestAppleSubtitleTranslatorEmptyAndCancelledRequestsDoNotRun(t *testing.T) {
	tr := &appleSubtitleTranslator{run: func(context.Context, appleAIRequest) (appleAIResponse, error) {
		t.Fatal("must not call system")
		return appleAIResponse{}, nil
	}}
	if result, err := tr.Translate(context.Background(), nil, "en", "zh"); err != nil || len(result) != 0 {
		t.Fatal(result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tr.Translate(ctx, []string{"a"}, "en", "zh"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestAppleOnlyEngineStatus(t *testing.T) {
	s := NewSubtitleService(t.TempDir())
	s.appleRunner = func(context.Context, appleAIRequest) (appleAIResponse, error) {
		return appleAIResponse{Supported: true, Languages: []AppleLanguage{{Code: "en_US", Installed: true}}}, nil
	}
	statuses, err := s.GetEngineStatuses()
	if err != nil || len(statuses) != 1 || statuses[0].Engine != SubtitleEngineApple || len(statuses[0].Languages) != 1 {
		t.Fatalf("%+v %v", statuses, err)
	}
}
func TestApplePreparationSerialAndCancellable(t *testing.T) {
	s := NewSubtitleService(t.TempDir())
	started := make(chan struct{})
	done := make(chan error, 1)
	s.appleRunner = func(ctx context.Context, r appleAIRequest) (appleAIResponse, error) {
		close(started)
		<-ctx.Done()
		return appleAIResponse{}, ctx.Err()
	}
	go func() { done <- s.PrepareAppleLanguage("en_US", "", false) }()
	<-started
	if err := s.PrepareAppleLanguage("zh_CN", "", false); !errors.Is(err, ErrSubtitleEnginePreparing) {
		t.Fatal(err)
	}
	if !s.CancelEnginePreparation() {
		t.Fatal("missing cancel")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrSubtitleEnginePreparationCancelled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not cancel")
	}
	if s.CancelEnginePreparation() {
		t.Fatal("cancel registration leaked")
	}
}
func TestAppleProcessProtocolAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "helper")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\ncat >/dev/null\n"+body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("printf '%s' '{\"translations\":[\"ok\"]}'\n")
	if result, err := runAppleAIProcess(context.Background(), path, appleAIRequest{Operation: "translate"}); err != nil || len(result.Translations) != 1 {
		t.Fatal(result, err)
	}
	write("printf '%s' 'not-json'\n")
	if _, err := runAppleAIProcess(context.Background(), path, appleAIRequest{}); err == nil {
		t.Fatal("bad JSON accepted")
	}
	write("printf '%s' '{\"error\":\"apple_translation_language_missing\"}'\n")
	if _, err := runAppleAIProcess(context.Background(), path, appleAIRequest{}); err == nil || !strings.Contains(err.Error(), "apple_translation_language_missing") {
		t.Fatal(err)
	}
	write("exec sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := runAppleAIProcess(ctx, path, appleAIRequest{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestSharedPreparationCommandCancelsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix process group fixture")
	}
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := preparationCommand(ctx, "/bin/sh", "-c", `sleep 30 & echo started > "$1"; wait`, "test", marker)
	done := make(chan error, 1)
	go func() { done <- command.Run() }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unexpected success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("process group did not stop")
	}
}
func TestSharedVenvCheckMissingPipAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	path := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if ok, err := venvUsable(context.Background(), path, func(string) bool { return true }); ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if ok, err := venvUsable(ctx, path, func(string) bool { return true }); ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(ok, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("cancel removed interpreter")
	}
}

func TestApplePreparationShutdownCancelsJoinsAndRejectsNewWork(t *testing.T) {
	s := NewSubtitleService(t.TempDir())
	started := make(chan struct{})
	finished := make(chan struct{})
	s.appleRunner = func(ctx context.Context, _ appleAIRequest) (appleAIResponse, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return appleAIResponse{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- s.PrepareAppleLanguage("en_US", "", false) }()
	<-started
	s.StopApplePreparation()
	select {
	case <-finished:
	default:
		t.Fatal("shutdown returned before helper stopped")
	}
	if err := <-done; !errors.Is(err, ErrSubtitleEnginePreparationCancelled) {
		t.Fatal(err)
	}
	if err := s.PrepareAppleLanguage("en_US", "", false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// D-004 (2026-10-13): macOS 15 runs the helper's older system paths; anything older stays unavailable.
func TestApplePlatformGateAcceptsMacOS15AndLater(t *testing.T) {
	for version, want := range map[string]bool{"15.0": true, "15.7.1\n": true, "26.0": true, "27.0": true, "14.7.1": false, "13": false, "": false, "unknown": false} {
		if got := appleAIPlatformSupported(version); got != want {
			t.Fatalf("%q: got %v, want %v", version, got, want)
		}
	}
}
func TestAppleErrorMessagesForMacOS15Paths(t *testing.T) {
	for code, want := range map[string]string{
		"apple_platform_unsupported":     "需要 macOS 15 或更新版本",
		"apple_speech_permission_denied": "「系统设置 › 隐私与安全性 › 语音识别」中允许析微影策",
		"apple_speech_dictation_missing": "「系统设置 › 键盘 › 听写」中添加该语言",
		"apple_face_unsupported":         "人脸识别不可用",
		"apple_face_version_changed":     "人脸识别不可用",
	} {
		if err := appleAIError(code); !strings.HasPrefix(err.Error(), code+": ") || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", code, err)
		}
	}
}
