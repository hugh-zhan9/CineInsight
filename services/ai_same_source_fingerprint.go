package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"math/bits"
	"os"
	"sort"
)

const (
	sameSourceFingerprintVersion = "same-source-dhash-v1"
	sameSourceHashMatchDistance  = 14
	sameSourceHashMedianDistance = 12
	sameSourceContentChunkSize   = 64 * 1024
)

type sameSourceFingerprintPayload struct {
	Positions []float64  `json:"positions"`
	Hashes    [][]uint64 `json:"hashes"`
}

func normalizedVideoPair(left, right uint) (uint, uint, error) {
	if left == 0 || right == 0 || left == right {
		return 0, 0, fmt.Errorf("same-source videos must be distinct non-zero IDs")
	}
	if left < right {
		return left, right, nil
	}
	return right, left, nil
}

func encodeSameSourceFingerprint(payload sameSourceFingerprintPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode same-source fingerprint: %w", err)
	}
	return string(data), nil
}

func decodeSameSourceFingerprint(value string) (sameSourceFingerprintPayload, error) {
	var payload sameSourceFingerprintPayload
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return payload, fmt.Errorf("decode same-source fingerprint: %w", err)
	}
	if len(payload.Hashes) == 0 || len(payload.Hashes) != len(payload.Positions) {
		return payload, fmt.Errorf("invalid same-source fingerprint payload")
	}
	return payload, nil
}

func sameSourceFrameHashes(src image.Image) []uint64 {
	if src == nil || src.Bounds().Dx() < 2 || src.Bounds().Dy() < 2 {
		return nil
	}
	bounds := src.Bounds()
	regions := []image.Rectangle{
		bounds,
		centerCrop(bounds, 0.80, 0.80),
		centerCrop(bounds, 0.60, 0.60),
		anchoredCrop(bounds, 0.80, 1.00, 0.00, 0.00),
		anchoredCrop(bounds, 0.80, 1.00, 1.00, 0.00),
		anchoredCrop(bounds, 1.00, 0.80, 0.00, 0.00),
		anchoredCrop(bounds, 1.00, 0.80, 0.00, 1.00),
	}
	hashes := make([]uint64, 0, len(regions))
	for _, region := range regions {
		if region.Dx() < 2 || region.Dy() < 2 {
			continue
		}
		hashes = append(hashes, differenceHash(src, region))
	}
	return hashes
}

func centerCrop(bounds image.Rectangle, widthRatio, heightRatio float64) image.Rectangle {
	return anchoredCrop(bounds, widthRatio, heightRatio, 0.5, 0.5)
}

func anchoredCrop(bounds image.Rectangle, widthRatio, heightRatio, anchorX, anchorY float64) image.Rectangle {
	width := maxInt(2, int(float64(bounds.Dx())*widthRatio))
	height := maxInt(2, int(float64(bounds.Dy())*heightRatio))
	width = minInt(width, bounds.Dx())
	height = minInt(height, bounds.Dy())
	availableX := bounds.Dx() - width
	availableY := bounds.Dy() - height
	x := bounds.Min.X + int(float64(availableX)*anchorX)
	y := bounds.Min.Y + int(float64(availableY)*anchorY)
	return image.Rect(x, y, x+width, y+height)
}

func differenceHash(src image.Image, region image.Rectangle) uint64 {
	var result uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			left := sampledLuma(src, region, x, y, 9, 8)
			right := sampledLuma(src, region, x+1, y, 9, 8)
			if left > right {
				result |= uint64(1) << uint(y*8+x)
			}
		}
	}
	return result
}

func sampledLuma(src image.Image, region image.Rectangle, x, y, width, height int) uint32 {
	px := region.Min.X + minInt(region.Dx()-1, x*region.Dx()/width)
	py := region.Min.Y + minInt(region.Dy()-1, y*region.Dy()/height)
	r, g, b, _ := src.At(px, py).RGBA()
	return (299*r + 587*g + 114*b) / 1000
}

func scoreSameSourceFingerprints(left, right sameSourceFingerprintPayload) (medianDistance, matchedAnchors int, ok bool) {
	count := minInt(len(left.Hashes), len(right.Hashes))
	if count == 0 {
		return 0, 0, false
	}
	distances := make([]int, 0, count)
	for index := 0; index < count; index++ {
		distance, found := minimumHashDistance(left.Hashes[index], right.Hashes[index])
		if !found {
			continue
		}
		distances = append(distances, distance)
		if distance <= sameSourceHashMatchDistance {
			matchedAnchors++
		}
	}
	if len(distances) == 0 {
		return 0, 0, false
	}
	sort.Ints(distances)
	medianDistance = distances[len(distances)/2]
	return medianDistance, matchedAnchors, matchedAnchors >= 3 && medianDistance <= sameSourceHashMedianDistance
}

func minimumHashDistance(left, right []uint64) (int, bool) {
	if len(left) == 0 || len(right) == 0 {
		return 0, false
	}
	minimum := 65
	for _, a := range left {
		for _, b := range right {
			distance := bits.OnesCount64(a ^ b)
			if distance < minimum {
				minimum = distance
			}
		}
	}
	return minimum, true
}

func sampledFileContentFingerprint(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open video for content fingerprint: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat video for content fingerprint: %w", err)
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "size:%d\n", info.Size())
	positions := []int64{0}
	if info.Size() > sameSourceContentChunkSize*3 {
		positions = append(positions, info.Size()/2)
	}
	if info.Size() > sameSourceContentChunkSize {
		positions = append(positions, maxInt64(0, info.Size()-sameSourceContentChunkSize))
	}
	buffer := make([]byte, sameSourceContentChunkSize)
	for _, position := range positions {
		if _, err := file.Seek(position, io.SeekStart); err != nil {
			return "", fmt.Errorf("seek video for content fingerprint: %w", err)
		}
		read, readErr := io.ReadFull(file, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return "", fmt.Errorf("read video for content fingerprint: %w", readErr)
		}
		_, _ = hash.Write(buffer[:read])
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
