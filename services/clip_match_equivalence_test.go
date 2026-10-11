package services

import (
	"math"
	"math/rand/v2"
	"testing"
)

// 以下 legacy* 是 2026-10 性能优化之前的 MatchClip / 粗筛原样拷贝，作为参照实现：
// 新实现（探针预计算、提前退出、低信息探针整对跳过）必须在任何输入上给出完全相同的
// 偏移、命中率与结论。
func legacyMatchClip(full, clip []uint64, intervalMS int) (int, float64, bool) {
	if !clipLengthsComparable(len(full), len(clip), intervalMS) {
		return 0, 0, false
	}
	bestOffset, bestRate := 0, 0.0
	for _, offset := range legacyClipCoarseOffsets(full, clip) {
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

func legacyClipCoarseOffsets(full, clip []uint64) []int {
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

// equivalenceLowInfoHash 返回一个低信息指纹（置位数 < 8 或 > 56）。
func equivalenceLowInfoHash(rng *rand.Rand) uint64 {
	var hash uint64
	if rng.IntN(2) == 0 {
		hash = ^uint64(0)
	}
	for flips := rng.IntN(8); flips > 0; flips-- {
		hash ^= 1 << rng.IntN(64)
	}
	return hash
}

// equivalenceWalk 生成带场景切换、缓慢漂移与低信息帧（黑场/白场）的序列。
func equivalenceWalk(rng *rand.Rand, length int, lowInfoRate float64) []uint64 {
	out := make([]uint64, length)
	hash := rng.Uint64()
	for index := range out {
		if rng.Float64() < 0.12 {
			hash = rng.Uint64()
		} else {
			for flips := rng.IntN(4); flips > 0; flips-- {
				hash ^= 1 << rng.IntN(64)
			}
		}
		out[index] = hash
		if rng.Float64() < lowInfoRate {
			out[index] = equivalenceLowInfoHash(rng)
		}
	}
	return out
}

func equivalenceFlip(rng *rand.Rand, hash uint64, bitsToFlip int) uint64 {
	for ; bitsToFlip > 0; bitsToFlip-- {
		hash ^= 1 << rng.IntN(64)
	}
	return hash
}

// equivalenceProbePositions 是与粗筛相同公式的探针位置（用来精确击中 12/16 边界）。
func equivalenceProbePositions(length int) []int {
	count := min(clipCoarseProbeFrames, length)
	positions := make([]int, 0, count)
	for index := 0; index < count; index++ {
		positions = append(positions, index*(length-1)/max(1, count-1))
	}
	return positions
}

// equivalenceClipCase 生成一对 (full, clip)：无关、带噪摘录、或恰好在探针边界上被破坏的摘录。
func equivalenceClipCase(rng *rand.Rand) (full, clip []uint64) {
	fullLen := 6 + rng.IntN(500)
	full = equivalenceWalk(rng, fullLen, []float64{0, 0.03, 0.2, 0.5}[rng.IntN(4)])
	if rng.IntN(5) == 0 {
		return full, equivalenceWalk(rng, 1+rng.IntN(fullLen), rng.Float64()*0.4)
	}
	length := 1 + rng.IntN(fullLen)
	if rng.IntN(3) == 0 {
		length = min(fullLen, 5+rng.IntN(16)) // 探针数 < 16 的短片段
	}
	start := rng.IntN(fullLen - length + 1)
	noise := rng.IntN(11)
	clip = make([]uint64, length)
	for index := range clip {
		clip[index] = equivalenceFlip(rng, full[start+index], rng.IntN(noise+1))
	}
	if rng.IntN(2) == 0 {
		// 只破坏探针位置：失配数正好落在 allowMiss 两侧。
		positions := equivalenceProbePositions(length)
		corrupt := rng.IntN(7)
		for _, index := range rng.Perm(len(positions))[:min(corrupt, len(positions))] {
			if rng.IntN(2) == 0 {
				clip[positions[index]] = equivalenceLowInfoHash(rng)
			} else {
				clip[positions[index]] = equivalenceFlip(rng, clip[positions[index]], 9+rng.IntN(20))
			}
		}
	}
	return full, clip
}

func TestMatchClipEquivalentToLegacyOnRandomizedSequences(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261011, 7))
	intervals := []int{clipFrameIntervalMS, clipFrameIntervalMS, clipFrameIntervalMS, 4000, 1000, 0, -1}
	trials := 40000
	if testing.Short() {
		trials = 5000
	}
	positive, boundary := 0, 0
	for trial := 0; trial < trials; trial++ {
		full, clip := equivalenceClipCase(rng)
		interval := intervals[rng.IntN(len(intervals))]
		wantOffset, wantRate, wantOK := legacyMatchClip(full, clip, interval)
		gotOffset, gotRate, gotOK := MatchClip(full, clip, interval)
		if gotOffset != wantOffset || gotRate != wantRate || gotOK != wantOK {
			t.Fatalf("trial %d MatchClip 不一致: got (%d %v %v) want (%d %v %v)", trial, gotOffset, gotRate, gotOK, wantOffset, wantRate, wantOK)
		}
		probe := newClipProbe(clip)
		want := legacyClipCoarseOffsets(full, clip)
		got := clipCoarseOffsets(full, clip, &probe)
		if len(got) != len(want) {
			t.Fatalf("trial %d 粗筛偏移数不一致: got %v want %v", trial, got, want)
		}
		for index := range got {
			if got[index] != want[index] {
				t.Fatalf("trial %d 粗筛偏移不一致: got %v want %v", trial, got, want)
			}
		}
		if wantOK {
			positive++
		}
		if probe.lowInfo == probe.allowMiss || probe.lowInfo == probe.allowMiss+1 {
			boundary++
		}
	}
	if positive < trials/50 || boundary < trials/100 {
		t.Fatalf("夹具覆盖不足: positive=%d boundary=%d", positive, boundary)
	}
	t.Logf("trials=%d positive=%d low-info-boundary=%d identical", trials, positive, boundary)
}
