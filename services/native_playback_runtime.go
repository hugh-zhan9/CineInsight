package services

import (
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const nativeRuntimeIdentity = "libmpv-0.41.0-iina-1.5.0-arm64"
const nativeRuntimeArchiveSHA = "ec7266468a297b907eadbb473c7b3d2992cd274c40d379d8e0e8910caedfae50"

type nativeRuntimeManifest struct {
	MaterialsSHA    string                      `json:"materials_sha256"`
	RuntimeID       string                      `json:"runtime_id"`
	Version         string                      `json:"version"`
	Architecture    string                      `json:"architecture"`
	SourceSHA       string                      `json:"source_sha256"`
	SourcesComplete bool                        `json:"source_materials_complete"`
	Entry           string                      `json:"entry"`
	Files           []nativeRuntimeManifestFile `json:"files"`
}
type nativeRuntimeManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type nativeRuntimeStamp struct {
	path string
	info os.FileInfo
}
type nativeRuntime struct {
	mu         sync.Mutex
	dir        string
	checked    bool
	capability NativePlaybackCapability
	library    string
	stamps     []nativeRuntimeStamp
}

func nativeRuntimeDirFor(executable, explicitDevelopmentDir string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	base := filepath.Dir(executable)
	contents := filepath.Dir(base)
	if strings.EqualFold(filepath.Base(base), "MacOS") && strings.EqualFold(filepath.Base(contents), "Contents") {
		return filepath.Join(contents, "Resources", "native-playback")
	}
	if explicitDevelopmentDir != "" && filepath.IsAbs(explicitDevelopmentDir) {
		return filepath.Clean(explicitDevelopmentDir)
	}
	return filepath.Join(base, "native-playback")
}
func defaultNativeRuntimeDir() string {
	executable, _ := os.Executable()
	return nativeRuntimeDirFor(executable, os.Getenv("CINEINSIGHT_NATIVE_RUNTIME_DIR"))
}
func nativeUnavailable(code, message string) NativePlaybackCapability {
	return NativePlaybackCapability{RuntimeVersion: nativeRuntimeIdentity, Platform: runtime.GOOS + "/" + runtime.GOARCH, ReasonCode: code, ReasonMessage: message}
}
func (r *nativeRuntime) probe() (NativePlaybackCapability, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.checked {
		if r.capability.Available {
			for _, stamp := range r.stamps {
				now, err := os.Lstat(stamp.path)
				if err != nil || !now.Mode().IsRegular() || !os.SameFile(stamp.info, now) || now.Size() != stamp.info.Size() || !now.ModTime().Equal(stamp.info.ModTime()) {
					r.capability = nativeUnavailable("native_runtime_mismatch", "本机播放组件已变化，请重新打开应用")
					r.library = ""
					break
				}
			}
		}
		return r.capability, r.library
	}
	r.checked = true
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		r.capability = nativeUnavailable("native_unavailable", "当前平台没有原生播放组件")
		return r.capability, ""
	}
	if r.dir == "" {
		r.dir = defaultNativeRuntimeDir()
	}
	rootInfo, rootErr := os.Lstat(r.dir)
	if rootErr != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		r.capability = nativeUnavailable("native_unavailable", "本机播放组件目录不可用")
		return r.capability, ""
	}
	manifestPath := filepath.Join(r.dir, "manifest.json")
	file, err := os.Open(manifestPath)
	if err != nil {
		r.capability = nativeUnavailable("native_unavailable", "本机播放组件未随应用安装")
		return r.capability, ""
	}
	raw, err := io.ReadAll(io.LimitReader(file, 128*1024+1))
	file.Close()
	var manifest nativeRuntimeManifest
	if err != nil || len(raw) > 128*1024 || json.Unmarshal(raw, &manifest) != nil || manifest.RuntimeID != nativeRuntimeIdentity || manifest.Version != "0.41.0" || manifest.SourceSHA != nativeRuntimeArchiveSHA || manifest.Architecture != "arm64" || !manifest.SourcesComplete || manifest.Entry != "lib/libmpv.2.dylib" || len(manifest.Files) == 0 || len(manifest.Files) > 128 {
		r.capability = nativeUnavailable("native_runtime_mismatch", "本机播放组件清单校验失败")
		return r.capability, ""
	}
	names := map[string]bool{}
	for _, entry := range manifest.Files {
		parts := strings.Split(entry.Path, "/")
		digest, err := hex.DecodeString(entry.SHA256)
		if len(parts) != 2 || parts[0] != "lib" || !strings.HasSuffix(parts[1], ".dylib") || filepath.Base(parts[1]) != parts[1] || names[parts[1]] || err != nil || len(digest) != 32 {
			r.capability = nativeUnavailable("native_runtime_mismatch", "本机播放组件文件清单无效")
			return r.capability, ""
		}
		names[parts[1]] = true
	}
	if !names["libmpv.2.dylib"] {
		r.capability = nativeUnavailable("native_runtime_mismatch", "本机播放组件缺少入口")
		return r.capability, ""
	}
	libraryDir, err := os.Lstat(filepath.Join(r.dir, "lib"))
	if err != nil || !libraryDir.IsDir() || libraryDir.Mode()&os.ModeSymlink != 0 {
		r.capability = nativeUnavailable("native_runtime_mismatch", "本机播放组件目录无效")
		return r.capability, ""
	}
	r.stamps = nil
	for _, entry := range manifest.Files {
		path := filepath.Join(r.dir, filepath.FromSlash(entry.Path))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			r.capability = nativeUnavailable("native_runtime_mismatch", "本机播放组件文件缺失")
			return r.capability, ""
		}
		digest, err := sha256File(path)
		if err != nil || digest != entry.SHA256 || verifyNativeMachO(path, names) != nil {
			r.capability = nativeUnavailable("native_runtime_mismatch", "本机播放组件完整性或系统版本校验失败")
			return r.capability, ""
		}
		r.stamps = append(r.stamps, nativeRuntimeStamp{path, info})
	}
	info, err := os.Lstat(manifestPath)
	if err != nil || !info.Mode().IsRegular() {
		r.capability = nativeUnavailable("native_runtime_mismatch", "本机播放组件清单无效")
		return r.capability, ""
	}
	materialStamps, err := verifyNativeMaterials(r.dir, manifest.MaterialsSHA)
	if err != nil {
		r.capability = nativeUnavailable("native_runtime_mismatch", "本机播放组件来源材料校验失败")
		return r.capability, ""
	}
	r.stamps = append(r.stamps, materialStamps...)
	r.stamps = append(r.stamps, nativeRuntimeStamp{manifestPath, info})
	r.library = filepath.Join(r.dir, filepath.FromSlash(manifest.Entry))
	r.capability = NativePlaybackCapability{Available: true, RuntimeVersion: nativeRuntimeIdentity, Platform: runtime.GOOS + "/" + runtime.GOARCH}
	return r.capability, r.library
}
func verifyNativeMachO(path string, names map[string]bool) error {
	binary, err := macho.Open(path)
	if err != nil {
		return err
	}
	defer binary.Close()
	if binary.Cpu != macho.CpuArm64 {
		return errors.New("wrong native architecture")
	}
	minimumFound := false
	for _, load := range binary.Loads {
		data := load.Raw()
		if len(data) >= 16 && binary.ByteOrder.Uint32(data) == 0x32 {
			minimumFound = true
			if binary.ByteOrder.Uint32(data[8:]) != 1 || binary.ByteOrder.Uint32(data[12:]) > 15<<16 {
				return errors.New("unsupported native OS target")
			}
		}
	}
	if !minimumFound {
		return errors.New("native OS target missing")
	}
	libraries, err := binary.ImportedLibraries()
	if err != nil {
		return err
	}
	for _, dependency := range libraries {
		if strings.HasPrefix(dependency, "/System/Library/") || strings.HasPrefix(dependency, "/usr/lib/") {
			continue
		}
		if !strings.HasPrefix(dependency, "@loader_path/") || !names[strings.TrimPrefix(dependency, "@loader_path/")] {
			return errors.New("external native dependency")
		}
	}
	return nil
}

