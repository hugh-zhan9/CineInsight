package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 运行时契约（P-012 定稿 §1）：随应用发布的 realesrgan-ncnn-vulkan sidecar，
// 身份固定，清单记录每个文件的 SHA-256；任何校验失败都视为能力不可用，
// 不下载、不安装、不降级。
const (
	EnhancementRuntimeIdentity = "realesrgan-ncnn-vulkan-v0.2.0-cineinsight1"
	enhancementRuntimeDirName  = "enhance-runtime"
	enhancementManifestName    = "manifest.json"
)

// EnhancementProfileSpec 是一个固定的模型配置。
type EnhancementProfileSpec struct {
	Profile   string
	ModelName string
	Scale     int
	// NCNN 参数（不含输入输出路径）：-s 2 -t 0 -g 0 -j 1:2:2 -f png
	ExtraArgs []string
}

// EnhancementProfiles 是首版唯二的模型配置；不开放自定义。
var EnhancementProfiles = map[string]EnhancementProfileSpec{
	"general": {Profile: "general", ModelName: "realesrgan-x4plus", Scale: 2, ExtraArgs: []string{"-s", "2", "-t", "0", "-g", "0", "-j", "1:2:2", "-f", "png"}},
	"anime":   {Profile: "anime", ModelName: "realesr-animevideov3", Scale: 2, ExtraArgs: []string{"-s", "2", "-t", "0", "-g", "0", "-j", "1:2:2", "-f", "png"}},
}

// EnhancementRuntimeCapability 描述超分运行时是否可用。
type EnhancementRuntimeCapability struct {
	Available      bool   `json:"available"`
	RuntimeVersion string `json:"runtime_version"`
	BinaryPath     string `json:"-"`
	ModelDir       string `json:"-"`
	ReasonCode     string `json:"reason_code"`
	Message        string `json:"message"`
}

type enhancementManifest struct {
	RuntimeVersion string                    `json:"runtime_version"`
	Binary         string                    `json:"binary"`
	ModelDir       string                    `json:"model_dir"`
	Files          []enhancementManifestFile `json:"files"`
}

type enhancementManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// ErrEnhancementUnavailable 表示超分能力不可用（平台、运行时或校验失败）。
var ErrEnhancementUnavailable = errors.New("enhancement runtime unavailable")

// ProbeEnhancementRuntime 校验平台、清单与文件哈希，返回能力状态。
// runtimeDir 为空时按可执行文件位置解析（.app/Contents/Resources/enhance-runtime
// 或开发模式下可执行文件旁的 enhance-runtime）。
func ProbeEnhancementRuntime(runtimeDir string) EnhancementRuntimeCapability {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return EnhancementRuntimeCapability{ReasonCode: "platform_unsupported", Message: "视频超分首版只支持 Apple Silicon macOS"}
	}
	if runtimeDir == "" {
		runtimeDir = defaultEnhancementRuntimeDir()
	}
	manifestPath := filepath.Join(runtimeDir, enhancementManifestName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: "超分运行时未随应用打包（缺少 enhance-runtime 清单）"}
	}
	var manifest enhancementManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: "超分运行时清单不可解析"}
	}
	if manifest.RuntimeVersion != EnhancementRuntimeIdentity {
		return EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: fmt.Sprintf("超分运行时版本不匹配（期望 %s）", EnhancementRuntimeIdentity)}
	}
	if manifest.Binary == "" || len(manifest.Files) == 0 {
		return EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: "超分运行时清单不完整"}
	}
	for _, file := range manifest.Files {
		if strings.Contains(file.Path, "..") || filepath.IsAbs(file.Path) {
			return EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: "超分运行时清单包含非法路径"}
		}
		digest, err := sha256File(filepath.Join(runtimeDir, file.Path))
		if err != nil {
			return EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: fmt.Sprintf("超分运行时文件缺失或不可读: %s", file.Path)}
		}
		if !strings.EqualFold(digest, file.SHA256) {
			return EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: fmt.Sprintf("超分运行时文件校验失败: %s", file.Path)}
		}
	}
	binaryPath := filepath.Join(runtimeDir, manifest.Binary)
	info, err := os.Stat(binaryPath)
	if err != nil || info.IsDir() || info.Mode().Perm()&0111 == 0 {
		return EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: "超分 sidecar 不可执行"}
	}
	modelDir := filepath.Join(runtimeDir, manifest.ModelDir)
	for _, spec := range EnhancementProfiles {
		for _, ext := range []string{".param", ".bin"} {
			if _, err := os.Stat(filepath.Join(modelDir, spec.ModelName+ext)); err != nil {
				return EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: fmt.Sprintf("超分模型文件缺失: %s%s", spec.ModelName, ext)}
			}
		}
	}
	return EnhancementRuntimeCapability{
		Available:      true,
		RuntimeVersion: manifest.RuntimeVersion,
		BinaryPath:     binaryPath,
		ModelDir:       modelDir,
	}
}

func defaultEnhancementRuntimeDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return enhancementRuntimeDirName
	}
	exeDir := filepath.Dir(exePath)
	candidates := []string{
		filepath.Join(exeDir, "..", "Resources", enhancementRuntimeDirName),
		filepath.Join(exeDir, enhancementRuntimeDirName),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, enhancementManifestName)); err == nil {
			return filepath.Clean(candidate)
		}
	}
	return candidates[0]
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// EnhancementOutputBasename 返回固定输出文件名（P-012 §2）。
func EnhancementOutputBasename(sourcePath, profile string) string {
	base := filepath.Base(sourcePath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return fmt.Sprintf("%s.enhanced-%s-2x.mkv", stem, profile)
}

// EnhancementRequiredDiskBytes 计算运行安全下限（P-012 §3），防溢出。
func EnhancementRequiredDiskBytes(sourceSize int64, sourceWidth, sourceHeight int) int64 {
	const gib = int64(1) << 30
	base := sourceSize * 4
	if base < 8*gib {
		base = 8 * gib
	}
	frameIn := int64(sourceWidth) * int64(sourceHeight) * 4
	frameOut := int64(sourceWidth*2) * int64(sourceHeight*2) * 4
	rawChunk := 120 * (frameIn + frameOut)
	if rawChunk < 0 || base < 0 {
		return int64(^uint64(0) >> 1)
	}
	required := base + rawChunk + gib
	if required < 0 {
		return int64(^uint64(0) >> 1)
	}
	return required
}
