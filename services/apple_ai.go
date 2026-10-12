package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"video-master/services/subtitleparser"
)

// AppleLanguage reports a system-supported language and whether speech resources are installed.
type AppleLanguage struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
}
type AppleLanguageCandidate struct {
	Language   string  `json:"language"`
	Confidence float64 `json:"confidence"`
}
type AppleLanguageDetection struct {
	Language   string                   `json:"language"`
	Candidates []AppleLanguageCandidate `json:"candidates"`
}
type appleAIRequest struct {
	Operation string   `json:"operation"`
	Paths     []string `json:"paths,omitempty"`
	Path      string   `json:"path,omitempty"`
	Source    string   `json:"source,omitempty"`
	Target    string   `json:"target,omitempty"`
	Texts     []string `json:"texts,omitempty"`
}
type appleWord struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}
type appleAIResponse struct {
	ModelID              string                   `json:"model_id"`
	Vectors              []string                 `json:"vectors"`
	Error                string                   `json:"error"`
	Supported            bool                     `json:"supported"`
	Languages            []AppleLanguage          `json:"languages"`
	TranslationLanguages []AppleLanguage          `json:"translationLanguages"`
	Language             string                   `json:"language"`
	Segments             []appleWord              `json:"segments"`
	Translations         []string                 `json:"translations"`
	Candidates           []AppleLanguageCandidate `json:"candidates"`
}
type appleAIRunner func(context.Context, appleAIRequest) (appleAIResponse, error)

func appleAIError(code string) error {
	messages := map[string]string{
		"apple_platform_unsupported":         "苹果系统能力需要 macOS 15 或更新版本",
		"apple_helper_missing":               "苹果系统组件未随应用打包，请重新构建或安装完整应用",
		"apple_speech_language_missing":      "该识别语言尚未准备，请先准备系统语音资源",
		"apple_speech_permission_denied":     "请在「系统设置 › 隐私与安全性 › 语音识别」中允许析微影策后重试",
		"apple_speech_dictation_missing":     "该语言的离线识别尚未下载：请在「系统设置 › 键盘 › 听写」中添加该语言并等它下载完成后重试",
		"apple_face_unsupported":             "当前 macOS 没有已验证的系统人脸特征接口，人脸识别不可用",
		"apple_face_version_changed":         "当前 macOS 的系统人脸特征版本与已验证版本不同，人脸识别不可用",
		"apple_translation_language_missing": "翻译语言尚未准备，请在字幕设置中准备对应语言",
		"apple_language_unsupported":         "苹果系统不支持所选语言或语言组合",
		"apple_language_uncertain":           "无法可靠判断语言，请手动选择源语言后重试",
		"apple_source_language_required":     "请选择要准备的源语言",
		"apple_translation_invalid_result":   "系统翻译返回的字幕条目不完整，原字幕未改动",
		"apple_system_failed":                "苹果系统处理失败，请检查语言资源后重试",
		"apple_invalid_result":               "系统字幕组件返回了无效结果",
	}
	if code == "cancelled" {
		return context.Canceled
	}
	message, ok := messages[code]
	if !ok {
		message = "苹果系统处理失败"
	}
	return fmt.Errorf("%s: %s", code, message)
}

func appleAIExecutable() string {
	const relative = "apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai"
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	if strings.Contains(executable, ".app/Contents/MacOS/") {
		return filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "Resources", relative))
	}
	// Development/test binaries use the same prebuilt helper as the packaging script.
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Join(filepath.Dir(source), "..", "build", relative)
}

// appleAIPlatformSupported reports whether a macOS product version can run the helper: macOS 26+ uses the
// new speech/translation interfaces, macOS 15 the on-device SFSpeechRecognizer and SwiftUI translation (D-004).
func appleAIPlatformSupported(productVersion string) bool {
	major, err := strconv.Atoi(strings.Split(strings.TrimSpace(productVersion), ".")[0])
	return err == nil && major >= 15
}
func runAppleAI(ctx context.Context, request appleAIRequest) (appleAIResponse, error) {
	var response appleAIResponse
	if err := ctx.Err(); err != nil {
		return response, err
	}
	if runtime.GOOS != "darwin" {
		return response, appleAIError("apple_platform_unsupported")
	}
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	version, err := exec.CommandContext(versionCtx, "/usr/bin/sw_vers", "-productVersion").Output()
	cancel()
	if err != nil || !appleAIPlatformSupported(string(version)) {
		return response, appleAIError("apple_platform_unsupported")
	}
	path := appleAIExecutable()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return response, appleAIError("apple_helper_missing")
	}
	return runAppleAIProcess(ctx, path, request)
}

