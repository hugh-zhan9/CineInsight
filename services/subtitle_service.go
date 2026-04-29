package services

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"video-master/services/subtitleparser"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// 预编译正则表达式（避免每次调用重复编译）
var (
	srtBlockSplitter     = regexp.MustCompile(`\r?\n\r?\n`)
	langDetectRe         = regexp.MustCompile(`auto-detected language:\s*(\w+)`)
	langDetectReFallback = regexp.MustCompile(`language:\s*(\w+)`)
)

// DeepL HTTP 客户端（带超时控制）
var deeplHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

type SubtitleService struct {
	ctx        context.Context
	cancelFunc context.CancelFunc // 取消当前生成任务
	mu         sync.Mutex
	BaseDir    string
	BinDir     string
	ModelDir   string
}

func NewSubtitleService(baseDir string) *SubtitleService {
	return &SubtitleService{
		BaseDir:  baseDir,
		BinDir:   filepath.Join(baseDir, "bin"),
		ModelDir: filepath.Join(baseDir, "models"),
	}
}

func (s *SubtitleService) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// CancelGeneration 取消正在进行的字幕生成任务
func (s *SubtitleService) CancelGeneration() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelFunc != nil {
		s.cancelFunc()
		log.Printf("[Subtitle] generation cancelled by user")
	}
}

func (s *SubtitleService) GetEngineStatuses() ([]SubtitleEngineStatus, error) {
	statuses := []SubtitleEngineStatus{
		s.getWhisperXStatus(),
		s.getQwenStatus(),
	}
	return statuses, nil
}

func (s *SubtitleService) getWhisperXStatus() SubtitleEngineStatus {
	ffmpegReady := s.findBinary("ffmpeg") != ""
	installed := s.isWhisperXInstalled()
	status := SubtitleEngineStatus{
		Engine:         SubtitleEngineWhisperX,
		DisplayName:    "WhisperX",
		Supported:      true,
		Available:      ffmpegReady && installed,
		NeedsPrepare:   false,
		PrepareMode:    SubtitlePrepareModeNone,
		ReasonCode:     SubtitleReasonReady,
		SourceLangMode: SubtitleSourceLangModeShared,
		ReasonMessage:  "WhisperX 已就绪",
		PrepareHint:    "",
	}

	if runtime.GOOS == "darwin" {
		status.PrepareMode = SubtitlePrepareModeManaged
		if !ffmpegReady {
			status.Available = false
			status.NeedsPrepare = true
			status.ReasonCode = SubtitleReasonMissingFFmpeg
			status.ReasonMessage = "缺少 FFmpeg，可通过应用自动准备。"
			status.PrepareHint = "准备 WhisperX 时会同时检查并安装 FFmpeg。"
			return status
		}
		if !installed {
			status.Available = false
			status.NeedsPrepare = true
			status.ReasonCode = SubtitleReasonMissingRuntime
			status.ReasonMessage = "缺少 WhisperX 运行时，可通过应用自动准备。"
			status.PrepareHint = "应用会创建私有 WhisperX sidecar 与模型缓存。"
			return status
		}
		return status
	}

	if !ffmpegReady {
		status.Available = false
		status.PrepareMode = SubtitlePrepareModeManualPrereq
		status.ReasonCode = SubtitleReasonManualPrereq
		status.ReasonMessage = "当前平台需要先手动安装 FFmpeg。"
		status.PrepareHint = "安装 FFmpeg 后，应用仍可继续准备 WhisperX 私有运行时。"
		return status
	}
	if !installed {
		status.Available = false
		status.NeedsPrepare = true
		status.PrepareMode = SubtitlePrepareModeManaged
		status.ReasonCode = SubtitleReasonMissingRuntime
		status.ReasonMessage = "共享前置条件已满足，可继续准备 WhisperX 运行时。"
		status.PrepareHint = "当前平台不会自动安装系统依赖，只会准备应用私有运行时。"
		return status
	}
	return status
}

