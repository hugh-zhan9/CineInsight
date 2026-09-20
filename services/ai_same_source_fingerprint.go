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
	// sameSourceFingerprintVersion 在 v2 上是因为 differenceHash 由点采样改成了面积
	// 平均（D-CD01）。ensureFingerprint 的 cacheValid 带版本比较，旧行因此自动失效
	// 重算，不需要迁移。
	sameSourceFingerprintVersion = "same-source-dhash-v2"
	sameSourceHashMatchDistance  = 14
	sameSourceHashMedianDistance = 12
	sameSourceContentChunkSize   = 64 * 1024
)

const (
	// differenceHash 的采样网格：9 列相邻两两比较，每行得到 8 位。
	differenceHashGridWidth  = 9
	differenceHashGridHeight = 8
	// differenceHashMaxSamplesPerAxis 是单个格子在一个轴上的取样上限。两个调用点的
	// 格子都远小于它——长边 480 的图片缩略图约 60 像素，宽度上限 512 的 AI 抽帧约
	// 57 像素——所以它在当前代码里从不触发，只是给将来传进超大图的调用方兜住
	// O(像素数) 的代价。
	differenceHashMaxSamplesPerAxis = 64
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

// differenceHash 计算 64 位 dHash：把 region 切成 9×8 个格子，取每格的亮度均值，
// 同一行内相邻两格比大小得出一位。位序（y*8+x）是既有契约，不能改。
//
// 每格取均值而不是取一个像素，是这里的要害。早先的实现每格只采一个点，等于完全
// 没有低通：实测同一张图的 480px 与 2560px 两个版本会差出 4 位，而近似重复的判定
// 阈值总共才 8——光是"同图不同尺寸"就吃掉一半预算，再叠一点压缩或裁剪就越过阈值
// 报不出来。改成面积平均后同样这两版的距离中位数是 0。视频侧的两条哈希流水线本来
// 就让 ffmpeg 用 scale=9:8:flags=lanczos / flags=area 做同一件事，这里是它们在 Go
// 侧的对应物。
func differenceHash(src image.Image, region image.Rectangle) uint64 {
	if region.Dx() < 1 || region.Dy() < 1 {
		return 0
	}
	var cells [differenceHashGridWidth * differenceHashGridHeight]uint64
	for y := 0; y < differenceHashGridHeight; y++ {
		top, bottom := hashCellBounds(region.Min.Y, region.Dy(), y, differenceHashGridHeight, region.Max.Y)
		for x := 0; x < differenceHashGridWidth; x++ {
			left, right := hashCellBounds(region.Min.X, region.Dx(), x, differenceHashGridWidth, region.Max.X)
			cells[y*differenceHashGridWidth+x] = averageLuma(src, left, right, top, bottom)
		}
	}

	var result uint64
	for y := 0; y < differenceHashGridHeight; y++ {
		row := y * differenceHashGridWidth
		for x := 0; x < differenceHashGridWidth-1; x++ {
			if cells[row+x] > cells[row+x+1] {
				result |= uint64(1) << uint(y*8+x)
			}
		}
	}
	return result
}

// hashCellBounds 给出第 index 个格子在一个轴上的 [lo, hi) 像素区间。图比网格还小时
// 格子会退化成单个像素——夹紧而不是返回空区间，8×7 的小图同样要能出哈希。
func hashCellBounds(start, span, index, divisions, limit int) (int, int) {
	lo := start + index*span/divisions
	if lo >= limit {
		lo = limit - 1
	}
	if lo < start {
		lo = start
	}
	hi := start + (index+1)*span/divisions
	if hi <= lo {
		hi = lo + 1
	}
	if hi > limit {
		hi = limit
	}
	return lo, hi
}

// averageLuma 求 [x0,x1)×[y0,y1) 的平均亮度。格子边长不超过采样上限时逐像素全取，
// 也就是精确的面积平均；超过时按等距位置抽，代价封顶。
func averageLuma(src image.Image, x0, x1, y0, y1 int) uint64 {
	spanX, spanY := x1-x0, y1-y0
	countX, countY := hashAxisSampleCount(spanX), hashAxisSampleCount(spanY)
	var total uint64
	for j := 0; j < countY; j++ {
		py := y0 + j*spanY/countY
		for i := 0; i < countX; i++ {
			px := x0 + i*spanX/countX
			r, g, b, _ := src.At(px, py).RGBA()
			total += uint64((299*r + 587*g + 114*b) / 1000)
		}
	}
	return total / uint64(countX*countY)
}

func hashAxisSampleCount(span int) int {
	if span < 1 {
		return 1
	}
	if span > differenceHashMaxSamplesPerAxis {
		return differenceHashMaxSamplesPerAxis
	}
	return span
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