func runAppleAIProcess(ctx context.Context, path string, request appleAIRequest) (appleAIResponse, error) {
	var response appleAIResponse
	payload, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	if len(payload) > 8<<20 {
		return response, errors.New("系统字幕请求过大")
	}
	cmd := exec.CommandContext(ctx, path)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.WaitDelay = 5 * time.Second
	output := &boundedBuffer{limit: 32 << 20}
	cmd.Stdout = output
	// Never surface system stderr: it can contain source paths or recognized content.
	err = cmd.Run()
	if ctx.Err() != nil {
		return response, ctx.Err()
	}
	if err != nil {
		return response, appleAIError("apple_system_failed")
	}
	if len(output.data) >= 32<<20 || json.Unmarshal(output.data, &response) != nil {
		return response, appleAIError("apple_invalid_result")
	}
	if response.Error != "" {
		return response, appleAIError(response.Error)
	}
	return response, nil
}
func (s *SubtitleService) callAppleAI(ctx context.Context, request appleAIRequest) (appleAIResponse, error) {
	if s.appleRunner != nil {
		return s.appleRunner(ctx, request)
	}
	return runAppleAI(ctx, request)
}

// DetectSubtitleLanguage samples audio using only installed Apple speech resources.
func (s *SubtitleService) DetectSubtitleLanguage(ctx context.Context, path string) (AppleLanguageDetection, error) {
	prepareCtx, finish, err := s.beginEnginePreparation()
	if err != nil {
		return AppleLanguageDetection{}, err
	}
	defer finish()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(prepareCtx, cancel)
	defer stop()
	if err := s.acquireTranscriptionSlot(ctx); err != nil {
		return AppleLanguageDetection{}, err
	}
	defer s.releaseTranscriptionSlot()
	return s.detectAppleLanguage(ctx, path)
}
func (s *SubtitleService) detectAppleLanguage(ctx context.Context, path string) (AppleLanguageDetection, error) {
	audio, err := os.CreateTemp("", "cineinsight-language-*.wav")
	if err != nil {
		return AppleLanguageDetection{}, err
	}
	sample := audio.Name()
	if err = audio.Close(); err != nil {
		os.Remove(sample)
		return AppleLanguageDetection{}, err
	}
	defer os.Remove(sample)
	ffmpeg := s.findBinary("ffmpeg")
	if ffmpeg == "" {
		return AppleLanguageDetection{}, errors.New("缺少 FFmpeg，无法提取语言检测音频")
	}
	// Bound the probe independently of full-file transcription. Silence/intro music can require manual selection.
	cmd := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", path, "-t", "30", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", sample, "-y")
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return AppleLanguageDetection{}, ctx.Err()
		}
		return AppleLanguageDetection{}, errors.New("提取语言检测音频失败")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	response, err := s.callAppleAI(probeCtx, appleAIRequest{Operation: "detect", Path: sample})
	result := AppleLanguageDetection{Language: response.Language, Candidates: response.Candidates}
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(result.Language) == "" {
		return result, appleAIError("apple_language_uncertain")
	}
	return result, nil
}
func (s *SubtitleService) transcribeApple(ctx context.Context, path, source string) (string, []subtitleparser.Segment, error) {
	if source == "" || source == "auto" {
		detection, err := s.detectAppleLanguage(ctx, path)
		if err != nil {
			return "", nil, err
		}
		source = detection.Language
	}
	response, err := s.callAppleAI(ctx, appleAIRequest{Operation: "transcribe", Path: path, Source: source})
	if err != nil {
		return "", nil, err
	}
	segments, err := appleSubtitleSegments(response.Segments)
	if err != nil {
		return "", nil, err
	}
	return response.Language, segments, nil
}

