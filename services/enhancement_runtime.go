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
	"sort"
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
	// ModelsInstallable 为真时表示二进制就绪、只差模型，界面应当给出下载入口
	// 而不是一句"不可用"。
	ModelsInstallable bool   `json:"models_installable"`
	ModelInstallDir   string `json:"-"`
}

type enhancementManifest struct {
	RuntimeVersion string                    `json:"runtime_version"`
	Binary         string                    `json:"binary"`
	ModelDir       string                    `json:"model_dir"`
	Files          []enhancementManifestFile `json:"files"`
	// Models 是模型文件的固定身份。模型不随应用打包（用户裁决：按需下载），
	// 但校验口径不变——哈希对不上就当作不可用，不降级、不将就。
	Models []enhancementManifestFile `json:"models"`
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
// EnhancementModelDirFor 返回按需下载的模型安装目录（用户数据目录下）。
func EnhancementModelDirFor(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(dataDir, enhancementRuntimeDirName, "models")
}

// ProbeEnhancementRuntime 校验平台、清单与文件哈希，返回能力状态。
// runtimeDir 为空时按可执行文件位置解析；installedModelDir 是按需下载的模型目录，
// 随包自带模型时（旧布局）仍然优先用包内的那份。
func ProbeEnhancementRuntime(runtimeDir string, installedModelDir string) EnhancementRuntimeCapability {
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
	// 模型优先用包内自带的（旧布局），否则用按需下载装到用户目录的那份。
	bundledModelDir := filepath.Join(runtimeDir, manifest.ModelDir)
	modelDir := bundledModelDir
	if !enhancementModelsPresent(bundledModelDir) && installedModelDir != "" {
		modelDir = installedModelDir
	}
	for _, required := range EnhancementRequiredModelFiles() {
		path := filepath.Join(modelDir, required)
		if _, err := os.Stat(path); err != nil {
			return EnhancementRuntimeCapability{
				ReasonCode:        "models_missing",
				Message:           "超分模型尚未下载（约 52 MB），在设置里下载后即可使用",
				ModelsInstallable: installedModelDir != "",
				ModelInstallDir:   installedModelDir,
				RuntimeVersion:    manifest.RuntimeVersion,
			}
		}
	}
	// 清单声明了模型哈希时逐个校验：下载来的东西必须和随包时是同一份。
	for _, model := range manifest.Models {
		if strings.Contains(model.Path, "..") || filepath.IsAbs(model.Path) {
			return EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: "超分清单包含非法模型路径"}
		}
		digest, err := sha256File(filepath.Join(modelDir, model.Path))
		if err != nil {
			return EnhancementRuntimeCapability{
				ReasonCode:        "models_missing",
				Message:           fmt.Sprintf("超分模型文件缺失或不可读: %s", model.Path),
				ModelsInstallable: installedModelDir != "",
				ModelInstallDir:   installedModelDir,
				RuntimeVersion:    manifest.RuntimeVersion,
			}
		}
		if !strings.EqualFold(digest, model.SHA256) {
			return EnhancementRuntimeCapability{
				ReasonCode:        "models_corrupt",
				Message:           fmt.Sprintf("超分模型校验失败，请重新下载: %s", model.Path),
				ModelsInstallable: installedModelDir != "",
				ModelInstallDir:   installedModelDir,
				RuntimeVersion:    manifest.RuntimeVersion,
			}
		}
	}
	return EnhancementRuntimeCapability{
		Available:       true,
		RuntimeVersion:  manifest.RuntimeVersion,
		BinaryPath:      binaryPath,
		ModelDir:        modelDir,
		ModelInstallDir: installedModelDir,
	}
}

// EnhancementRequiredModelFiles 列出 sidecar 实际会去读的模型文件名。
// realesr-animevideov3 按 <name>-x<scale> 拼路径（上游 main.cpp 行为）。
func EnhancementRequiredModelFiles() []string {
	names := make([]string, 0, 4)
	seen := make(map[string]struct{})
	for _, spec := range EnhancementProfiles {
		for _, ext := range []string{".param", ".bin"} {
			file := spec.ModelName + ext
			if spec.ModelName == "realesr-animevideov3" {
				file = fmt.Sprintf("%s-x%d%s", spec.ModelName, spec.Scale, ext)
			}
			if _, exists := seen[file]; exists {
				continue
			}
			seen[file] = struct{}{}
			names = append(names, file)
		}
	}
	sort.Strings(names)
	return names
}

func enhancementModelsPresent(dir string) bool {
	for _, required := range EnhancementRequiredModelFiles() {
		if _, err := os.Stat(filepath.Join(dir, required)); err != nil {
			return false
		}
	}
	return true
}

// loadEnhancementModelRequirements 读随包清单里的模型身份。清单没声明模型时报错——
// 下载一份无法校验的权重比不下载更糟。
func loadEnhancementModelRequirements(runtimeDir string) ([]enhancementManifestFile, error) {
	if runtimeDir == "" {
		runtimeDir = defaultEnhancementRuntimeDir()
	}
	raw, err := os.ReadFile(filepath.Join(runtimeDir, enhancementManifestName))
	if err != nil {
		return nil, fmt.Errorf("超分运行时未随应用打包，无法下载模型")
	}
	var manifest enhancementManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("超分运行时清单不可解析")
	}
	if len(manifest.Models) == 0 {
		return nil, fmt.Errorf("超分清单没有声明模型校验信息，拒绝下载")
	}
	return manifest.Models, nil
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
