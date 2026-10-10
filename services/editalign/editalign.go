// Package editalign 为视频工作台提供纯计算的片头识别与高清分段对齐
// （视频编辑合同「分析：片头识别与分段对齐」）。它不访问数据库，
// 只通过注入的 ffmpeg 读取器拿帧；services 层负责取媒体槽、来源校验与持久化。
package editalign

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
)

// ErrNotImplemented 是骨架占位，保留只为兼容已有引用；本包任何函数都不再返回它。
var ErrNotImplemented = errors.New("editalign: not implemented")

// Sequence 是从 StartMS 起按固定 StepMS 采样的 9×8 灰度 dHash 序列。
// Hashes[i] 对应时间 StartMS + i*StepMS。
type Sequence struct {
	StartMS int64
	StepMS  int64
	Hashes  []uint64
}

// GrayFrame 是用于帧级细化的原帧率小灰度图（Side×Side 字节）及其时间。
type GrayFrame struct {
	PTSMS  int64
	Pixels []byte
}

// GrayFrameReader 读取 [startMS, startMS+durationMS) 窗口内的全部原帧率灰度帧，按时间升序。
type GrayFrameReader func(ctx context.Context, startMS, durationMS int64) ([]GrayFrame, error)

// Source 描述一个参与分析的视频。
type Source struct {
	ID         uint
	DurationMS int64
	Hashes     Sequence
	Frames     GrayFrameReader
}

// IntroStatus 是单个视频片头识别结果。
type IntroStatus string

const (
	IntroDetected   IntroStatus = "detected"
	IntroUndetected IntroStatus = "undetected"
	IntroAmbiguous  IntroStatus = "ambiguous"
)

// IntroResult 给出某个来源的片头区间 [StartMS, EndMS)。未识别时区间为零。
type IntroResult struct {
	SourceID    uint
	Status      IntroStatus
	StartMS     int64
	EndMS       int64
	MatchRate   float64
	MatchedWith int
}

// IntroOptions 的零值字段使用合同默认值：MinDurationMS=10000，MaxHamming=8。
type IntroOptions struct {
	MinDurationMS int64
	MaxHamming    int
}

// DetectIntros 在 ≥2 个来源的前段序列中寻找共享片头；少于 2 个来源返回错误。
func DetectIntros(ctx context.Context, sources []Source, opts IntroOptions) ([]IntroResult, error) {
	return detectIntros(ctx, sources, opts)
}

// SegmentStatus 标记对齐段是否与其他段在长版时间线上冲突。
type SegmentStatus string

const (
	SegmentMatched  SegmentStatus = "matched"
	SegmentConflict SegmentStatus = "conflict"
)

// AlignedSegment 是长版区间与高清版区间的一段对应；两者时长相等（不变速）。
type AlignedSegment struct {
	LongStartMS int64
	LongEndMS   int64
	HDStartMS   int64
	HDEndMS     int64
	MatchRate   float64
	Status      SegmentStatus
}

// AlignOptions 的零值字段使用合同默认值：AnchorStepMS=1000，MinSegmentMS=3000，
// MergeGapMS=3000，MaxHamming=8，SecondBestMargin=3。
type AlignOptions struct {
	AnchorStepMS     int64
	MinSegmentMS     int64
	MergeGapMS       int64
	MaxHamming       int
	SecondBestMargin int
}

// AlignSegments 找出高清来源在长版时间线上的全部对应段，按长版时间升序。
// progress 可为 nil；done/total 以锚点计。
func AlignSegments(ctx context.Context, long, hd Source, opts AlignOptions, progress func(done, total int)) ([]AlignedSegment, error) {
	return alignSegments(ctx, long, hd, opts, progress)
}

// HashGray9x8 计算与 services.frameHashFromGrayscale 相同位序的 dHash（72 字节输入）。
func HashGray9x8(pixels []byte) (uint64, error) {
	if len(pixels) != hashGrayBytes {
		return 0, fmt.Errorf("editalign: 灰度帧有 %d 字节，期望 %d", len(pixels), hashGrayBytes)
	}
	var value uint64
	for row := 0; row < 8; row++ {
		for column := 0; column < 8; column++ {
			value <<= 1
			if pixels[row*9+column] > pixels[row*9+column+1] {
				value |= 1
			}
		}
	}
	return value, nil
}

// Informative 判断指纹是否有足够明暗变化可作为证据（与清理同口径：8≤popcount≤56）。
func Informative(hash uint64) bool {
	ones := bits.OnesCount64(hash)
	return ones >= 8 && ones <= 56
}

// ReadHashSequence 用 ffmpeg 读取 [startMS, startMS+durationMS) 的 dHash 序列（durationMS≤0 表示到结尾）。
func ReadHashSequence(ctx context.Context, ffmpegBin, path string, startMS, durationMS, stepMS int64) (Sequence, error) {
	return readHashSequence(ctx, ffmpegBin, path, startMS, durationMS, stepMS)
}

// NewFFmpegGrayReader 返回读取原帧率 side×side 灰度帧的读取器。
func NewFFmpegGrayReader(ffmpegBin, path string, side int) GrayFrameReader {
	return func(ctx context.Context, startMS, durationMS int64) ([]GrayFrame, error) {
		return readGrayFrames(ctx, ffmpegBin, path, side, startMS, durationMS)
	}
}
