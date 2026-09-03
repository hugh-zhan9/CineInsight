package services

import (
	"math/bits"
	"math/rand"
	"testing"
)

// 合成序列 fixture（D-027、TC-08）：
//   - A 是 3600 帧（按 2 秒一帧 = 两小时）的随机 64 位序列；
//   - B 是 A[600:1200]（第 20 分钟起的 20 分钟）每帧加 ≤3 位噪声，模拟转码后的截取；
//   - C 是与 A 无关的随机序列。
//
// 随机 uint64 两两之间的期望汉明距离是 32，落进 10 位容差里的概率约 1e-4 量级，
// 600 帧里全都撞上是不可能事件——这就是"无关视频不该出现"这条断言的底气。
func syntheticClipFixture(t *testing.T) (full, clip, unrelated []uint64) {
	t.Helper()
	source := rand.New(rand.NewSource(20260902))
	full = make([]uint64, 3600)
	for index := range full {
		full[index] = source.Uint64()
	}
	clip = make([]uint64, 0, 600)
	for _, hash := range full[600:1200] {
		clip = append(clip, flipRandomBits(source, hash, 3))
	}
	unrelated = make([]uint64, 600)
	for index := range unrelated {
		unrelated[index] = source.Uint64()
	}
	return full, clip, unrelated
}

func flipRandomBits(source *rand.Rand, value uint64, count int) uint64 {
	for i := 0; i < count; i++ {
		value ^= uint64(1) << uint(source.Intn(64))
	}
	return value
}

// bruteForceMatchClip 是不做任何剪枝的参照实现：逐个偏移算整段命中率。
// 它只在测试里存在，用来证明粗筛没有把真正的匹配剪掉。
func bruteForceMatchClip(full, clip []uint64, intervalMS int) (int, float64, bool) {
	if !clipLengthsComparable(len(full), len(clip), intervalMS) {
		return 0, 0, false
	}
	bestOffset, bestRate := 0, 0.0
	for offset := 0; offset <= len(full)-len(clip); offset++ {
		hits := 0
		for index := range clip {
			if bits.OnesCount64(full[offset+index]^clip[index]) <= clipHammingThreshold {
				hits++
			}
		}
		rate := float64(hits) / float64(len(clip))
		if rate > bestRate {
			bestOffset, bestRate = offset, rate
		}
	}
	if bestRate < clipMatchRateThreshold {
		return 0, 0, false
	}
	return bestOffset, bestRate, true
}

func TestMatchClipFindsTheClipOffsetAndRate(t *testing.T) {
	full, clip, _ := syntheticClipFixture(t)

	offset, rate, ok := MatchClip(full, clip, clipFrameIntervalMS)
	if !ok {
		t.Fatalf("A 的中段截取应被识别出来: offset=%d rate=%.3f", offset, rate)
	}
	if offset != 600 {
		t.Fatalf("对齐偏移应为 600 帧（第 1200 秒），实际 %d", offset)
	}
	if rate < 0.9 {
		t.Fatalf("≤3 位噪声下命中率应 ≥ 0.9，实际 %.3f", rate)
	}
}

func TestMatchClipRejectsUnrelatedSequence(t *testing.T) {
	full, _, unrelated := syntheticClipFixture(t)

	offset, rate, ok := MatchClip(full, unrelated, clipFrameIntervalMS)
	if ok {
		t.Fatalf("无关序列不该成为候选: offset=%d rate=%.3f", offset, rate)
	}
	if rate != 0 || offset != 0 {
		t.Fatalf("不成候选时应返回零值: offset=%d rate=%.3f", offset, rate)
	}
}

// 长度前置条件（D-027）：片段必须短于完整片的 0.9 倍，否则那是"同一部片的两个
// 版本"，该由近似重复与同源回答。
func TestMatchClipRejectsSequenceLongerThanRatio(t *testing.T) {
	full, _, _ := syntheticClipFixture(t)

	// 3241 帧 > 0.9 × 3600 = 3240：这一份即便逐帧全中也不进候选。
	tooLong := append([]uint64(nil), full[:3241]...)
	if _, _, ok := MatchClip(full, tooLong, clipFrameIntervalMS); ok {
		t.Fatal("长度超过 0.9 倍的片段不该进候选")
	}
	// 正好 3240 帧仍在范围内。
	atLimit := append([]uint64(nil), full[:3240]...)
	if _, _, ok := MatchClip(full, atLimit, clipFrameIntervalMS); !ok {
		t.Fatal("正好 0.9 倍的片段应当仍是候选")
	}
}

// 短于 10 秒的片段不进候选（D-027）：2 秒一帧，10 秒就是 5 帧。
func TestMatchClipRejectsSequenceShorterThanTenSeconds(t *testing.T) {
	full, _, _ := syntheticClipFixture(t)

	if got := clipMinFrames(clipFrameIntervalMS); got != 5 {
		t.Fatalf("10 秒按 2 秒一帧应为 5 帧下限，实际 %d", got)
	}
	if _, _, ok := MatchClip(full, full[600:604], clipFrameIntervalMS); ok {
		t.Fatal("4 帧（8 秒）不该进候选")
	}
	offset, _, ok := MatchClip(full, full[600:605], clipFrameIntervalMS)
	if !ok {
		t.Fatal("5 帧（10 秒）应当进候选")
	}
	if offset != 600 {
		t.Fatalf("5 帧片段的偏移应为 600，实际 %d", offset)
	}
}