func (s *SubtitleService) getQwenStatus() SubtitleEngineStatus {
	status := SubtitleEngineStatus{
		Engine:         SubtitleEngineQwen,
		DisplayName:    "Qwen3-ASR-1.7B",
		Supported:      false,
		Available:      false,
		NeedsPrepare:   false,
		PrepareMode:    SubtitlePrepareModeUnsupported,
		ReasonCode:     SubtitleReasonUnsupportedPlatform,
		SourceLangMode: SubtitleSourceLangModeIgnored,
		ReasonMessage:  "Qwen v1 当前仅在 macOS 上提供。",
		PrepareHint:    "",
	}

	if runtime.GOOS != "darwin" {
		return status
	}
	if runtime.GOARCH != "arm64" {
		status.ReasonMessage = "Qwen v1 当前仅默认启用 macOS arm64；amd64 需后续探测通过后再启用。"
		return status
	}

	ffmpegReady := s.findBinary("ffmpeg") != ""
	installed := s.isQwenInstalled()
	status.Supported = true
	status.PrepareMode = SubtitlePrepareModeManaged
	status.ReasonCode = SubtitleReasonReady
	status.ReasonMessage = "Qwen 已就绪"
	status.PrepareHint = ""
	status.Available = ffmpegReady && installed
	if !ffmpegReady {
		status.Available = false
		status.NeedsPrepare = true
		status.ReasonCode = SubtitleReasonMissingFFmpeg
		status.ReasonMessage = "缺少 FFmpeg，可通过应用自动准备。"
		status.PrepareHint = "准备 Qwen 时会同时检查并安装 FFmpeg。"
		return status
	}
	if !installed {
		status.Available = false
		status.NeedsPrepare = true
		status.ReasonCode = SubtitleReasonMissingRuntime
		status.ReasonMessage = "缺少 Qwen 运行时，可通过应用自动准备。"
		status.PrepareHint = "应用会创建独立的 Qwen ASR sidecar。"
		return status
	}
	return status
}

func (s *SubtitleService) PrepareEngine(engine SubtitleEngine) error {
	statusMap := map[SubtitleEngine]SubtitleEngineStatus{}
	statuses, err := s.GetEngineStatuses()
	if err != nil {
		return err
	}
	for _, status := range statuses {
		statusMap[status.Engine] = status
	}
	status, ok := statusMap[engine]
	if !ok {
		return fmt.Errorf("不支持的字幕引擎: %s", engine)
	}
	if !status.Supported {
		return fmt.Errorf(status.ReasonMessage)
	}
	if !status.NeedsPrepare {
		if s.ctx != nil {
			wailsRuntime.EventsEmit(s.ctx, "subtitle-prepare-complete", map[string]interface{}{
				"engine": string(engine),
			})
		}
		return nil
	}

	if !status.Available && status.ReasonCode == SubtitleReasonMissingFFmpeg && runtime.GOOS == "darwin" {
		if err := s.downloadFFmpeg(); err != nil {
			return err
		}
	}

	s.emitProgress("prepare", engine, "checking", 0, "准备运行时...")

	switch engine {
	case SubtitleEngineWhisperX:
		if runtime.GOOS != "darwin" && s.findBinary("ffmpeg") == "" {
			return fmt.Errorf("当前平台需先手动安装 FFmpeg，再准备 WhisperX 运行时")
		}
		if err := s.installWhisperXRuntime(); err != nil {
			return err
		}
	case SubtitleEngineQwen:
		if err := s.installQwenRuntime(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("不支持的字幕引擎: %s", engine)
	}

	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-prepare-complete", map[string]interface{}{
			"engine": string(engine),
		})
	}
	return nil
}

func (s *SubtitleService) CheckDependencies() (map[string]bool, error) {
	statuses, err := s.GetEngineStatuses()
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool)
	result["ffmpeg"] = s.findBinary("ffmpeg") != ""
	for _, status := range statuses {
		if status.Engine == SubtitleEngineWhisperX {
			result["whisper"] = status.Available
			result["model"] = status.Available
		}
	}
	return result, nil
}