// appleSubtitleSegments groups timestamped words while keeping their authoritative audio ranges.
func appleSubtitleSegments(words []appleWord) ([]subtitleparser.Segment, error) {
	var result []subtitleparser.Segment
	var current subtitleparser.Segment
	flush := func() {
		if strings.TrimSpace(current.Text) != "" {
			current.Text = strings.TrimSpace(current.Text)
			current.Index = len(result) + 1
			result = append(result, current)
		}
		current = subtitleparser.Segment{}
	}
	var lastStart int64 = -1
	for _, word := range words {
		if math.IsNaN(word.Start) || math.IsInf(word.Start, 0) || math.IsNaN(word.End) || math.IsInf(word.End, 0) || word.Start < 0 || word.End <= word.Start || word.End > 1e9 {
			return nil, appleAIError("apple_invalid_result")
		}
		start, end := int64(math.Round(word.Start*1000)), int64(math.Round(word.End*1000))
		if start < lastStart || end <= start {
			return nil, appleAIError("apple_invalid_result")
		}
		lastStart = start
		if strings.TrimSpace(word.Text) == "" {
			continue
		}
		if current.Text != "" && (start-current.EndTimeMs > 1000 || end-current.StartTimeMs > 6000 || utf8.RuneCountInString(current.Text+word.Text) > 48) {
			flush()
		}
		if current.Text == "" {
			current.StartTimeMs = start
		}
		current.Text += word.Text
		current.EndTimeMs = end
		if strings.ContainsAny(strings.TrimSpace(word.Text), "。！？!?\n") || strings.HasSuffix(strings.TrimSpace(word.Text), ".") {
			flush()
		}
	}
	flush()
	for i := 1; i < len(result); i++ {
		if result[i].StartTimeMs < result[i-1].EndTimeMs {
			return nil, appleAIError("apple_invalid_result")
		}
	}
	if len(result) == 0 {
		return nil, errors.New("未识别到有效语音，原字幕未改动")
	}
	return result, nil
}

func (s *SubtitleService) getAppleStatus() SubtitleEngineStatus {
	status := SubtitleEngineStatus{Engine: SubtitleEngineApple, DisplayName: "Apple 系统识别", SourceLangMode: SubtitleSourceLangModeShared, PrepareMode: SubtitlePrepareModeNone, ReasonCode: SubtitleReasonReady}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := s.callAppleAI(ctx, appleAIRequest{Operation: "status"})
	if err != nil {
		status.ReasonMessage = err.Error()
		status.ReasonCode = SubtitleReasonMissingRuntime
		return status
	}
	status.Supported = result.Supported
	status.Available = result.Supported
	status.Languages = result.Languages
	status.TranslationLanguages = result.TranslationLanguages
	if !status.Supported {
		status.ReasonCode = SubtitleReasonUnsupportedPlatform
		status.ReasonMessage = "这台设备不支持苹果系统语音识别"
		return status
	}
	status.ReasonMessage = "系统识别已就绪；自动检测仅覆盖已安装语种"
	if s.findBinary("ffmpeg") == "" {
		status.Available = false
		status.NeedsPrepare = true
		status.PrepareMode = SubtitlePrepareModeManaged
		status.ReasonCode = SubtitleReasonMissingFFmpeg
		status.ReasonMessage = "缺少 FFmpeg，可准备音频提取工具"
	}
	return status
}

// PrepareAppleLanguage prepares system-owned language assets explicitly requested by the user.
func (s *SubtitleService) PrepareAppleLanguage(source, target string, translation bool) error {
	ctx, finish, err := s.beginEnginePreparation()
	if err != nil {
		return err
	}
	defer finish()
	s.invalidateEngineStatusCache()
	defer s.invalidateEngineStatusCache()
	operation := "prepare_speech"
	if translation {
		operation = "prepare_translation"
	}
	if strings.TrimSpace(source) == "" || source == "auto" {
		return appleAIError("apple_source_language_required")
	}
	s.emitProgress("prepare", SubtitleEngineApple, "preparing-runtime", 0, "正在准备苹果系统语言资源…")
	_, err = s.callAppleAI(ctx, appleAIRequest{Operation: operation, Source: source, Target: target})
	if ctx.Err() != nil {
		s.emitProgress("prepare", SubtitleEngineApple, "cancelled", 0, ErrSubtitleEnginePreparationCancelled.Error())
		return ErrSubtitleEnginePreparationCancelled
	}
	s.notifyEnginePreparation("Apple 系统语言", err)
	if err != nil {
		return err
	}
	s.emitPrepareComplete(SubtitleEngineApple)
	return nil
}

// StopApplePreparation cancels and joins interactive detection/resource preparation on application exit.
func (s *SubtitleService) StopApplePreparation() {
	s.prepareMu.Lock()
	s.prepareClosed = true
	cancel, done := s.prepareCancel, s.prepareDone
	s.prepareMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
