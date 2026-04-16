package services

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"video-master/services/subtitleparser"
)

const (
	whisperXRuntimeDirName = "whisperx_sidecar"
	whisperXVenvDirName    = "venv"
	whisperXWorkerFileName = "whisperx_worker.py"
	whisperXVersion        = "3.8.2"
	whisperXModelName      = "medium"
	managedPythonMacARM64  = "https://github.com/astral-sh/python-build-standalone/releases/download/20260303/cpython-3.10.20%2B20260303-aarch64-apple-darwin-install_only_stripped.tar.gz"
	managedPythonMacAMD64  = "https://github.com/astral-sh/python-build-standalone/releases/download/20260303/cpython-3.10.20%2B20260303-x86_64-apple-darwin-install_only_stripped.tar.gz"
)

//go:embed whisperx_worker.py
var whisperXWorkerScript string

type whisperXPayload struct {
	Language    string                   `json:"language"`
	DurationSec float64                  `json:"duration_sec"`
	Segments    []whisperXPayloadSegment `json:"segments"`
}

type whisperXPayloadSegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

func (s *SubtitleService) whisperXRuntimeDir() string {
	return filepath.Join(s.BaseDir, whisperXRuntimeDirName)
}

func (s *SubtitleService) whisperXWorkerPath() string {
	return filepath.Join(s.whisperXRuntimeDir(), whisperXWorkerFileName)
}

func (s *SubtitleService) whisperXVenvDir() string {
	return filepath.Join(s.whisperXRuntimeDir(), whisperXVenvDirName)
}

func (s *SubtitleService) whisperXModelCacheDir() string {
	return filepath.Join(s.whisperXRuntimeDir(), "model_cache")
}

func (s *SubtitleService) whisperXAsrModelDir() string {
	return filepath.Join(s.whisperXModelCacheDir(), "asr")
}

func (s *SubtitleService) whisperXAlignModelDir() string {
	return filepath.Join(s.whisperXModelCacheDir(), "align")
}

func (s *SubtitleService) whisperXHFHomeDir() string {
	return filepath.Join(s.whisperXRuntimeDir(), "hf")
}

func (s *SubtitleService) whisperXHFHubCacheDir() string {
	return filepath.Join(s.whisperXHFHomeDir(), "hub")
}

func (s *SubtitleService) whisperXTorchHomeDir() string {
	return filepath.Join(s.whisperXRuntimeDir(), "torch")
}

func (s *SubtitleService) whisperXXDGCacheDir() string {
	return filepath.Join(s.whisperXRuntimeDir(), "xdg_cache")
}

func (s *SubtitleService) whisperXVenvPython() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(s.whisperXVenvDir(), "Scripts", "python.exe")
	}
	return filepath.Join(s.whisperXVenvDir(), "bin", "python3")
}

func (s *SubtitleService) whisperXManagedPython() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(s.whisperXRuntimeDir(), "python", "python.exe")
	}
	return filepath.Join(s.whisperXRuntimeDir(), "python", "bin", "python3")
}

func (s *SubtitleService) ensureWhisperXWorkerScript() error {
	if err := os.MkdirAll(s.whisperXRuntimeDir(), 0755); err != nil {
		return err
	}

	path := s.whisperXWorkerPath()
	if data, err := os.ReadFile(path); err == nil && string(data) == whisperXWorkerScript {
		return nil
	}

	return os.WriteFile(path, []byte(whisperXWorkerScript), 0644)
}

func (s *SubtitleService) whisperXEnvironment() ([]string, error) {
	dirs := []string{
		s.whisperXRuntimeDir(),
		s.whisperXModelCacheDir(),
		s.whisperXAsrModelDir(),
		s.whisperXAlignModelDir(),
		s.whisperXHFHomeDir(),
		s.whisperXHFHubCacheDir(),
		s.whisperXTorchHomeDir(),
		s.whisperXXDGCacheDir(),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}

	env := append(os.Environ(),
		"PIP_DISABLE_PIP_VERSION_CHECK=1",
		"PIP_PROGRESS_BAR=off",
		"PYTHONUNBUFFERED=1",
		"HF_HUB_DISABLE_XET=1",
		"WHISPERX_ASR_MODEL_DIR="+s.whisperXAsrModelDir(),
		"WHISPERX_ALIGN_MODEL_DIR="+s.whisperXAlignModelDir(),
		"HF_HOME="+s.whisperXHFHomeDir(),
		"HF_HUB_CACHE="+s.whisperXHFHubCacheDir(),
		"TORCH_HOME="+s.whisperXTorchHomeDir(),
		"XDG_CACHE_HOME="+s.whisperXXDGCacheDir(),
	)
	return env, nil
}