// findBinary searches for a binary in: local bin dir, Homebrew paths, system PATH
func (s *SubtitleService) findBinary(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	// 1. Local bin dir
	localPath := filepath.Join(s.BinDir, name)
	if _, err := os.Stat(localPath); err == nil {
		return localPath
	}

	// 2. Common Homebrew paths (macOS .app bundles have minimal PATH)
	if runtime.GOOS == "darwin" {
		brewPaths := []string{
			"/opt/homebrew/bin/" + name, // Apple Silicon
			"/usr/local/bin/" + name,    // Intel Mac
		}
		for _, p := range brewPaths {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}

	// 3. System PATH
	if p, err := exec.LookPath(name); err == nil {
		return p
	}

	return ""
}

func (s *SubtitleService) DownloadDependencies() error {
	return s.PrepareEngine(SubtitleEngineWhisperX)
}

func (s *SubtitleService) GenerateSubtitle(req SubtitleGenerateRequest, videoPath string,
	bilingualEnabled bool, bilingualLang string, deeplApiKey string, forceGenerate bool) (*SubtitleGenerateResult, error) {

	// 创建可取消的子 context
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.cancelFunc = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.cancelFunc = nil
		s.mu.Unlock()
	}()

	s.emitProgress("generate", req.Engine, "checking", 0, "初始化任务...")

	statuses, err := s.GetEngineStatuses()
	if err != nil {
		return nil, err
	}
	var engineStatus *SubtitleEngineStatus
	for idx := range statuses {
		if statuses[idx].Engine == req.Engine {
			engineStatus = &statuses[idx]
			break
		}
	}
	if engineStatus == nil {
		return nil, fmt.Errorf("不支持的字幕引擎: %s", req.Engine)
	}
	if !engineStatus.Supported {
		return nil, fmt.Errorf(engineStatus.ReasonMessage)
	}
	if !engineStatus.Available {
		return nil, fmt.Errorf(engineStatus.ReasonMessage)
	}

	// Extract Audio
	s.emitProgress("generate", req.Engine, "extracting-audio", 10, "提取音频...")
	tempWav := filepath.Join(s.BaseDir, fmt.Sprintf("temp_%d.wav", req.VideoID))
	defer os.Remove(tempWav)

	if err := s.extractAudio(ctx, videoPath, tempWav); err != nil {
		if ctx.Err() != nil {
			s.emitCancelled(req.VideoID, req.Engine, "字幕生成已取消")
			return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: req.VideoID, Message: "字幕生成已取消"}, nil
		}
		return nil, err
	}

	// Transcribe (原文识别)
	s.emitProgress("generate", req.Engine, "transcribing", 20, fmt.Sprintf("使用 %s 转写音频...", engineStatus.DisplayName))
	outputPrefix := strings.TrimSuffix(videoPath, filepath.Ext(videoPath))

	if req.SourceLang == "" {
		req.SourceLang = "auto"
	}

	var detectedLang string
	var segments []subtitleparser.Segment
	switch req.Engine {
	case SubtitleEngineWhisperX:
		detectedLang, segments, err = s.transcribeWhisperXWithLang(ctx, tempWav, req.SourceLang)
	case SubtitleEngineQwen:
		detectedLang, segments, err = s.transcribeQwenWithLang(ctx, tempWav, req.SourceLang)
	default:
		return nil, fmt.Errorf("不支持的字幕引擎: %s", req.Engine)
	}
	if err != nil {
		if ctx.Err() != nil {
			s.emitCancelled(req.VideoID, req.Engine, "字幕生成已取消")
			return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: req.VideoID, Message: "字幕生成已取消"}, nil
		}
		return nil, err
	}

	s.emitProgress("generate", req.Engine, "normalizing", 35, "整理转写结果...")
	srtPath := outputPrefix + ".srt"
	if err := writeSRT(srtPath, segments); err != nil {
		return nil, fmt.Errorf("写入字幕失败: %w", err)
	}

	// 后处理：检测幻觉输出（forceGenerate 时跳过）
	if !forceGenerate {
		s.emitProgress("generate", req.Engine, "validating", 50, "校验字幕输出...")
		if err := s.validateSRT(srtPath); err != nil {
			var validationErr *SubtitleValidationError
			if ok := errors.As(err, &validationErr); ok {
				return &SubtitleGenerateResult{
					Status:         SubtitleResultStatusValidationFailed,
					VideoID:        req.VideoID,
					Message:        validationErr.Message,
					ValidationCode: validationErr.Code,
					ForceEligible:  validationErr.ForceEligible,
					Engine:         req.Engine,
					SourceLang:     req.SourceLang,
				}, nil
			}
			return nil, err
		}
	}

	// 双语字幕处理
	if bilingualEnabled && deeplApiKey != "" && bilingualLang != "" {
		log.Printf("[Subtitle] bilingual: detected=%s target=%s", detectedLang, bilingualLang)

		// 如果检测到的语言已经是目标语言，跳过翻译
		if s.isSameLanguage(detectedLang, bilingualLang) {
			log.Printf("[Subtitle] detected language matches target, skipping translation")
		} else {
			// 直接用 DeepL 翻译原文 SRT（DeepL 支持任意语言对，自动检测源语言）
			s.emitProgress("generate", req.Engine, "translating", 60, "通过 DeepL 翻译字幕...")

			translatedSrtPath := outputPrefix + "_translated_temp.srt"
			defer os.Remove(translatedSrtPath)

			// sourceLang 传空，让 DeepL 自动检测源语言
			if err := s.translateSRT(srtPath, translatedSrtPath, "", bilingualLang, deeplApiKey); err != nil {
				log.Printf("[Subtitle] DeepL translate failed: %v, keeping original SRT", err)
				goto done
			}

			// 合并双语 SRT（原文上行、翻译下行）
			s.emitProgress("generate", req.Engine, "merging", 85, "合并双语字幕...")
			if err := s.mergeBilingualSRT(srtPath, translatedSrtPath, srtPath); err != nil {
				log.Printf("[Subtitle] merge failed: %v", err)
			}
		}
	}

