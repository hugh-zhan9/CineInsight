package services

import (
	"encoding/binary"
	"math"
	"math/bits"
)

// 截取片段识别的全部阈值集中在这里（D-027）。改动任何一个都会改变清理中心
// "截取片段"类别的判定，clip_match_test.go 的合成序列 fixture 把它们钉住。
const (
	// clipFrameIntervalMS 是帧哈希序列的采样间隔：2 秒一帧（D-026）。
	// 它同时是 ffmpeg 抽帧滤镜与 video_frame_hash_sequences.interval_ms 的唯一来源，
	// 两处从同一个常量算出来，就不可能各自漂移。
	clipFrameIntervalMS = 2000
	// clipMaxDurationRatio：截取片段必须明显短于完整片，否则那是"同一部片的两个
	// 版本"，该由近似重复与同源那两条路径回答，不是截取。
	clipMaxDurationRatio = 0.9
	// clipMinDurationSeconds：太短的片段（几秒的表情包、误录）画面本来就容易撞，
	// 判定不可靠，一律不进候选。
	clipMinDurationSeconds = 10
	// clipHammingThreshold：单帧 dHash 的汉明距离容差。转码、缩放、码率变化会让
	// 同一帧的 dHash 差几位，8 位保留少量转码误差；哈希命中还需画面结构复核。
	clipHammingThreshold = 8
	// clipMatchRateThreshold：整段序列的命中率下限。要求 80% 的帧提供有效证据，
	// 低信息帧不计命中，但仍留在分母，不能靠黑场撑高分数。
	clipMatchRateThreshold = 0.80
	// clipCoarseProbeFrames / clipCoarseProbeHits：粗筛均匀取 B 的最多 16 帧在 A 上找
	// 命中 ≥ 12 帧的偏移，只有这些偏移才做全序列验证。全量对每个偏移都比 len(B)
	// 帧是 O(N·M)，粗筛把常数从"片段长度"降到 16。
	clipCoarseProbeFrames = 16
	clipCoarseProbeHits   = 12
)

// clipMinFrames 是"至少 10 秒"换算成帧数后的下限。
// 10 秒按 2 秒一帧是 5 帧（t=0,2,4,6,8）。
func clipMinFrames(intervalMS int) int {
	if intervalMS <= 0 {
		return 0
	}
	return int(math.Ceil(clipMinDurationSeconds * 1000.0 / float64(intervalMS)))
}

// clipFrameIntervalSeconds 把采样间隔换成秒，供偏移换算与时长判断使用。
func clipFrameIntervalSeconds(intervalMS int) float64 {
	return float64(intervalMS) / 1000.0
}

// MatchClip 判断 clip 是否是 full 里的一段，返回对齐偏移（帧）、命中率与结论（D-027）。
//
// 命中率 = 落在汉明距离容差内的帧数 / len(clip)；取命中率最高的偏移，命中率
// ≥ clipMatchRateThreshold 才算候选。命中率相同取更小的偏移，结果与输入顺序无关。
//
// intervalMS 是这两条序列的采样间隔（调用方传行上的 interval_ms，而不是让这里
// 假定包级常量）：「至少 10 秒」只有换算成帧数才能判，而帧数与间隔一一对应。
// 拿常量顶替行上的值，等于在库里存着 4 秒间隔的旧行时按 2 秒去判长度。
//
// 长度前置条件（片段必须短于 0.9 倍且不短于 10 秒）在这里就判掉，不只在配对
// 阶段判：这个函数是判定的唯一入口，边界写在入口上才不会被下一个调用方绕过。
//
// 不识别倍速、镜像、裁剪画面的截取——那些会让逐帧 dHash 整体错位，本方法的
// 前提（同一帧缩放后 dHash 接近）不成立。
func MatchClip(full, clip []uint64, intervalMS int) (int, float64, bool) {
	if !clipLengthsComparable(len(full), len(clip), intervalMS) {
		return 0, 0, false
	}
	bestOffset, bestRate := 0, 0.0
	for _, offset := range clipCoarseOffsets(full, clip) {
		rate := clipMatchRateAt(full, clip, offset)
		if rate > bestRate {
			bestOffset, bestRate = offset, rate
		}
	}
	if bestRate < clipMatchRateThreshold {
		return 0, 0, false
	}
	return bestOffset, bestRate, true
}