// 「至少 10 秒」按传进来的采样间隔换算，不按包级常量：同样 4 帧的片段，
// 2 秒间隔是 8 秒（判掉），4 秒间隔是 16 秒（放行）。
func TestMatchClipMinimumLengthFollowsGivenInterval(t *testing.T) {
	full, _, _ := syntheticClipFixture(t)

	if got := clipMinFrames(4000); got != 3 {
		t.Fatalf("10 秒按 4 秒一帧应为 3 帧下限，实际 %d", got)
	}
	if _, _, ok := MatchClip(full, full[600:604], clipFrameIntervalMS); ok {
		t.Fatal("2 秒间隔下 4 帧只有 8 秒，不该进候选")
	}
	offset, rate, ok := MatchClip(full, full[600:604], 4000)
	if !ok || offset != 600 || rate != 1 {
		t.Fatalf("4 秒间隔下 4 帧是 16 秒，应当进候选: offset=%d rate=%.3f ok=%v", offset, rate, ok)
	}
	// 间隔非法（0 或负）时一律不判为候选，而不是当成默认间隔。
	if _, _, ok := MatchClip(full, full[600:620], 0); ok {
		t.Fatal("采样间隔为 0 时不该给出结论")
	}
}

// 粗筛是剪枝，不能把真正的匹配剪掉：与暴力解逐一比对。
func TestMatchClipCoarsePruningAgreesWithBruteForce(t *testing.T) {
	full, clip, unrelated := syntheticClipFixture(t)

	for name, candidate := range map[string][]uint64{
		"截取片段":  clip,
		"无关序列":  unrelated,
		"片头 5%": full[:180],
		"片尾一段":  full[3400:],
	} {
		wantOffset, wantRate, wantOK := bruteForceMatchClip(full, candidate, clipFrameIntervalMS)
		gotOffset, gotRate, gotOK := MatchClip(full, candidate, clipFrameIntervalMS)
		if gotOK != wantOK || gotOffset != wantOffset || gotRate != wantRate {
			t.Fatalf("%s：粗筛结果与暴力解不一致 got=(%d,%.4f,%v) want=(%d,%.4f,%v)",
				name, gotOffset, gotRate, gotOK, wantOffset, wantRate, wantOK)
		}
	}
}

// 片头截取（设计 4.6.4 的"B 为 A 前 5% 片头"）：只要够 10 秒就照样识别，偏移是 0。
func TestMatchClipIdentifiesLeadingSegment(t *testing.T) {
	full, _, _ := syntheticClipFixture(t)

	offset, rate, ok := MatchClip(full, full[:180], clipFrameIntervalMS)
	if !ok || offset != 0 || rate != 1 {
		t.Fatalf("片头截取应识别为偏移 0、命中率 1: offset=%d rate=%.3f ok=%v", offset, rate, ok)
	}
}

// 命中率刚好跨过 0.70 这条线的两侧：阈值是判定的核心，不能只靠"随机序列不中"来间接覆盖。
func TestMatchClipHonorsMatchRateThreshold(t *testing.T) {
	source := rand.New(rand.NewSource(7))
	full := make([]uint64, 200)
	for index := range full {
		full[index] = source.Uint64()
	}
	// 20 帧的片段：14/20 = 0.70 刚好达标，13/20 = 0.65 不达标。
	// 被替换掉的帧用与 A 无关的随机值，等价于"这一帧没对上"。
	buildClip := func(mismatches int) []uint64 {
		clip := append([]uint64(nil), full[50:70]...)
		for index := 0; index < mismatches; index++ {
			clip[len(clip)-1-index] = source.Uint64()
		}
		return clip
	}
	if _, rate, ok := MatchClip(full, buildClip(6), clipFrameIntervalMS); !ok || rate < clipMatchRateThreshold {
		t.Fatalf("14/20 = 0.70 应当达标，实际 rate=%.3f ok=%v", rate, ok)
	}
	if _, rate, ok := MatchClip(full, buildClip(7), clipFrameIntervalMS); ok {
		t.Fatalf("13/20 = 0.65 不该达标，实际 rate=%.3f", rate)
	}
}

func TestFrameHashesRoundTripThroughBigEndianBlob(t *testing.T) {
	hashes := []uint64{0, 1, 0xFFFFFFFFFFFFFFFF, 0x0102030405060708}
	encoded := encodeFrameHashes(hashes)
	if len(encoded) != len(hashes)*8 {
		t.Fatalf("序列应是每帧 8 字节，实际 %d 字节 / %d 帧", len(encoded), len(hashes))
	}
	// 大端：0x0102030405060708 的首字节是 0x01。
	if encoded[24] != 0x01 || encoded[31] != 0x08 {
		t.Fatalf("应按大端存放，实际首尾字节 %#x %#x", encoded[24], encoded[31])
	}
	decoded := decodeFrameHashes(encoded)
	if len(decoded) != len(hashes) {
		t.Fatalf("读回帧数不一致: %d != %d", len(decoded), len(hashes))
	}
	for index := range hashes {
		if decoded[index] != hashes[index] {
			t.Fatalf("第 %d 帧读回不一致: %#x != %#x", index, decoded[index], hashes[index])
		}
	}
	// 被截断的行当成"没有序列"，不猜哪一半是好的。
	if decodeFrameHashes(encoded[:len(encoded)-3]) != nil {
		t.Fatal("字节数不是 8 的整数倍时应返回空序列")
	}
	if decodeFrameHashes(nil) != nil {
		t.Fatal("空字节应返回空序列")
	}
}