done:
	if err := indexSubtitleFileForVideoID(req.VideoID, srtPath); err != nil {
		log.Printf("[Subtitle] index subtitle failed videoID=%d path=%s err=%v", req.VideoID, srtPath, err)
	}

	s.emitProgress("generate", req.Engine, "finalizing", 100, "完成收尾")

	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-success", map[string]interface{}{
			"videoID": req.VideoID,
			"engine":  string(req.Engine),
			"path":    srtPath,
		})
	}
	return &SubtitleGenerateResult{Status: SubtitleResultStatusSuccess, VideoID: req.VideoID, Path: srtPath}, nil
}

func (s *SubtitleService) extractAudio(ctx context.Context, videoPath, outputPath string) error {
	ffmpegBin := s.findBinary("ffmpeg")
	if ffmpegBin == "" {
		return fmt.Errorf("未找到 FFmpeg，请重新安装依赖")
	}

	log.Printf("[Subtitle] extractAudio: ffmpeg=%s input=%s output=%s\n", ffmpegBin, videoPath, outputPath)
	cmd := exec.CommandContext(ctx, ffmpegBin, "-i", videoPath, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", outputPath, "-y")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("字幕生成已取消")
		}
		detail := string(output)
		log.Printf("[Subtitle] ffmpeg error: %s\n%s\n", err, detail)
		// User-friendly error messages
		if strings.Contains(detail, "moov atom not found") || strings.Contains(detail, "Invalid data found") {
			return fmt.Errorf("视频文件异常或已损坏，无法提取音频")
		}
		if strings.Contains(detail, "No such file") || strings.Contains(detail, "does not exist") {
			return fmt.Errorf("视频文件不存在")
		}
		if strings.Contains(detail, "Permission denied") {
			return fmt.Errorf("没有权限访问视频文件")
		}
		return fmt.Errorf("音频提取失败，请检查视频文件是否有效")
	}
	return nil
}

// findWhisperBin 查找 whisper 可执行文件
func (s *SubtitleService) findWhisperBin() string {
	whisperBin := s.findBinary("whisper-cli")
	if whisperBin == "" {
		whisperBin = s.findBinary("whisper-cpp")
	}
	if whisperBin == "" {
		whisperBin = s.findBinary("main")
	}
	return whisperBin
}

