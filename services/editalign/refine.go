package editalign

import (
	"context"
	"fmt"
	"slices"
	"sort"
)

// frameCache 包一层 GrayFrameReader：窗口向外取整到 500ms 网格再读，已读窗口能覆盖的
// 请求直接切片返回。参照片的同一处边界会被每个比较对象各读一次，缓存让它只读一次。
type frameCache struct {
	read    GrayFrameReader
	windows []frameWindow
}

type frameWindow struct {
	start, end int64
	frames     []GrayFrame
}

func newFrameCache(read GrayFrameReader) *frameCache {
	if read == nil {
		return nil
	}
	return &frameCache{read: read}
}

// window 返回 PTS 落在 [startMS, endMS) 的帧（升序）。
func (c *frameCache) window(ctx context.Context, startMS, endMS int64) ([]GrayFrame, error) {
	startMS = max(startMS, 0)
	if endMS <= startMS {
		return nil, nil
	}
	for _, cached := range c.windows {
		if cached.start <= startMS && endMS <= cached.end {
			return framesBetween(cached.frames, startMS, endMS), nil
		}
	}
	readStart := floorDiv(startMS, refineCacheGridMS) * refineCacheGridMS
	readEnd := -floorDiv(-endMS, refineCacheGridMS) * refineCacheGridMS
	frames, err := c.read(ctx, readStart, readEnd-readStart)
	if err != nil {
		return nil, err
	}
	if !slices.IsSortedFunc(frames, func(a, b GrayFrame) int { return compareInt64(a.PTSMS, b.PTSMS) }) {
		frames = slices.Clone(frames)
		slices.SortStableFunc(frames, func(a, b GrayFrame) int { return compareInt64(a.PTSMS, b.PTSMS) })
	}
	if len(c.windows) >= refineCacheWindows {
		c.windows = c.windows[1:]
	}
	c.windows = append(c.windows, frameWindow{start: readStart, end: readEnd, frames: frames})
	return framesBetween(frames, startMS, endMS), nil
}

func framesBetween(frames []GrayFrame, startMS, endMS int64) []GrayFrame {
	lo := sort.Search(len(frames), func(i int) bool { return frames[i].PTSMS >= startMS })
	hi := sort.Search(len(frames), func(i int) bool { return frames[i].PTSMS >= endMS })
	return frames[lo:hi]
}

// meanAbsDiff 是两帧小灰度图的平均绝对差（0–255）。
func meanAbsDiff(a, b []byte) (float64, error) {
	if len(a) != len(b) || len(a) == 0 {
		return 0, fmt.Errorf("editalign: 细化帧尺寸不一致（%d vs %d 字节）", len(a), len(b))
	}
	total := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		total += d
	}
	return float64(total) / float64(len(a)), nil
}

// nearestFrame 返回 PTS 最接近 target 且相差不超过 tolerance 的帧下标，没有则 -1。
func nearestFrame(frames []GrayFrame, target, tolerance int64) int {
	i := sort.Search(len(frames), func(i int) bool { return frames[i].PTSMS >= target })
	best, bestDiff := -1, tolerance+1
	for _, k := range [2]int{i - 1, i} {
		if k >= 0 && k < len(frames) {
			if diff := absInt64(frames[k].PTSMS - target); diff < bestDiff {
				best, bestDiff = k, diff
			}
		}
	}
	return best
}

