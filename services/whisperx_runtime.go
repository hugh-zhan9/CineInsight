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

// managedPythonPathIn 返回托管解释器在某个运行时目录下的位置。
// 抽成包级函数是为了让第二、第三个 sidecar（Qwen、人脸）复用同一套目录约定，
// 行为与原来逐个 runtime 各写一份完全一致。
func managedPythonPathIn(runtimeDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(runtimeDir, "python", "python.exe")
	}
	return filepath.Join(runtimeDir, "python", "bin", "python3")
}

func (s *SubtitleService) whisperXManagedPython() string {
	return managedPythonPathIn(s.whisperXRuntimeDir())
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
	return findPythonInterpreter(s.whisperXManagedPython(), s.pythonMeetsMinimumVersion)
}

// findPythonInterpreter 按"托管解释器 → 平台候选清单 → PATH"的顺序返回第一个被
// accept 接受的解释器路径；一个都没有时返回空串。
//
// 从 SubtitleService.findBasePython 原样抽出来（顺序、候选清单、PATH 兜底都不变），
// 只是把"什么版本算可用"变成参数：人脸 sidecar 的 onnxruntime wheel 只覆盖
// 3.10–3.13，它需要一个带上限的判据，而 WhisperX 仍是"3.10 及以上"。
func findPythonInterpreter(managedPython string, accept func(string) bool) string {
	if path := managedPython; accept(path) {
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
			if accept(candidate) {
				return candidate
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil && accept(path) {
			return path
		}
	}

	if path, err := exec.LookPath("python3"); err == nil && accept(path) {
		return path
	}
	if path, err := exec.LookPath("python"); err == nil && accept(path) {
		return path
	}
	return ""
}

func (s *SubtitleService) pythonMeetsMinimumVersion(path string) bool {
	return pythonVersionInRange(path, 10, 0)
}

// pythonVersionInRange 报告 path 上的解释器是否是 3.minMinor 及以上、且（maxMinor > 0
// 时）不高于 3.maxMinor。
//
// 从 SubtitleService.pythonMeetsMinimumVersion 原样抽出：maxMinor <= 0 时的判据
// 与原来逐字一致（major > 3 || (major == 3 && minor >= minMinor)）。上限是为人脸
// sidecar 加的——它的 onnxruntime wheel 只发到 cp313。
func pythonVersionInRange(path string, minMinor, maxMinor int) bool {
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
	if maxMinor > 0 {
		return major == 3 && minor >= minMinor && minor <= maxMinor
	}
	return major > 3 || (major == 3 && minor >= minMinor)
}

func (s *SubtitleService) ensureManagedPython() (string, error) {
	// WhisperX 侧传 context.Background()：它原来就没有取消与超时，
	// 换成带 ctx 的实现只是让人脸那一侧能取消，这里的行为一个字节都不变。
	return ensureManagedPythonRuntime(context.Background(), s.whisperXRuntimeDir(), "WhisperX", "Python 3.10+", s.pythonMeetsMinimumVersion)
}

// ensureManagedPythonRuntime 把 python-build-standalone 解到 runtimeDir/python 并返回
// 解释器路径；已经装好（accept 接受）时直接返回，不重复下载。
//
// 从 SubtitleService.ensureManagedPython 原样抽出，只把两处会出现在文案里的名字
// 变成参数：label（"WhisperX" / "人脸识别"）与 requirement（版本要求的说法）。
// 传入 "WhisperX" 与 "Python 3.10+" 时，四条错误信息与抽取前逐字一致。
func ensureManagedPythonRuntime(ctx context.Context, runtimeDir, label, requirement string, accept func(string) bool) (string, error) {
	pythonPath := managedPythonPathIn(runtimeDir)
	if accept(pythonPath) {
		return pythonPath, nil
	}

	var url string
	switch {
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		url = managedPythonMacARM64
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		url = managedPythonMacAMD64
	default:
		return "", fmt.Errorf("当前平台缺少 %s，且暂未实现自动下载: %s/%s", requirement, runtime.GOOS, runtime.GOARCH)
	}

	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		return "", err
	}

	archivePath := filepath.Join(runtimeDir, "python-runtime.tar.gz")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("下载 %s Python runtime 失败: %w", label, err)
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("下载 %s Python runtime 失败: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("下载 %s Python runtime 失败: HTTP %d", label, resp.StatusCode)
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

	extractCmd := exec.CommandContext(ctx, "tar", "-xzf", archivePath, "-C", runtimeDir)
	output, err := extractCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("解压 %s Python runtime 失败: %s", label, strings.TrimSpace(string(output)))
	}

	if !accept(pythonPath) {
		return "", fmt.Errorf("%s Python runtime 解压完成，但未找到可用的 %s", label, requirement)
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

	s.emitProgress("prepare", SubtitleEngineWhisperX, "preparing-runtime", 10, "Preparing WhisperX runtime...")

	upgradePip := exec.Command(venvPython, "-m", "pip", "install", "--upgrade", "pip")
	upgradePip.Env = env
	if output, err := upgradePip.CombinedOutput(); err != nil {
		return fmt.Errorf("升级 pip 失败: %s", strings.TrimSpace(string(output)))
	}

	s.emitProgress("prepare", SubtitleEngineWhisperX, "preparing-runtime", 45, "Installing WhisperX dependencies...")
	install := exec.Command(venvPython, "-m", "pip", "install", fmt.Sprintf("whisperx==%s", whisperXVersion), "numpy")
	install.Env = env
	if output, err := install.CombinedOutput(); err != nil {
		return fmt.Errorf("安装 WhisperX 失败: %s", strings.TrimSpace(string(output)))
	}

	s.emitProgress("prepare", SubtitleEngineWhisperX, "preparing-runtime", 100, "WhisperX runtime ready")
	return nil
}

func (s *SubtitleService) transcribeWhisperXWithLang(ctx context.Context, wavPath, sourceLang string, config SubtitleRecognitionConfig) (string, []subtitleparser.Segment, error) {
	if err := s.ensureWhisperXWorkerScript(); err != nil {
		return "", nil, err
	}
	if !s.isWhisperXInstalled() {
		return "", nil, fmt.Errorf("缺少 WhisperX 运行时，请先点击下载依赖")
	}
	config.WhisperXModel = normalizeSubtitleWhisperXModel(config.WhisperXModel)
	config.WhisperXBatchSize = normalizeSubtitleWhisperXBatchSize(config.WhisperXBatchSize)
	config.WhisperXComputeType = normalizeSubtitleWhisperXComputeType(config.WhisperXComputeType)

	venvPython := s.whisperXVenvPython()
	args := []string{
		s.whisperXWorkerPath(),
		"--wav-path", wavPath,
		"--model", config.WhisperXModel,
		"--language", sourceLang,
		"--compute-type", config.WhisperXComputeType,
		"--batch-size", strconv.Itoa(config.WhisperXBatchSize),
		"--asr-device", "cpu",
		"--align-device", "cpu",
	}

	cmd := exec.CommandContext(ctx, venvPython, args...)
	env, err := s.whisperXEnvironment()
	if err != nil {
		return "", nil, err
	}
	if isSubtitleLocalOnlyASR(ctx) {
		env = append(env, "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1")
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
		log.Printf("[Subtitle] whisperx error: %v stderr_bytes=%d", err, len(stderr.Bytes()))
		return "", nil, fmt.Errorf("WhisperX 识别失败: %s", truncateLogSnippet(detail, 500))
	}

	var payload whisperXPayload
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		log.Printf("[Subtitle] whisperx json parse failed: %v stdout_bytes=%d stderr_bytes=%d", err, len(stdout.Bytes()), len(stderr.Bytes()))
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
