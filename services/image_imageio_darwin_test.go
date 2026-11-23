//go:build darwin

package services

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeImageIOThumbnailAndProbe(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "sample.png")
	dstPath := filepath.Join(dir, "thumb.jpg")

	img := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 1200; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 140, A: 255})
		}
	}
	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	w, h, err := nativeFastImageProbe(context.Background(), srcPath)
	if err != nil {
		t.Fatalf("nativeFastImageProbe failed: %v", err)
	}
	if w != 1200 || h != 800 {
		t.Fatalf("probe dimensions got=%dx%d want=1200x800", w, h)
	}

	if err := nativeFastImageThumbnail(context.Background(), srcPath, dstPath, 480); err != nil {
		t.Fatalf("nativeFastImageThumbnail failed: %v", err)
	}
	outFile, err := os.Open(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()
	decoded, err := jpeg.DecodeConfig(outFile)
	if err != nil {
		t.Fatalf("generated thumbnail is not valid jpeg: %v", err)
	}
	if decoded.Width != 480 || decoded.Height != 320 {
		t.Fatalf("thumbnail dimensions got=%dx%d want=480x320", decoded.Width, decoded.Height)
	}
}