func (s *SubtitleService) findBasePython() string {
	if path := s.whisperXManagedPython(); s.pythonMeetsMinimumVersion(path) {
		return path
	}

	candidates := []string{}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/opt/homebrew/bin/python3",
			"/usr/local/bin/python3",
			"/Library/Frameworks/Python.framework/Versions/3.12/bin/python3",
			"/Library/Frameworks/Python.framework/Versions/3.11/bin/python3",
			"/Library/Frameworks/Python.framework/Versions/3.10/bin/python3",
			"/usr/bin/python3",
		)
	} else if runtime.GOOS == "windows" {
		candidates = append(candidates, "python.exe", "python3.exe")
	} else {
		candidates = append(candidates, "/usr/bin/python3", "/usr/local/bin/python3")
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if strings.Contains(candidate, string(os.PathSeparator)) {
			if s.pythonMeetsMinimumVersion(candidate) {
				return candidate
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil && s.pythonMeetsMinimumVersion(path) {
			return path
		}
	}

	if path, err := exec.LookPath("python3"); err == nil && s.pythonMeetsMinimumVersion(path) {
		return path
	}
	if path, err := exec.LookPath("python"); err == nil && s.pythonMeetsMinimumVersion(path) {
		return path
	}
	return ""
}

func (s *SubtitleService) pythonMeetsMinimumVersion(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}

	cmd := exec.Command(path, "-c", `import sys; print(f"{sys.version_info[0]}.{sys.version_info[1]}")`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	version := strings.TrimSpace(string(output))
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return major > 3 || (major == 3 && minor >= 10)
}

func (s *SubtitleService) ensureManagedPython() (string, error) {
	pythonPath := s.whisperXManagedPython()
	if s.pythonMeetsMinimumVersion(pythonPath) {
		return pythonPath, nil
	}

	var url string
	switch {
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		url = managedPythonMacARM64
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		url = managedPythonMacAMD64
	default:
		return "", fmt.Errorf("当前平台缺少 Python 3.10+，且暂未实现自动下载: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	if err := os.MkdirAll(s.whisperXRuntimeDir(), 0755); err != nil {
		return "", err
	}

	archivePath := filepath.Join(s.whisperXRuntimeDir(), "python-runtime.tar.gz")
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("下载 WhisperX Python runtime 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("下载 WhisperX Python runtime 失败: HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return "", err
	}
	out.Close()

	extractCmd := exec.Command("tar", "-xzf", archivePath, "-C", s.whisperXRuntimeDir())
	output, err := extractCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("解压 WhisperX Python runtime 失败: %s", strings.TrimSpace(string(output)))
	}

	if !s.pythonMeetsMinimumVersion(pythonPath) {
		return "", fmt.Errorf("WhisperX Python runtime 解压完成，但未找到可用的 Python 3.10+")
	}
	return pythonPath, nil
}

func (s *SubtitleService) ensureWhisperXVenv() (string, error) {
	basePython := s.findBasePython()
	if basePython == "" {
		var err error
		basePython, err = s.ensureManagedPython()
		if err != nil {
			return "", err
		}
	}

	venvPython := s.whisperXVenvPython()
	if s.pythonMeetsMinimumVersion(venvPython) {
		return venvPython, nil
	}

	_ = os.RemoveAll(s.whisperXVenvDir())

	if err := os.MkdirAll(s.whisperXRuntimeDir(), 0755); err != nil {
		return "", err
	}

	cmd := exec.Command(basePython, "-m", "venv", s.whisperXVenvDir())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("创建 WhisperX 虚拟环境失败: %s", strings.TrimSpace(string(output)))
	}

	if _, err := os.Stat(venvPython); err != nil {
		return "", fmt.Errorf("WhisperX 虚拟环境创建后未找到 python 可执行文件")
	}
	return venvPython, nil
}

