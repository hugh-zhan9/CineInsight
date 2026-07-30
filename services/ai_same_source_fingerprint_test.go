package services

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizedVideoPair(t *testing.T) {
	left, right, err := normalizedVideoPair(9, 3)
	if err != nil || left != 3 || right != 9 {
		t.Fatalf("unexpected normalized pair left=%d right=%d err=%v", left, right, err)
	}
	if _, _, err := normalizedVideoPair(3, 3); err == nil {
		t.Fatal("same video ID must be rejected")
	}
}

func TestSameSourceFrameHashesSurviveCenterCrop(t *testing.T) {
	original := patternedImage(120, 80)
	cropped := cropImage(original, image.Rect(12, 8, 108, 72))
	left := sameSourceFingerprintPayload{
		Positions: []float64{1, 2, 3, 4, 5},
		Hashes:    repeatFrameHashes(sameSourceFrameHashes(original), 5),
	}
	right := sameSourceFingerprintPayload{
		Positions: []float64{1, 2, 3, 4, 5},
		Hashes:    repeatFrameHashes(sameSourceFrameHashes(cropped), 5),
	}
	_, matched, ok := scoreSameSourceFingerprints(left, right)
	if !ok || matched < 3 {
		t.Fatalf("center crop should remain a candidate matched=%d ok=%v", matched, ok)
	}
}

func TestSameSourceFrameHashesSurviveLowResolutionJPEGReencode(t *testing.T) {
	original := patternedImage(120, 80)
	lowResolution := image.NewRGBA(image.Rect(0, 0, 60, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 60; x++ {
			lowResolution.Set(x, y, original.At(x*2, y*2))
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, lowResolution, &jpeg.Options{Quality: 35}); err != nil {
		t.Fatal(err)
	}
	reencoded, err := jpeg.Decode(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	left := sameSourceFingerprintPayload{Positions: []float64{1, 2, 3, 4, 5}, Hashes: repeatFrameHashes(sameSourceFrameHashes(original), 5)}
	right := sameSourceFingerprintPayload{Positions: []float64{1, 2, 3, 4, 5}, Hashes: repeatFrameHashes(sameSourceFrameHashes(reencoded), 5)}
	if _, matched, ok := scoreSameSourceFingerprints(left, right); !ok || matched < 3 {
		t.Fatalf("low-resolution JPEG re-encode should remain a candidate matched=%d ok=%v", matched, ok)
	}
}

func TestSameSourceFingerprintRejectsDifferentContent(t *testing.T) {
	leftImage := patternedImage(120, 80)
	rightImage := image.NewRGBA(image.Rect(0, 0, 120, 80))
	seed := uint32(0x9e3779b9)
	for y := 0; y < 80; y++ {
		for x := 0; x < 120; x++ {
			seed = seed*1664525 + 1013904223
			rightImage.Set(x, y, color.RGBA{R: uint8(seed >> 24), G: uint8(seed >> 16), B: uint8(seed >> 8), A: 255})
		}
	}
	left := sameSourceFingerprintPayload{Positions: []float64{1, 2, 3, 4, 5}, Hashes: repeatFrameHashes(sameSourceFrameHashes(leftImage), 5)}
	right := sameSourceFingerprintPayload{Positions: []float64{1, 2, 3, 4, 5}, Hashes: repeatFrameHashes(sameSourceFrameHashes(rightImage), 5)}
	if _, _, ok := scoreSameSourceFingerprints(left, right); ok {
		t.Fatal("different content must not pass local same-source threshold")
	}
}

func TestSampledFileContentFingerprintChangesWithContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.bin")
	if err := os.WriteFile(path, []byte("first-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := sampledFileContentFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("second-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := sampledFileContentFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("content fingerprint must change when file content changes")
	}
}

func patternedImage(width, height int) image.Image {
	result := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			result.Set(x, y, color.RGBA{R: uint8((x * 3) % 255), G: uint8((y * 5) % 255), B: uint8((x + y*2) % 255), A: 255})
		}
	}
	return result
}

func cropImage(source image.Image, rectangle image.Rectangle) image.Image {
	result := image.NewRGBA(image.Rect(0, 0, rectangle.Dx(), rectangle.Dy()))
	for y := 0; y < rectangle.Dy(); y++ {
		for x := 0; x < rectangle.Dx(); x++ {
			result.Set(x, y, source.At(rectangle.Min.X+x, rectangle.Min.Y+y))
		}
	}
	return result
}

func repeatFrameHashes(hashes []uint64, count int) [][]uint64 {
	result := make([][]uint64, count)
	for index := range result {
		result[index] = append([]uint64(nil), hashes...)
	}
	return result
}