// clipLengthsComparable 是候选对的长度预筛（D-027）：片段短于完整片的 0.9 倍，
// 且自身不短于 10 秒。两个序列必须来自同一采样间隔，否则逐帧对齐无从谈起。
func clipLengthsComparable(fullFrames, clipFrames, intervalMS int) bool {
	if fullFrames <= 0 || clipFrames <= 0 {
		return false
	}
	// 间隔非法就没法把「至少 10 秒」换算成帧数。这里不回退到默认间隔：
	// 拿一个猜出来的间隔给出结论，比拒绝回答更糟。
	if intervalMS <= 0 {
		return false
	}
	if clipFrames < clipMinFrames(intervalMS) {
		return false
	}
	return float64(clipFrames) <= clipMaxDurationRatio*float64(fullFrames)
}

// clipHashInformative 只把有足够明暗变化的指纹当成证据。全黑、全白和
// 单向渐变可能有完全相同的指纹，不能据此认定两帧相同。
func clipHashInformative(hash uint64) bool {
	ones := bits.OnesCount64(hash)
	return ones >= 8 && ones <= 56
}

func clipFrameMatches(left, right uint64) bool {
	return clipHashInformative(left) && clipHashInformative(right) &&
		bits.OnesCount64(left^right) <= clipHammingThreshold
}

// clipCoarseOffsets 均匀检查整段，避免只凭共用片头进入全量比较。
func clipCoarseOffsets(full, clip []uint64) []int {
	probe := min(clipCoarseProbeFrames, len(clip))
	required := int(math.Ceil(float64(probe) * float64(clipCoarseProbeHits) / float64(clipCoarseProbeFrames)))
	offsets := make([]int, 0, 8)
	for offset := 0; offset <= len(full)-len(clip); offset++ {
		hits := 0
		for index := 0; index < probe; index++ {
			position := index * (len(clip) - 1) / max(1, probe-1)
			if clipFrameMatches(full[offset+position], clip[position]) {
				hits++
			}
		}
		if hits >= required {
			offsets = append(offsets, offset)
		}
	}
	return offsets
}

// clipMatchRateAt 要求证据分布在全段，并包含不同画面；一个静态背景重复
// 一千次也不等于一千份独立证据。三个代表指纹各自至少相差 9 位。
func clipMatchRateAt(full, clip []uint64, offset int) float64 {
	if offset < 0 || offset+len(clip) > len(full) || len(clip) == 0 {
		return 0
	}
	hits := 0
	var sectionHits, sectionCounts [4]int
	representatives := make([]uint64, 0, 3)
	for index, hash := range clip {
		section := index * 4 / len(clip)
		sectionCounts[section]++
		if !clipFrameMatches(full[offset+index], hash) {
			continue
		}
		hits++
		sectionHits[section]++
		if len(representatives) < 3 {
			distinct := true
			for _, existing := range representatives {
				if bits.OnesCount64(existing^hash) <= clipHammingThreshold {
					distinct = false
					break
				}
			}
			if distinct {
				representatives = append(representatives, hash)
			}
		}
	}
	if len(representatives) < 3 {
		return 0
	}
	for section, count := range sectionCounts {
		if sectionHits[section]*2 < count {
			return 0
		}
	}
	return float64(hits) / float64(len(clip))
}

// encodeFrameHashes 把序列打成连续的 uint64 大端字节流（D-026 的存储格式）。
func encodeFrameHashes(hashes []uint64) []byte {
	encoded := make([]byte, len(hashes)*8)
	for index, hash := range hashes {
		binary.BigEndian.PutUint64(encoded[index*8:], hash)
	}
	return encoded
}

// decodeFrameHashes 读回序列。长度不是 8 的整数倍说明这一行被截断了，
// 与其猜哪一半是好的，不如当成没有序列（调用方按"待重算"处理）。
func decodeFrameHashes(encoded []byte) []uint64 {
	if len(encoded) == 0 || len(encoded)%8 != 0 {
		return nil
	}
	hashes := make([]uint64, len(encoded)/8)
	for index := range hashes {
		hashes[index] = binary.BigEndian.Uint64(encoded[index*8:])
	}
	return hashes
}