// transcribeCLIWithLang 转录音频并返回检测到的语言代码
func (s *SubtitleService) transcribeCLIWithLang(ctx context.Context, wavPath, outputPrefix, sourceLang string) (string, error) {
	whisperBin := s.findWhisperBin()
	if whisperBin == "" {
		return "", fmt.Errorf("未找到 Whisper，请重新安装依赖")
	}

	modelPath := filepath.Join(s.ModelDir, "ggml-medium.bin")

	log.Printf("[Subtitle] transcribeCLIWithLang: whisper=%s model=%s input=%s output=%s lang=%s\n", whisperBin, modelPath, wavPath, outputPrefix, sourceLang)

	cmd := exec.CommandContext(ctx, whisperBin,
		"-m", modelPath,
		"-f", wavPath,
		"-osrt",
		"-of", outputPrefix,
		"-l", sourceLang,
		"--no-fallback",
		"-et", "2.4",
		"-lpt", "-1.0",
		"-bo", "5",
		"-bs", "5",
	)

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// 从输出中提取检测到的语言（支持多种 whisper 输出格式）
	detectedLang := "en" // 默认英文
	if matches := langDetectRe.FindStringSubmatch(outputStr); len(matches) > 1 {
		detectedLang = strings.ToLower(matches[1])
		log.Printf("[Subtitle] detected language: %s", detectedLang)
	} else if matches := langDetectReFallback.FindStringSubmatch(outputStr); len(matches) > 1 {
		detectedLang = strings.ToLower(matches[1])
		log.Printf("[Subtitle] detected language (fallback): %s", detectedLang)
	} else {
		log.Printf("[Subtitle] WARNING: could not detect language from whisper output, defaulting to 'en'")
	}

	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("字幕生成已取消")
		}
		log.Printf("[Subtitle] whisper error: %s\n%s\n", err, outputStr)
		if strings.Contains(outputStr, "failed to open") || strings.Contains(outputStr, "no such file") {
			return "", fmt.Errorf("模型文件缺失，请重新安装依赖")
		}
		return "", fmt.Errorf("语音识别失败，请确保视频包含有效音频")
	}
	return detectedLang, nil
}

// validateSRT 检测 SRT 文件是否存在幻觉输出（大量重复文本）
func (s *SubtitleService) validateSRT(srtPath string) error {
	f, err := os.Open(srtPath)
	if err != nil {
		return fmt.Errorf("字幕文件生成失败")
	}
	defer f.Close()

	lineCounts := make(map[string]int)
	totalLines := 0
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.Contains(line, "-->") {
			continue
		}
		isNum := true
		for _, c := range line {
			if c < '0' || c > '9' {
				isNum = false
				break
			}
		}
		if isNum {
			continue
		}
		totalLines++
		lineCounts[line]++
	}

	if totalLines == 0 {
		log.Printf("[Subtitle] validateSRT: 字幕文件无有效文本行")
		return fmt.Errorf("语音识别未产生有效字幕，视频可能没有清晰的语音内容")
	}

	maxCount := 0
	maxLine := ""
	for line, count := range lineCounts {
		if count > maxCount {
			maxCount = count
			maxLine = line
		}
	}

	repeatRatio := float64(maxCount) / float64(totalLines)
	log.Printf("[Subtitle] validateSRT: totalLines=%d maxCount=%d ratio=%.2f maxLine=%q", totalLines, maxCount, repeatRatio, maxLine)

	if repeatRatio > 0.85 {
		os.Remove(srtPath)
		return &SubtitleValidationError{
			Code:          SubtitleValidationCodeHallucinationDetected,
			Message:       fmt.Sprintf("检测到异常输出（疑似模型幻觉），字幕内容重复率 %.0f%%。可选择强制生成保留结果", repeatRatio*100),
			ForceEligible: true,
		}
	}

	segments, err := subtitleparser.ParseFile(srtPath)
	if err == nil && hasTokenizedTimingFailure(segments) {
		os.Remove(srtPath)
		return &SubtitleValidationError{
			Code:          SubtitleValidationCodeHallucinationDetected,
			Message:       "检测到异常逐字字幕（大量单字或零时长片段），可选择强制生成保留结果",
			ForceEligible: true,
		}
	}
	if err != nil {
		log.Printf("[Subtitle] validateSRT: parse structured segments failed: %v", err)
	}

	return nil
}

