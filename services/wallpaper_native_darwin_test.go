//go:build darwin && cgo

package services

import (
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 只探测合成素材，不调用设置壁纸或打开桌面窗口，不改变测试机器的桌面。
func TestWallpaperNativeImageDimensions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 1920, 800))); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	platform := newWallpaperPlatform()
	defer platform.Close()
	width, height, err := platform.Inspect(path, "image")
	if err != nil || width != 1920 || height != 800 {
		t.Fatalf("native image dimensions: %dx%d %v", width, height, err)
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, valid[:100], 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := platform.Inspect(path, "image"); err == nil {
		t.Fatal("native accepted a truncated image with a valid dimension header")
	}
	if err := os.WriteFile(path, []byte("broken image"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := platform.Inspect(path, "image"); err == nil {
		t.Fatal("native accepted broken image")
	}
}

func TestWallpaperNativeVideoDimensions(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required to create a synthetic video fixture")
	}
	path := filepath.Join(t.TempDir(), "video.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=1920x800:r=1", "-t", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create synthetic video: %v %s", err, output)
	}
	platform := newWallpaperPlatform()
	defer platform.Close()
	width, height, err := platform.Inspect(path, "video")
	if err != nil || width != 1920 || height != 800 {
		t.Fatalf("native video dimensions: %dx%d %v", width, height, err)
	}
	if err := os.WriteFile(path, []byte("broken video"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := platform.Inspect(path, "video"); err == nil {
		t.Fatal("native accepted broken video")
	}
}
