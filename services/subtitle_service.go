package services

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type SubtitleService struct {
	ctx      context.Context
	BaseDir  string
	BinDir   string
	ModelDir string
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

func (s *SubtitleService) CheckDependencies() (map[string]bool, error) {
	result := make(map[string]bool)

	// Check FFmpeg
	result["ffmpeg"] = s.findBinary("ffmpeg") != ""

	// Check Whisper (brew installs as whisper-cli, not whisper-cpp)
	result["whisper"] = s.findBinary("whisper-cli") != "" || s.findBinary("whisper-cpp") != "" || s.findBinary("main") != ""

	// Check Model (多语言 medium 模型)
	modelPath := filepath.Join(s.ModelDir, "ggml-medium.bin")
	if _, err := os.Stat(modelPath); err == nil {
		result["model"] = true
	} else {
		result["model"] = false
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
	status, _ := s.CheckDependencies()

	if !status["ffmpeg"] {
		if err := s.downloadFFmpeg(); err != nil {
			return err
		}
	}

	if !status["model"] {
		if err := s.downloadModel(); err != nil {
			return err
		}
	}

	// Whisper: auto-install if not found
	if !status["whisper"] {
		if runtime.GOOS == "darwin" {
			if err := s.installWhisperMac(); err != nil {
				return err
			}
		} else if runtime.GOOS == "windows" {
			if err := s.downloadWhisperWindows(); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("请手动安装 whisper.cpp")
		}
	}

	return nil
}

func (s *SubtitleService) GenerateSubtitle(videoID uint, videoPath string,
	bilingualEnabled bool, bilingualLang string, deeplApiKey string) error {
	s.emitProgress("process", 0, "Initializing...")

	status, err := s.CheckDependencies()
	if err != nil {
		return err
	}
	if !status["ffmpeg"] {
		return fmt.Errorf("缺少 FFmpeg，请先点击下载依赖")
	}
	if !status["whisper"] {
		return fmt.Errorf("缺少 Whisper，请先点击下载依赖")
	}
	if !status["model"] {
		return fmt.Errorf("缺少语音模型，请先点击下载依赖")
	}

	// Extract Audio
	s.emitProgress("process", 10, "Extracting audio...")
	tempWav := filepath.Join(s.BaseDir, fmt.Sprintf("temp_%d.wav", videoID))
	defer os.Remove(tempWav)

	if err := s.extractAudio(videoPath, tempWav); err != nil {
		return err
	}

	// Transcribe (原文识别)
	s.emitProgress("process", 20, "Transcribing (this may take a while)...")
	outputPrefix := strings.TrimSuffix(videoPath, filepath.Ext(videoPath))

	detectedLang, err := s.transcribeCLIWithLang(tempWav, outputPrefix)
	if err != nil {
		return err
	}

	srtPath := outputPrefix + ".srt"

	// 后处理：检测幻觉输出
	s.emitProgress("process", 50, "Validating output...")
	if err := s.validateSRT(srtPath); err != nil {
		return err
	}

	// 双语字幕处理
	if bilingualEnabled && deeplApiKey != "" && bilingualLang != "" {
		log.Printf("[Subtitle] bilingual: detected=%s target=%s", detectedLang, bilingualLang)

		// 如果检测到的语言已经是目标语言，跳过翻译
		if s.isSameLanguage(detectedLang, bilingualLang) {
			log.Printf("[Subtitle] detected language matches target, skipping translation")
		} else {
			// 直接用 DeepL 翻译原文 SRT（DeepL 支持任意语言对，自动检测源语言）
			s.emitProgress("process", 60, "Translating via DeepL...")

			translatedSrtPath := outputPrefix + "_translated_temp.srt"
			defer os.Remove(translatedSrtPath)

			// sourceLang 传空，让 DeepL 自动检测源语言
			if err := s.translateSRT(srtPath, translatedSrtPath, "", bilingualLang, deeplApiKey); err != nil {
				log.Printf("[Subtitle] DeepL translate failed: %v, keeping original SRT", err)
				goto done
			}

			// 合并双语 SRT（原文上行、翻译下行）
			s.emitProgress("process", 85, "Merging bilingual subtitles...")
			if err := s.mergeBilingualSRT(srtPath, translatedSrtPath, srtPath); err != nil {
				log.Printf("[Subtitle] merge failed: %v", err)
			}
		}
	}

done:
	s.emitProgress("process", 100, "Completed")

	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-success", map[string]interface{}{
			"videoID": videoID,
			"path":    srtPath,
		})
	}
	return nil
}

func (s *SubtitleService) extractAudio(videoPath, outputPath string) error {
	ffmpegBin := s.findBinary("ffmpeg")
	if ffmpegBin == "" {
		return fmt.Errorf("未找到 FFmpeg，请重新安装依赖")
	}

	log.Printf("[Subtitle] extractAudio: ffmpeg=%s input=%s output=%s\n", ffmpegBin, videoPath, outputPath)
	cmd := exec.Command(ffmpegBin, "-i", videoPath, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", outputPath, "-y")
	output, err := cmd.CombinedOutput()
	if err != nil {
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
func (s *SubtitleService) transcribeCLIWithLang(wavPath, outputPrefix string) (string, error) {
	whisperBin := s.findWhisperBin()
	if whisperBin == "" {
		return "", fmt.Errorf("未找到 Whisper，请重新安装依赖")
	}

	modelPath := filepath.Join(s.ModelDir, "ggml-medium.bin")

	log.Printf("[Subtitle] transcribeCLIWithLang: whisper=%s model=%s input=%s output=%s\n", whisperBin, modelPath, wavPath, outputPrefix)

	cmd := exec.Command(whisperBin,
		"-m", modelPath,
		"-f", wavPath,
		"-osrt",
		"-of", outputPrefix,
		"-l", "auto",
		"--no-fallback",
		"-et", "2.4",
		"-lpt", "-1.0",
		"-bo", "5",
		"-bs", "5",
	)

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// 从输出中提取检测到的语言
	detectedLang := "en" // 默认英文
	langRe := regexp.MustCompile(`auto-detected language:\s*(\w+)`)
	if matches := langRe.FindStringSubmatch(outputStr); len(matches) > 1 {
		detectedLang = strings.ToLower(matches[1])
		log.Printf("[Subtitle] detected language: %s", detectedLang)
	}

	if err != nil {
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

	if repeatRatio > 0.7 {
		os.Remove(srtPath)
		return fmt.Errorf("检测到异常输出（模型幻觉），字幕内容大量重复。请尝试使用更短的视频片段或检查音频质量")
	}

	return nil
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
	blocks := regexp.MustCompile(`\r?\n\r?\n`).Split(strings.TrimSpace(string(data)), -1)

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

	resp, err := http.DefaultClient.Do(req)
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

	s.emitProgress(pkg, 0, fmt.Sprintf("正在通过 Homebrew 安装 %s...", displayName))

	cmd := exec.Command(brewPath, "install", pkg)
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("brew install %s 失败: %s\n%s", pkg, err, string(output))
	}

	s.emitProgress(pkg, 100, fmt.Sprintf("%s 安装完成", displayName))
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
	s.emitProgress("model", 0, "Downloading Model (~1.5GB)...")
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
				s.emitProgress(component, p, fmt.Sprintf("Downloading %d%%", p))
			}
		},
	}
	_, err = io.Copy(out, tracker)
	return err
}

func (s *SubtitleService) emitProgress(comp string, pct int, msg string) {
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "download-progress", map[string]interface{}{
			"component": comp, "percent": pct, "msg": msg,
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