// frameInterval 是窗口内相邻帧间隔的中位数；帧太少时按 40ms（25fps）估。
func frameInterval(frames []GrayFrame) int64 {
	if len(frames) < 2 {
		return 40
	}
	gaps := make([]int64, 0, len(frames)-1)
	for i := 1; i < len(frames); i++ {
		if gap := frames[i].PTSMS - frames[i-1].PTSMS; gap > 0 {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) == 0 {
		return 40
	}
	slices.Sort(gaps)
	return gaps[(len(gaps)-1)/2]
}

// spanEstimate 是序列层面的粗结果：a 侧 [aStart, aEnd)（aStart 为首个命中采样时间，
// aEnd 为末个命中采样时间+一步），b 时间 ≈ a 时间 + offset，offset 在 offCenter±offRadius 内。
type spanEstimate struct {
	aStart, aEnd         int64
	offCenter, offRadius int64
	stepMS               int64
}

// refinedSpan 是帧级细化结果；refined=false 表示无法细化，沿用序列精度。
type refinedSpan struct {
	aStart, aEnd, offset int64
	refined              bool
}

// refineSpan 把粗区间细化到帧：先用两端内部帧的平均绝对差在 ±offRadius 内选定唯一偏移
// （两端合算，静止的一端由另一端定），再在两端 ±1 秒内按"失配→匹配"变点定位首/末匹配帧。
// a 或 b 没有读取器时原样返回粗结果。
func refineSpan(ctx context.Context, a, b *frameCache, est spanEstimate) (refinedSpan, error) {
	coarse := refinedSpan{aStart: est.aStart, aEnd: est.aEnd, offset: est.offCenter}
	if a == nil || b == nil || est.aEnd <= est.aStart {
		return coarse, nil
	}
	const window = int64(refineWindowMS)
	offLo, offHi := est.offCenter-est.offRadius, est.offCenter+est.offRadius
	var aHead, aTail, bHead, bTail []GrayFrame
	for _, read := range []struct {
		cache      *frameCache
		start, end int64
		into       *[]GrayFrame
	}{
		{a, est.aStart - window, est.aStart + window, &aHead},
		{a, est.aEnd - window, est.aEnd + window, &aTail},
		{b, est.aStart + offLo - window, est.aStart + offHi + window, &bHead},
		{b, est.aEnd + offLo - window, est.aEnd + offHi + window, &bTail},
	} {
		frames, err := read.cache.window(ctx, read.start, read.end)
		if err != nil {
			return refinedSpan{}, err
		}
		*read.into = frames
	}
	// 内部帧取粗区间两端各 1 秒（含粗边界那一步）：越靠近边界越能区分相差几帧的偏移，
	// 粗边界外侧混进来的几帧对每个候选偏移都是同样的失配，不改变排序。
	lastSample := est.aEnd - est.stepMS
	headInterior := framesBetween(aHead, est.aStart, min(est.aStart+window, est.aEnd))
	tailInterior := framesBetween(aTail, max(est.aEnd-window, est.aStart), est.aEnd)
	tolerance := frameInterval(slices.Concat(bHead, bTail))/2 + 1
	if len(bHead) >= 2 {
		tolerance = frameInterval(bHead)/2 + 1
	}

	offsets := slices.Concat(candidateOffsets(headInterior, bHead, offLo, offHi), candidateOffsets(tailInterior, bTail, offLo, offHi))
	slices.Sort(offsets)
	offsets = slices.Compact(offsets)
	bestOffset, bestCost, found := int64(0), 0.0, false
	required := max(3, (len(headInterior)+len(tailInterior))/2)
	for _, offset := range offsets {
		headDiffs, err := interiorDiffs(headInterior, bHead, offset, tolerance)
		if err != nil {
			return refinedSpan{}, err
		}
		tailDiffs, err := interiorDiffs(tailInterior, bTail, offset, tolerance)
		if err != nil {
			return refinedSpan{}, err
		}
		diffs := slices.Concat(headDiffs, tailDiffs)
		if len(diffs) < required {
			continue
		}
		cost := mean(diffs)
		if !found || cost < bestCost || (cost == bestCost && absInt64(offset-est.offCenter) < absInt64(bestOffset-est.offCenter)) {
			bestOffset, bestCost, found = offset, cost, true
		}
	}
	if !found {
		return coarse, nil
	}
	headDiffs, _ := interiorDiffs(headInterior, bHead, bestOffset, tolerance)
	tailDiffs, _ := interiorDiffs(tailInterior, bTail, bestOffset, tolerance)
	threshold := madThreshold(slices.Concat(headDiffs, tailDiffs))

	result := refinedSpan{aStart: est.aStart, aEnd: est.aEnd, offset: bestOffset, refined: true}
	headFrames := framesBetween(aHead, est.aStart-window, lastSample+1)
	headLabels, err := labelFrames(headFrames, bHead, bestOffset, tolerance, threshold)
	if err != nil {
		return refinedSpan{}, err
	}
	if k := startChangePoint(headLabels); k < len(headFrames) {
		result.aStart = headFrames[k].PTSMS
	}
	tailFrames := framesBetween(aTail, est.aStart, est.aEnd+window)
	tailLabels, err := labelFrames(tailFrames, bTail, bestOffset, tolerance, threshold)
	if err != nil {
		return refinedSpan{}, err
	}
	switch k := endChangePoint(tailLabels); {
	case k == 0:
	case k < len(tailFrames):
		result.aEnd = tailFrames[k].PTSMS
	default:
		result.aEnd = tailFrames[len(tailFrames)-1].PTSMS + frameInterval(tailFrames)
	}
	if result.aEnd <= result.aStart {
		return refinedSpan{aStart: est.aStart, aEnd: est.aEnd, offset: bestOffset, refined: false}, nil
	}
	return result, nil
}

// candidateOffsets 以内部中间一帧为基准，列出把它对到 b 每一帧所需、落在 [lo, hi] 内的偏移。
func candidateOffsets(interior, bFrames []GrayFrame, lo, hi int64) []int64 {
	if len(interior) == 0 {
		return nil
	}
	pivot := interior[len(interior)/2].PTSMS
	var offsets []int64
	for _, frame := range bFrames {
		if offset := frame.PTSMS - pivot; offset >= lo && offset <= hi {
			offsets = append(offsets, offset)
		}
	}
	return offsets
}

// interiorDiffs 计算 a 侧每帧与 b 侧对应帧（a.PTS+offset 最近帧）的平均绝对差；对不上的帧跳过。
func interiorDiffs(aFrames, bFrames []GrayFrame, offset, tolerance int64) ([]float64, error) {
	diffs := make([]float64, 0, len(aFrames))
	for _, frame := range aFrames {
		k := nearestFrame(bFrames, frame.PTSMS+offset, tolerance)
		if k < 0 {
			continue
		}
		diff, err := meanAbsDiff(frame.Pixels, bFrames[k].Pixels)
		if err != nil {
			return nil, err
		}
		diffs = append(diffs, diff)
	}
	return diffs, nil
}

func mean(values []float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

// madThreshold：同一画面判定阈值 = 2×内部帧差 90 分位 + 3，夹在 [8, 20]。
// 内部帧差反映这对来源的转码/缩放噪声；夹取防止噪声大时把不同画面也判成相同。
func madThreshold(diffs []float64) float64 {
	if len(diffs) == 0 {
		return madThresholdMin
	}
	sorted := slices.Clone(diffs)
	slices.Sort(sorted)
	p90 := sorted[(len(sorted)-1)*9/10]
	return min(max(2*p90+3, madThresholdMin), madThresholdMax)
}

// labelFrames 标记 a 侧每帧是否与 b 侧对应帧相同；b 侧没有对应帧（越界、片尾）记为不同。
// 两侧都是近乎均匀的画面（黑场、白场）时不算相同：与序列层"低信息帧不计票"同口径，
// 边界落在最后/最先一帧有内容的匹配帧上，而不是随窗口长度滑进黑场。
func labelFrames(aFrames, bFrames []GrayFrame, offset, tolerance int64, threshold float64) ([]bool, error) {
	labels := make([]bool, len(aFrames))
	for i, frame := range aFrames {
		k := nearestFrame(bFrames, frame.PTSMS+offset, tolerance)
		if k < 0 {
			continue
		}
		diff, err := meanAbsDiff(frame.Pixels, bFrames[k].Pixels)
		if err != nil {
			return nil, err
		}
		labels[i] = diff <= threshold && !(uniformFrame(frame.Pixels) && uniformFrame(bFrames[k].Pixels))
	}
	return labels, nil
}

// uniformFrame 判断小灰度图是否近乎均匀：像素相对均值的平均绝对偏差低于 frameUniformDeviation。
func uniformFrame(pixels []byte) bool {
	if len(pixels) == 0 {
		return true
	}
	total := 0
	for _, p := range pixels {
		total += int(p)
	}
	mean := float64(total) / float64(len(pixels))
	deviation := 0.0
	for _, p := range pixels {
		d := float64(p) - mean
		if d < 0 {
			d = -d
		}
		deviation += d
	}
	return deviation/float64(len(pixels)) < frameUniformDeviation
}

// startChangePoint 选 k 使"k 之前失配数 + k 起匹配数"最大（平票取最早），即首个匹配帧；
// 全部失配时返回 len(labels)。用计数而不是逐帧回溯，单帧噪声不会把边界拖走。
func startChangePoint(labels []bool) int {
	matchesAfter := 0
	for _, label := range labels {
		if label {
			matchesAfter++
		}
	}
	if matchesAfter == 0 {
		return len(labels)
	}
	best, bestScore, mismatchesBefore := 0, matchesAfter, 0
	for k, label := range labels {
		if label {
			matchesAfter--
		} else {
			mismatchesBefore++
		}
		if score := mismatchesBefore + matchesAfter; score > bestScore {
			best, bestScore = k+1, score
		}
	}
	return best
}

// endChangePoint 选 k 使"k 之前匹配数 + k 起失配数"最大（平票取最晚），即末个匹配帧之后
// 的第一帧；全部失配时返回 0。
func endChangePoint(labels []bool) int {
	mismatchesAfter, matchesBefore := 0, 0
	for _, label := range labels {
		if !label {
			mismatchesAfter++
		}
	}
	if mismatchesAfter == len(labels) {
		return 0
	}
	best, bestScore := 0, mismatchesAfter
	for k, label := range labels {
		if label {
			matchesBefore++
		} else {
			mismatchesAfter--
		}
		if score := matchesBefore + mismatchesAfter; score >= bestScore {
			best, bestScore = k+1, score
		}
	}
	return best
}