func hasTokenizedTimingFailure(segments []subtitleparser.Segment) bool {
	if len(segments) < 30 {
		return false
	}

	zeroDurationCount := 0
	shortTextCount := 0
	startTimes := make(map[int64]struct{}, len(segments))
	for _, segment := range segments {
		if segment.EndTimeMs <= segment.StartTimeMs {
			zeroDurationCount++
		}
		text := strings.TrimSpace(strings.ReplaceAll(segment.Text, "\n", ""))
		if utf8.RuneCountInString(text) <= 2 {
			shortTextCount++
		}
		startTimes[segment.StartTimeMs] = struct{}{}
	}

	total := float64(len(segments))
	zeroRatio := float64(zeroDurationCount) / total
	shortRatio := float64(shortTextCount) / total
	uniqueStartRatio := float64(len(startTimes)) / total
	log.Printf("[Subtitle] validateSRT timing: segments=%d zeroRatio=%.2f shortRatio=%.2f uniqueStartRatio=%.2f", len(segments), zeroRatio, shortRatio, uniqueStartRatio)

	return shortRatio > 0.85 && (zeroRatio > 0.50 || uniqueStartRatio < 0.20)
}

// isSameLanguage 判断 whisper 检测到的语言与用户目标语言是否相同
func (s *SubtitleService) isSameLanguage(detected, target string) bool {
	// whisper 输出格式如 "chinese", "english", "japanese"
	// 用户设定格式如 "zh", "en", "ja"
	langMap := map[string]string{
		"chinese":    "zh",
		"english":    "en",
		"japanese":   "ja",
		"korean":     "ko",
		"french":     "fr",
		"german":     "de",
		"spanish":    "es",
		"portuguese": "pt",
		"russian":    "ru",
		"italian":    "it",
	}

	detectedCode := strings.ToLower(detected)
	targetCode := strings.ToLower(target)

	// 先尝试映射 whisper 输出的全名
	if code, ok := langMap[detectedCode]; ok {
		detectedCode = code
	}

	return detectedCode == targetCode
}

// SRTEntry 表示一条 SRT 字幕
type SRTEntry struct {
	Index string
	Time  string
	Text  string
}

