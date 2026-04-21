package services

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"video-master/services/subtitleparser"
)

const (
	qwenRuntimeDirName = "qwen_asr_sidecar"
	qwenVenvDirName    = "venv"
	qwenWorkerFileName = "qwen_asr_worker.py"
	qwenPackageName    = "qwen-asr"
	qwenModelName      = "Qwen/Qwen3-ASR-1.7B"
	qwenAlignerName    = "Qwen/Qwen3-ForcedAligner-0.6B"
)

//go:embed qwen_asr_worker.py
var qwenWorkerScript string

type qwenPayload struct {
	Language string               `json:"language"`
	Segments []qwenPayloadSegment `json:"segments"`
}

type qwenPayloadSegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

func (s *SubtitleService) qwenRuntimeDir() string {
	return filepath.Join(s.BaseDir, qwenRuntimeDirName)
}

func (s *SubtitleService) qwenWorkerPath() string {
	return filepath.Join(s.qwenRuntimeDir(), qwenWorkerFileName)
}

func (s *SubtitleService) qwenVenvDir() string {
	return filepath.Join(s.qwenRuntimeDir(), qwenVenvDirName)
}

func (s *SubtitleService) qwenVenvPython() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(s.qwenVenvDir(), "Scripts", "python.exe")
	}
	return filepath.Join(s.qwenVenvDir(), "bin", "python3")
}

func (s *SubtitleService) qwenHFHomeDir() string {
	return filepath.Join(s.qwenRuntimeDir(), "hf")
}

func (s *SubtitleService) qwenTorchHomeDir() string {
	return filepath.Join(s.qwenRuntimeDir(), "torch")
}

func (s *SubtitleService) ensureQwenWorkerScript() error {
	if err := os.MkdirAll(s.qwenRuntimeDir(), 0755); err != nil {
		return err
	}
	path := s.qwenWorkerPath()
	if data, err := os.ReadFile(path); err == nil && string(data) == qwenWorkerScript {
		return nil
	}
	return os.WriteFile(path, []byte(qwenWorkerScript), 0644)
}

func (s *SubtitleService) qwenEnvironment() ([]string, error) {
	dirs := []string{s.qwenRuntimeDir(), s.qwenHFHomeDir(), s.qwenTorchHomeDir()}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}
	return append(os.Environ(),
		"PIP_DISABLE_PIP_VERSION_CHECK=1",
		"PIP_PROGRESS_BAR=off",
		"PYTHONUNBUFFERED=1",
		"HF_HOME="+s.qwenHFHomeDir(),
		"TORCH_HOME="+s.qwenTorchHomeDir(),
	), nil
}

func (s *SubtitleService) ensureQwenVenv() (string, error) {
	basePython := s.findBasePython()
	if basePython == "" {
		return "", fmt.Errorf("Qwen 运行时需要 Python 3.10+，当前环境未找到可用 Python")
	}
	venvPython := s.qwenVenvPython()
	if s.pythonMeetsMinimumVersion(venvPython) {
		return venvPython, nil
	}
	_ = os.RemoveAll(s.qwenVenvDir())
	if err := os.MkdirAll(s.qwenRuntimeDir(), 0755); err != nil {
		return "", err
	}
	cmd := exec.Command(basePython, "-m", "venv", s.qwenVenvDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("创建 Qwen 虚拟环境失败: %s", strings.TrimSpace(string(output)))
	}
	if _, err := os.Stat(venvPython); err != nil {
		return "", fmt.Errorf("Qwen 虚拟环境创建后未找到 python 可执行文件")
	}
	return venvPython, nil
}

func (s *SubtitleService) isQwenInstalled() bool {
	venvPython := s.qwenVenvPython()
	if _, err := os.Stat(venvPython); err != nil {
		return false
	}
	cmd := exec.Command(venvPython, "-c", `import qwen_asr; print("ok")`)
	output, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(output)) == "ok"
}

func (s *SubtitleService) installQwenRuntime() error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return fmt.Errorf("Qwen v1 当前仅默认支持 macOS arm64")
	}
	if err := s.ensureQwenWorkerScript(); err != nil {
		return err
	}
	venvPython, err := s.ensureQwenVenv()
	if err != nil {
		return err
	}
	env, err := s.qwenEnvironment()
	if err != nil {
		return err
	}
	s.emitProgress("prepare", SubtitleEngineQwen, "preparing-runtime", 10, "Preparing Qwen ASR runtime...")
	upgradePip := exec.Command(venvPython, "-m", "pip", "install", "--upgrade", "pip", "setuptools", "wheel")
	upgradePip.Env = env
	if output, err := upgradePip.CombinedOutput(); err != nil {
		return fmt.Errorf("升级 Qwen pip 依赖失败: %s", strings.TrimSpace(string(output)))
	}
	s.emitProgress("prepare", SubtitleEngineQwen, "preparing-runtime", 45, "Installing Qwen ASR dependencies...")
	install := exec.Command(venvPython, "-m", "pip", "install", "-U", qwenPackageName, "numpy", "soundfile")
	install.Env = env
	if output, err := install.CombinedOutput(); err != nil {
		return fmt.Errorf("安装 Qwen ASR 失败: %s", strings.TrimSpace(string(output)))
	}
	s.emitProgress("prepare", SubtitleEngineQwen, "preparing-runtime", 100, "Qwen ASR runtime ready")
	return nil
}

func (s *SubtitleService) transcribeQwenWithLang(ctx context.Context, wavPath, sourceLang string) (string, []subtitleparser.Segment, error) {
	if err := s.ensureQwenWorkerScript(); err != nil {
		return "", nil, err
	}
	if !s.isQwenInstalled() {
		return "", nil, fmt.Errorf("缺少 Qwen ASR 运行时，请先点击准备组件")
	}
	venvPython := s.qwenVenvPython()
	args := []string{
		s.qwenWorkerPath(),
		"--wav-path", wavPath,
		"--model", qwenModelName,
		"--aligner", qwenAlignerName,
		"--language", sourceLang,
	}
	cmd := exec.CommandContext(ctx, venvPython, args...)
	env, err := s.qwenEnvironment()
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
		return "", nil, fmt.Errorf("Qwen ASR 识别失败: %s", detail)
	}
	var payload qwenPayload
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return "", nil, fmt.Errorf("Qwen ASR 输出解析失败")
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
		lines := []string{text}
		segments = append(segments, subtitleparser.Segment{
			Index:       len(segments) + 1,
			StartTimeMs: startMs,
			EndTimeMs:   endMs,
			Text:        text,
			Lines:       lines,
		})
	}
	if len(segments) == 0 {
		return "", nil, fmt.Errorf("Qwen ASR 未产生有效字幕，视频可能没有清晰的语音内容")
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
