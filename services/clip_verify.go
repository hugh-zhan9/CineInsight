package services

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"time"
)

const (
	clipVerifySide       = 32
	clipVerifyFrameBytes = clipVerifySide * clipVerifySide * 3
	clipVerifySamples    = 5
	clipVerifyTimeout    = 30 * time.Second
)

type clipFrameReader func(context.Context, string, float64) ([]byte, error)

// verifyClipPair 用画面结构与空间位置复核哈希候选。只读源文件，既不调用 AI，
// 也不保存抽帧。读取失败必须报错，不能把未经复核的配对当作候选。
func (s *CleanupService) verifyClipPair(full, clip clipSequence, candidate CleanupClipGroup) (bool, error) {
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, clipVerifyTimeout)
	defer cancel()
	reader := s.clipFrame
	if reader == nil {
		reader = readClipRGBFrame
	}
	if err := checkClipVerificationSources(full, clip); err != nil {
		return false, err
	}
	offset := int(math.Round(candidate.OffsetSeconds / clipFrameIntervalSeconds(clip.intervalMS)))
	positions := clipVerificationPositions(full.hashes, clip.hashes, offset)
	if len(positions) < 3 {
		return false, nil
	}
	hits := 0
	for _, position := range positions {
		// 使用同一采样网格的时点；末帧不能再加半个间隔，以免落到 EOF。
		second := float64(position) * clipFrameIntervalSeconds(clip.intervalMS)
		left, err := reader(ctx, full.video.Path, second+candidate.OffsetSeconds)
		if err != nil {
			return false, clipVerificationReadError(ctx, full.video.ID)
		}
		right, err := reader(ctx, clip.video.Path, second)
		if err != nil {
			return false, clipVerificationReadError(ctx, clip.video.ID)
		}
		if len(left) != clipVerifyFrameBytes || len(right) != clipVerifyFrameBytes {
			return false, fmt.Errorf("截取片段画面复核失败：视频 %d/%d 抽帧数据不完整", full.video.ID, clip.video.ID)
		}
		if clipRGBFramesMatch(left, right) {
			hits++
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := checkClipVerificationSources(full, clip); err != nil {
		return false, err
	}
	return hits*5 >= len(positions)*4, nil
}

func clipVerificationReadError(ctx context.Context, videoID uint) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("截取片段画面复核中止（视频 %d）：%w", videoID, err)
	}
	return fmt.Errorf("截取片段画面复核失败：无法读取视频 %d 的对应画面，请检查文件与 ffmpeg", videoID)
}

func checkClipVerificationSources(sequences ...clipSequence) error {
	for _, sequence := range sequences {
		info, err := os.Stat(sequence.video.Path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("截取片段画面复核失败：视频 %d 的文件不可用", sequence.video.ID)
		}
		if info.Size() != sequence.sourceSize || info.ModTime().UnixNano() != sequence.sourceMod {
			return fmt.Errorf("截取片段画面复核失败：视频 %d 已变化，请补全帧哈希后重新分析", sequence.video.ID)
		}
	}
	return nil
}

// clipVerificationPositions 从已命中的位置均匀取样，不能只验证共同片头。
func clipVerificationPositions(full, clip []uint64, offset int) []int {
	if offset < 0 || offset+len(clip) > len(full) {
		return nil
	}
	matched := make([]int, 0, len(clip))
	for index, hash := range clip {
		if clipFrameMatches(full[offset+index], hash) {
			matched = append(matched, index)
		}
	}
	count := min(clipVerifySamples, len(matched))
	positions := make([]int, count)
	for index := range positions {
		positions[index] = matched[index*(len(matched)-1)/max(1, count-1)]
	}
	return positions
}

// clipRGBFramesMatch 比较归一化亮度结构，不把调色差异直接当成不同内容。
// 全图相关性约束构图，分块相关性约束主体细节；平坦块不提供证据。
// 去均值与归一化方差允许亮度/对比度改变，RGB 抽样转亮度允许保留调色素材。
func clipRGBFramesMatch(left, right []byte) bool {
	if len(left) != clipVerifyFrameBytes || len(right) != clipVerifyFrameBytes {
		return false
	}
	const tileSide = 4
	const tilesPerRow = clipVerifySide / tileSide
	var total clipStructureStats
	var tiles [tilesPerRow * tilesPerRow]clipStructureStats
	for pixel := 0; pixel < clipVerifySide*clipVerifySide; pixel++ {
		index := pixel * 3
		luminance := func(frame []byte) float64 {
			return (77*float64(frame[index]) + 150*float64(frame[index+1]) + 29*float64(frame[index+2])) / 256
		}
		a, b := luminance(left), luminance(right)
		total.add(a, b)
		tile := (pixel/clipVerifySide/tileSide)*tilesPerRow + (pixel%clipVerifySide)/tileSide
		tiles[tile].add(a, b)
	}
	if total.correlation() < 0.90 {
		return false
	}
	informative, matched := 0, 0
	for _, tile := range tiles {
		// 任一侧有结构就必须核对；两边都平坦的背景不撑高命中数。
		if tile.varianceLeft() < 9 && tile.varianceRight() < 9 {
			continue
		}
		informative++
		if tile.correlation() >= 0.80 {
			matched++
		}
	}
	return informative >= 8 && matched*5 >= informative*4
}

type clipStructureStats struct {
	count, left, right, leftSquare, rightSquare, product float64
}

func (s *clipStructureStats) add(left, right float64) {
	s.count++
	s.left += left
	s.right += right
	s.leftSquare += left * left
	s.rightSquare += right * right
	s.product += left * right
}

func (s clipStructureStats) varianceLeft() float64 {
	return s.leftSquare/s.count - (s.left/s.count)*(s.left/s.count)
}
func (s clipStructureStats) varianceRight() float64 {
	return s.rightSquare/s.count - (s.right/s.count)*(s.right/s.count)
}
func (s clipStructureStats) correlation() float64 {
	left, right := s.varianceLeft(), s.varianceRight()
	if left < 9 || right < 9 {
		return 0
	}
	covariance := s.product/s.count - (s.left/s.count)*(s.right/s.count)
	return covariance / math.Sqrt(left*right)
}

// readClipRGBFrame 精确读取指定时点。不能复用近重复抽帧的“回退到更早
// 位置”，因为这里核对的是同一时间偏移下的画面。
func readClipRGBFrame(ctx context.Context, path string, second float64) ([]byte, error) {
	ffmpegBin, err := findThumbnailFFmpeg()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, ffmpegBin,
		"-v", "error", "-nostdin", "-ss", strconv.FormatFloat(second, 'f', 3, 64),
		"-i", path, "-map", "0:V:0", "-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:%d:flags=area", clipVerifySide, clipVerifySide),
		"-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	var output bytes.Buffer
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return nil, err
	}
	if output.Len() != clipVerifyFrameBytes {
		return nil, fmt.Errorf("抽帧字节数 %d，期望 %d", output.Len(), clipVerifyFrameBytes)
	}
	return output.Bytes(), nil
}