func (s *SubtitleService) isWhisperXInstalled() bool {
	venvPython := s.whisperXVenvPython()
	if _, err := os.Stat(venvPython); err != nil {
		return false
	}

	cmd := exec.Command(venvPython, "-c", `from importlib import metadata; import whisperx, numpy; print(metadata.version("whisperx"))`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == whisperXVersion
}

func (s *SubtitleService) installWhisperXRuntime() error {
	if err := s.ensureWhisperXWorkerScript(); err != nil {
		return err
	}

	venvPython, err := s.ensureWhisperXVenv()
	if err != nil {
		return err
	}

	env, err := s.whisperXEnvironment()
	if err != nil {
		return err
	}

	s.emitProgress("whisper", 10, "Preparing WhisperX runtime...")

	upgradePip := exec.Command(venvPython, "-m", "pip", "install", "--upgrade", "pip")
	upgradePip.Env = env
	if output, err := upgradePip.CombinedOutput(); err != nil {
		return fmt.Errorf("升级 pip 失败: %s", strings.TrimSpace(string(output)))
	}

	s.emitProgress("whisper", 45, "Installing WhisperX dependencies...")
	install := exec.Command(venvPython, "-m", "pip", "install", fmt.Sprintf("whisperx==%s", whisperXVersion), "numpy")
	install.Env = env
	if output, err := install.CombinedOutput(); err != nil {
		return fmt.Errorf("安装 WhisperX 失败: %s", strings.TrimSpace(string(output)))
	}

	s.emitProgress("whisper", 100, "WhisperX runtime ready")
	return nil
}

func (s *SubtitleService) transcribeWhisperXWithLang(ctx context.Context, wavPath, sourceLang string) (string, []subtitleparser.Segment, error) {
	if err := s.ensureWhisperXWorkerScript(); err != nil {
		return "", nil, err
	}
	if !s.isWhisperXInstalled() {
		return "", nil, fmt.Errorf("缺少 WhisperX 运行时，请先点击下载依赖")
	}

	venvPython := s.whisperXVenvPython()
	args := []string{
		s.whisperXWorkerPath(),
		"--wav-path", wavPath,
		"--model", whisperXModelName,
		"--language", sourceLang,
		"--compute-type", "int8",
		"--batch-size", "8",
		"--asr-device", "cpu",
		"--align-device", "cpu",
	}

	cmd := exec.CommandContext(ctx, venvPython, args...)
	env, err := s.whisperXEnvironment()
	if err != nil {
		return "", nil, err
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", nil, fmt.Errorf("字幕生成已取消")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		log.Printf("[Subtitle] whisperx error: %v\n%s", err, detail)
		return "", nil, fmt.Errorf("WhisperX 识别失败: %s", detail)
	}

	var payload whisperXPayload
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		log.Printf("[Subtitle] whisperx json parse failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
		return "", nil, fmt.Errorf("WhisperX 输出解析失败")
	}

	segments := make([]subtitleparser.Segment, 0, len(payload.Segments))
	for _, raw := range payload.Segments {
		text := strings.TrimSpace(raw.Text)
		if text == "" {
			continue
		}

		startMs := int64(math.Round(raw.Start * 1000))
		endMs := int64(math.Round(raw.End * 1000))
		if endMs < startMs {
			endMs = startMs
		}

		lines := strings.Split(text, "\n")
		cleanLines := make([]string, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			cleanLines = append(cleanLines, line)
		}
		if len(cleanLines) == 0 {
			continue
		}

		segments = append(segments, subtitleparser.Segment{
			Index:       len(segments) + 1,
			StartTimeMs: startMs,
			EndTimeMs:   endMs,
			Text:        strings.Join(cleanLines, "\n"),
			Lines:       cleanLines,
		})
	}

	if len(segments) == 0 {
		return "", nil, fmt.Errorf("WhisperX 未产生有效字幕，视频可能没有清晰的语音内容")
	}

	detectedLang := strings.TrimSpace(payload.Language)
	if detectedLang == "" {
		if sourceLang == "" || sourceLang == "auto" {
			detectedLang = "unknown"
		} else {
			detectedLang = sourceLang
		}
	}

	return detectedLang, segments, nil
}

func formatSRTTimestamp(ms int64) string {
	hours := ms / 3600000
	minutes := (ms % 3600000) / 60000
	seconds := (ms % 60000) / 1000
	millis := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, seconds, millis)
}

func writeSRT(path string, segments []subtitleparser.Segment) error {
	var builder strings.Builder
	for idx, segment := range segments {
		builder.WriteString(fmt.Sprintf("%d\n", idx+1))
		builder.WriteString(formatSRTTimestamp(segment.StartTimeMs))
		builder.WriteString(" --> ")
		builder.WriteString(formatSRTTimestamp(segment.EndTimeMs))
		builder.WriteString("\n")
		builder.WriteString(segment.Text)
		builder.WriteString("\n\n")
	}
	return os.WriteFile(path, []byte(builder.String()), 0644)
}