// parseSRTEntries 解析 SRT 文件为条目列表
func parseSRTEntries(srtPath string) ([]SRTEntry, error) {
	data, err := os.ReadFile(srtPath)
	if err != nil {
		return nil, err
	}

	var entries []SRTEntry
	blocks := srtBlockSplitter.Split(strings.TrimSpace(string(data)), -1)

	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 3 {
			continue
		}
		entry := SRTEntry{
			Index: strings.TrimSpace(lines[0]),
			Time:  strings.TrimSpace(lines[1]),
			Text:  strings.TrimSpace(strings.Join(lines[2:], "\n")),
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// translateDeepL 调用 DeepL API 翻译文本
func (s *SubtitleService) translateDeepL(texts []string, sourceLang, targetLang, apiKey string) ([]string, error) {
	// DeepL 目标语言代码需要大写
	targetUpper := strings.ToUpper(targetLang)
	// 中文需要特殊处理：DeepL 使用 ZH-HANS
	if targetUpper == "ZH" {
		targetUpper = "ZH-HANS"
	}

	// 构建请求体
	type DeepLRequest struct {
		Text       []string `json:"text"`
		TargetLang string   `json:"target_lang"`
		SourceLang string   `json:"source_lang,omitempty"`
	}

	reqBody := DeepLRequest{
		Text:       texts,
		TargetLang: targetUpper,
	}
	if sourceLang != "" {
		reqBody.SourceLang = strings.ToUpper(sourceLang)
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("构建翻译请求失败: %v", err)
	}

	// 判断是免费版还是付费版 API
	apiURL := "https://api-free.deepl.com/v2/translate"
	if !strings.HasSuffix(apiKey, ":fx") {
		apiURL = "https://api.deepl.com/v2/translate"
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "DeepL-Auth-Key "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := deeplHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("翻译请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == 403 {
			return nil, fmt.Errorf("DeepL API Key 无效或已过期")
		}
		if resp.StatusCode == 456 {
			return nil, fmt.Errorf("DeepL 翻译额度已用完")
		}
		return nil, fmt.Errorf("DeepL API 返回 %d: %s", resp.StatusCode, string(respBody))
	}

	type DeepLTranslation struct {
		Text string `json:"text"`
	}
	type DeepLResponse struct {
		Translations []DeepLTranslation `json:"translations"`
	}

	var deeplResp DeepLResponse
	if err := json.NewDecoder(resp.Body).Decode(&deeplResp); err != nil {
		return nil, fmt.Errorf("解析翻译响应失败: %v", err)
	}

	results := make([]string, len(deeplResp.Translations))
	for i, t := range deeplResp.Translations {
		results[i] = t.Text
	}
	return results, nil
}

// translateSRT 翻译 SRT 文件中的所有文本行
func (s *SubtitleService) translateSRT(inputPath, outputPath, sourceLang, targetLang, apiKey string) error {
	entries, err := parseSRTEntries(inputPath)
	if err != nil {
		return fmt.Errorf("读取字幕文件失败: %v", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("字幕文件为空")
	}

	// 收集文本行（DeepL 一次最多翻译 50 条）
	batchSize := 50
	var translatedEntries []SRTEntry

	for i := 0; i < len(entries); i += batchSize {
		end := i + batchSize
		if end > len(entries) {
			end = len(entries)
		}
		batch := entries[i:end]

		texts := make([]string, len(batch))
		for j, e := range batch {
			texts[j] = e.Text
		}

		translated, err := s.translateDeepL(texts, sourceLang, targetLang, apiKey)
		if err != nil {
			return err
		}

		for j, e := range batch {
			translatedText := e.Text
			if j < len(translated) {
				translatedText = translated[j]
			}
			translatedEntries = append(translatedEntries, SRTEntry{
				Index: e.Index,
				Time:  e.Time,
				Text:  translatedText,
			})
		}
	}

	// 写入翻译后的 SRT
	var buf strings.Builder
	for _, e := range translatedEntries {
		buf.WriteString(e.Index + "\n")
		buf.WriteString(e.Time + "\n")
		buf.WriteString(e.Text + "\n\n")
	}

	return os.WriteFile(outputPath, []byte(buf.String()), 0644)
}

// mergeBilingualSRT 合并两个 SRT 文件为双语 SRT（每条字幕上行原文、下行翻译）
func (s *SubtitleService) mergeBilingualSRT(originalPath, translatedPath, outputPath string) error {
	origEntries, err := parseSRTEntries(originalPath)
	if err != nil {
		return fmt.Errorf("读取原文字幕失败: %v", err)
	}
	transEntries, err := parseSRTEntries(translatedPath)
	if err != nil {
		return fmt.Errorf("读取翻译字幕失败: %v", err)
	}

	var buf strings.Builder
	maxLen := len(origEntries)
	if len(transEntries) > maxLen {
		maxLen = len(transEntries)
	}

	for i := 0; i < maxLen; i++ {
		idx := fmt.Sprintf("%d", i+1)
		var timeLine, origText, transText string

		if i < len(origEntries) {
			timeLine = origEntries[i].Time
			origText = origEntries[i].Text
		}
		if i < len(transEntries) {
			if timeLine == "" {
				timeLine = transEntries[i].Time
			}
			transText = transEntries[i].Text
		}

		buf.WriteString(idx + "\n")
		buf.WriteString(timeLine + "\n")
		// 原文在上，翻译在下
		if origText != "" && transText != "" {
			buf.WriteString(origText + "\n" + transText + "\n\n")
		} else if origText != "" {
			buf.WriteString(origText + "\n\n")
		} else {
			buf.WriteString(transText + "\n\n")
		}
	}

	log.Printf("[Subtitle] mergeBilingualSRT: merged %d entries", maxLen)
	return os.WriteFile(outputPath, []byte(buf.String()), 0644)
}

// Download helpers
func (s *SubtitleService) downloadFFmpeg() error {
	if runtime.GOOS == "darwin" {
		return s.installBrewPackage("ffmpeg", "FFmpeg")
	}
	return fmt.Errorf("auto-download ffmpeg only supported on macOS for now")
}

func (s *SubtitleService) installWhisperMac() error {
	return s.installBrewPackage("whisper-cpp", "Whisper")
}

// installBrewPackage installs a package via Homebrew with progress feedback
func (s *SubtitleService) installBrewPackage(pkg, displayName string) error {
	brewPath, err := exec.LookPath("brew")
	if err != nil {
		// Also check common brew paths
		for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
			if _, err := os.Stat(p); err == nil {
				brewPath = p
				break
			}
		}
		if brewPath == "" {
			return fmt.Errorf("未找到 Homebrew，请先安装 Homebrew (https://brew.sh)，然后重试")
		}
	}

	s.emitProgress("prepare", SubtitleEngineWhisperX, "preparing-runtime", 0, fmt.Sprintf("正在通过 Homebrew 安装 %s...", displayName))

	cmd := exec.Command(brewPath, "install", pkg)
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("brew install %s 失败: %s\n%s", pkg, err, string(output))
	}

	s.emitProgress("prepare", SubtitleEngineWhisperX, "preparing-runtime", 100, fmt.Sprintf("%s 安装完成", displayName))
	return nil
}

func (s *SubtitleService) downloadWhisperWindows() error {
	// TODO: download windows binary from releases
	return fmt.Errorf("windows auto-download pending")
}

func (s *SubtitleService) downloadModel() error {
	if err := os.MkdirAll(s.ModelDir, 0755); err != nil {
		return err
	}
	// 多语言 medium 模型（~1.5GB），支持自动语言检测
	url := "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin"
	dest := filepath.Join(s.ModelDir, "ggml-medium.bin")
	s.emitProgress("prepare", SubtitleEngineWhisperX, "downloading-model", 0, "Downloading Model (~1.5GB)...")
	return s.downloadFile(url, dest, "model")
}

func (s *SubtitleService) downloadFile(url, dest, component string) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	size := resp.ContentLength
	tracker := &ProgressTracker{
		Reader: resp.Body, Total: size,
		OnProgress: func(c int64) {
			if size > 0 {
				p := int(float64(c) / float64(size) * 100)
				s.emitProgress("prepare", SubtitleEngineWhisperX, "downloading-model", p, fmt.Sprintf("Downloading %d%%", p))
			}
		},
	}
	_, err = io.Copy(out, tracker)
	return err
}