func verifyNativeMaterials(root, expected string) ([]nativeRuntimeStamp, error) {
	base := filepath.Join(root, "licenses")
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("native material directory invalid")
	}
	path := filepath.Join(base, "MATERIALS.json")
	digest, err := sha256File(path)
	if err != nil || len(expected) != 64 || digest != expected {
		return nil, errors.New("native material index mismatch")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4*1024*1024+1))
	file.Close()
	if err != nil || len(raw) > 4*1024*1024 {
		return nil, errors.New("native material index budget")
	}
	var index struct {
		Files []nativeRuntimeManifestFile `json:"files"`
	}
	if json.Unmarshal(raw, &index) != nil || len(index.Files) == 0 || len(index.Files) > 10000 {
		return nil, errors.New("native material index invalid")
	}
	seen := map[string]bool{}
	var stamps []nativeRuntimeStamp
	for _, entry := range index.Files {
		if !filepath.IsLocal(entry.Path) || seen[entry.Path] {
			return nil, errors.New("native material path invalid")
		}
		seen[entry.Path] = true
		target := filepath.Join(base, entry.Path)
		actual, err := os.Lstat(target)
		if err != nil || !actual.Mode().IsRegular() {
			return nil, errors.New("native material missing")
		}
		digest, err := sha256File(target)
		if err != nil || digest != entry.SHA256 {
			return nil, errors.New("native material checksum mismatch")
		}
		stamps = append(stamps, nativeRuntimeStamp{target, actual})
	}
	if !seen["SOURCES.json"] || !seen["recipe/versions.env"] {
		return nil, errors.New("native source index incomplete")
	}
	info, err = os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("native material index not regular")
	}
	stamps = append(stamps, nativeRuntimeStamp{path, info})
	return stamps, nil
}
