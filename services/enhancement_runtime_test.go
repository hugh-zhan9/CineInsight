package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeEnhancementRuntimeFixture(t *testing.T, dir string, mutate func(*enhancementManifest)) {
	t.Helper()
	files := map[string]string{
		"bin/realesrgan-ncnn-vulkan":            "fake-binary",
		"models/realesrgan-x4plus.param":        "p1",
		"models/realesrgan-x4plus.bin":          "b1",
		"models/realesr-animevideov3-x2.param":  "p2",
		"models/realesr-animevideov3-x2.bin":    "b2",
		"licenses/REAL-ESRGAN-LICENSE.txt":      "license",
		"licenses/NCNN-LICENSE.txt":             "license",
		"lib/libMoltenVK.dylib":                 "molten",
		"models/realesrgan-x4plus.license.note": "note",
	}
	manifest := enhancementManifest{
		RuntimeVersion: EnhancementRuntimeIdentity,
		Binary:         "bin/realesrgan-ncnn-vulkan",
		ModelDir:       "models",
	}
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0644)
		if path == manifest.Binary {
			mode = 0755
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(content))
		manifest.Files = append(manifest.Files, enhancementManifestFile{Path: path, SHA256: hex.EncodeToString(digest[:])})
	}
	if mutate != nil {
		mutate(&manifest)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, enhancementManifestName), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestProbeEnhancementRuntimeVerifiesManifestAndModels(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("超分运行时只支持 darwin/arm64")
	}
	dir := t.TempDir()
	writeEnhancementRuntimeFixture(t, dir, nil)
	capability := ProbeEnhancementRuntime(dir)
	if !capability.Available || capability.RuntimeVersion != EnhancementRuntimeIdentity {
		t.Fatalf("capability=%+v", capability)
	}
	if capability.BinaryPath == "" || capability.ModelDir == "" {
		t.Fatalf("missing paths: %+v", capability)
	}
}

func TestProbeEnhancementRuntimeFailsClosed(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("超分运行时只支持 darwin/arm64")
	}
	missing := ProbeEnhancementRuntime(t.TempDir())
	if missing.Available || missing.ReasonCode != "runtime_unavailable" {
		t.Fatalf("missing runtime should be unavailable: %+v", missing)
	}

	tampered := t.TempDir()
	writeEnhancementRuntimeFixture(t, tampered, nil)
	if err := os.WriteFile(filepath.Join(tampered, "models/realesrgan-x4plus.bin"), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	capability := ProbeEnhancementRuntime(tampered)
	if capability.Available || capability.ReasonCode != "runtime_unavailable" {
		t.Fatalf("tampered runtime should be unavailable: %+v", capability)
	}

	wrongVersion := t.TempDir()
	writeEnhancementRuntimeFixture(t, wrongVersion, func(manifest *enhancementManifest) {
		manifest.RuntimeVersion = "realesrgan-ncnn-vulkan-v0.1.0"
	})
	capability = ProbeEnhancementRuntime(wrongVersion)
	if capability.Available {
		t.Fatalf("wrong runtime version should be unavailable: %+v", capability)
	}
}

func TestEnhancementOutputBasenameAndDiskFloor(t *testing.T) {
	if got := EnhancementOutputBasename("/library/a/movie.mp4", "general"); got != "movie.enhanced-general-2x.mkv" {
		t.Fatalf("basename=%q", got)
	}
	if got := EnhancementOutputBasename("/library/b/剧场版.v2.mkv", "anime"); got != "剧场版.v2.enhanced-anime-2x.mkv" {
		t.Fatalf("basename=%q", got)
	}

	const gib = int64(1) << 30
	small := EnhancementRequiredDiskBytes(100<<20, 1280, 720)
	frame := int64(1280)*720*4 + int64(2560)*1440*4
	want := 8*gib + 120*frame + gib
	if small != want {
		t.Fatalf("disk floor=%d want=%d", small, want)
	}
	big := EnhancementRequiredDiskBytes(4*gib, 1920, 1080)
	if big <= 16*gib+gib {
		t.Fatalf("large source should scale with size*4: %d", big)
	}
	if EnhancementRequiredDiskBytes(int64(^uint64(0)>>2), 1920, 1080) <= 0 {
		t.Fatalf("overflow must clamp to positive max")
	}
}
