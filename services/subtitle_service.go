package services

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

	// Check Model
	modelPath := filepath.Join(s.ModelDir, "ggml-base.en.bin")
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

func (s *SubtitleService) GenerateSubtitle(videoID uint, videoPath string) error {
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
		return err // already user-friendly
	}

	// Transcribe
	s.emitProgress("process", 30, "Transcribing (this may take a while)...")

	outputPrefix := strings.TrimSuffix(videoPath, filepath.Ext(videoPath))

	if err := s.transcribeCLI(tempWav, outputPrefix); err != nil {
		return err // already user-friendly
	}

	srtPath := outputPrefix + ".srt"

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

func (s *SubtitleService) transcribeCLI(wavPath, outputPrefix string) error {
	whisperBin := s.findBinary("whisper-cli")
	if whisperBin == "" {
		whisperBin = s.findBinary("whisper-cpp")
	}
	if whisperBin == "" {
		whisperBin = s.findBinary("main")
	}
	if whisperBin == "" {
		return fmt.Errorf("未找到 Whisper，请重新安装依赖")
	}

	modelPath := filepath.Join(s.ModelDir, "ggml-base.en.bin")

	log.Printf("[Subtitle] transcribeCLI: whisper=%s model=%s input=%s output=%s\n", whisperBin, modelPath, wavPath, outputPrefix)
	cmd := exec.Command(whisperBin, "-m", modelPath, "-f", wavPath, "-osrt", "-of", outputPrefix)

	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := string(output)
		log.Printf("[Subtitle] whisper error: %s\n%s\n", err, detail)
		if strings.Contains(detail, "failed to open") || strings.Contains(detail, "no such file") {
			return fmt.Errorf("模型文件缺失，请重新安装依赖")
		}
		return fmt.Errorf("语音识别失败，请确保视频包含有效音频")
	}
	return nil
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
	url := "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin"
	dest := filepath.Join(s.ModelDir, "ggml-base.en.bin")
	s.emitProgress("model", 0, "Downloading Model...")
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