func (s *SubtitleService) emitProgress(action string, engine SubtitleEngine, phase string, pct int, msg string) {
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-progress", map[string]interface{}{
			"action":      action,
			"engine":      string(engine),
			"phase":       phase,
			"percent":     pct,
			"message":     msg,
			"cancellable": action == "generate",
			"jobScope":    "single_active_v1",
		})
	}
}

func (s *SubtitleService) emitCancelled(videoID uint, engine SubtitleEngine, msg string) {
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-cancelled", map[string]interface{}{
			"videoID": videoID,
			"engine":  string(engine),
			"message": msg,
		})
	}
}

type ProgressTracker struct {
	io.Reader
	Total, Current int64
	OnProgress     func(int64)
}

func (pt *ProgressTracker) Read(p []byte) (n int, err error) {
	n, err = pt.Reader.Read(p)
	pt.Current += int64(n)
	if pt.OnProgress != nil {
		pt.OnProgress(pt.Current)
	}
	return
}

func unzipFile(src, destDir, targetPrefix string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if strings.HasPrefix(name, targetPrefix) {
			fpath := filepath.Join(destDir, name)
			if f.FileInfo().IsDir() {
				continue
			}
			out, err := os.Create(fpath)
			if err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				out.Close()
				return err
			}
			io.Copy(out, rc)
			out.Close()
			rc.Close()
			return nil
		}
	}
	return nil
}
