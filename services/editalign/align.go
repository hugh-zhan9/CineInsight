package editalign

import (
	"context"
	"fmt"
	"math/bits"
	"slices"
)

func normalizeAlignOptions(opts AlignOptions) (AlignOptions, error) {
	if opts.AnchorStepMS < 0 || opts.MinSegmentMS < 0 || opts.MergeGapMS < 0 || opts.MaxHamming < 0 || opts.SecondBestMargin < 0 {
		return opts, fmt.Errorf("editalign: 对齐选项不能为负: %+v", opts)
	}
	if opts.AnchorStepMS == 0 {
		opts.AnchorStepMS = defaultAnchorStepMS
	}
	if opts.MinSegmentMS == 0 {
		opts.MinSegmentMS = defaultMinSegmentMS
	}
	if opts.MergeGapMS == 0 {
		opts.MergeGapMS = defaultMergeGapMS
	}
	if opts.MaxHamming == 0 {
		opts.MaxHamming = defaultMaxHamming
	}
	if opts.SecondBestMargin == 0 {
		opts.SecondBestMargin = defaultSecondBestMargin
	}
	return opts, nil
}

// anchorVote 是一个锚点窗口的结论：ok 时 HD 采样 hd 对应长版采样 long。
type anchorVote struct {
	ok       bool
	hd, long int
}

// pickAnchor 在 [lo, hi) 中选一个有信息量的采样作锚点：popcount 最接近 32（明暗最均衡），
// 平票取最早。没有有信息量的采样返回 -1。
func pickAnchor(hashes []uint64, lo, hi int) int {
	best, bestScore := -1, 65
	for i := lo; i < hi; i++ {
		if !Informative(hashes[i]) {
			continue
		}
		if score := absInt(bits.OnesCount64(hashes[i]) - 32); score < bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

// findAnchors 对每个锚点在长版全部有信息量的采样中暴力找最小汉明距离。最优须 ≤maxHamming，
// 且与最优位置相距超过一步的次优至少大 margin，否则作废（静止画面、重复场景不投票）。
// 内层循环只做异或+popcount 与一次字节写入，无分配。
func findAnchors(ctx context.Context, hd, long []uint64, window, maxHamming, margin int, report func(done, total int)) ([]anchorVote, error) {
	positions := make([]int, 0, len(long))
	values := make([]uint64, 0, len(long))
	for j, hash := range long {
		if Informative(hash) {
			positions = append(positions, j)
			values = append(values, hash)
		}
	}
	distances := make([]uint8, len(values))
	total := (len(hd) + window - 1) / window
	votes := make([]anchorVote, total)
	for k := range total {
		if k%progressEvery == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			report(k, total)
		}
		i := pickAnchor(hd, k*window, min(len(hd), (k+1)*window))
		if i < 0 || len(values) == 0 {
			continue
		}
		hash := hd[i]
		best, bestAt := 65, 0
		distances := distances[:len(values)]
		for q, value := range values {
			d := bits.OnesCount64(hash ^ value)
			distances[q] = uint8(d)
			if d < best {
				best, bestAt = d, q
			}
		}
		if best > maxHamming {
			continue
		}
		at := positions[bestAt]
		lo, hi := bestAt, bestAt
		for lo > 0 && positions[lo-1] >= at-1 {
			lo--
		}
		for hi+1 < len(positions) && positions[hi+1] <= at+1 {
			hi++
		}
		second := minDistance(distances[:lo], minDistance(distances[hi+1:], 65))
		if second-best < margin {
			continue
		}
		votes[k] = anchorVote{ok: true, hd: i, long: at}
	}
	return votes, nil
}

func minDistance(distances []uint8, floor int) int {
	smallest := floor
	for _, d := range distances {
		if int(d) < smallest {
			smallest = int(d)
		}
	}
	return smallest
}

// alignChain 是一串偏移一致的锚点及其外扩结果（均为 HD 采样下标，含两端）。
type alignChain struct {
	anchorFirst, anchorLast int
	offsets                 []int
	offset                  int
	hdStart, hdEnd          int
}

// chainAnchors 把相邻且偏移一致（与链首相差 ≤1 步）的锚点连成链；作废的锚点打断链，
// 断开处由外扩与合并接回。
func chainAnchors(votes []anchorVote) []alignChain {
	var chains []alignChain
	open := false
	for _, vote := range votes {
		if !vote.ok {
			open = false
			continue
		}
		offset := vote.long - vote.hd
		if open {
			last := &chains[len(chains)-1]
			if absInt(offset-last.offsets[0]) <= 1 {
				last.anchorLast = vote.hd
				last.offsets = append(last.offsets, offset)
				continue
			}
		}
		chains = append(chains, alignChain{anchorFirst: vote.hd, anchorLast: vote.hd, offsets: []int{offset}})
		open = true
	}
	for i := range chains {
		chains[i].offset = medianInt(chains[i].offsets)
		chains[i].hdStart, chains[i].hdEnd = chains[i].anchorFirst, chains[i].anchorLast
	}
	return chains
}

// segmentMatcher 判定 HD 采样 k 在偏移 o（容差 ±1 步）下与长版是否相同画面。
type segmentMatcher struct {
	hd, long       []uint64
	hdInf, longInf []bool
	maxHamming     int
}

func (m segmentMatcher) classify(k, offset int) sampleClass {
	if m.hdInf[k] {
		for _, delta := range [3]int{0, -1, 1} {
			j := k + offset + delta
			if j >= 0 && j < len(m.long) && m.longInf[j] && hamming(m.hd[k], m.long[j]) <= m.maxHamming {
				return classMatch
			}
		}
		return classMismatch
	}
	if j := k + offset; j >= 0 && j < len(m.long) && !m.longInf[j] {
		return classNeutral
	}
	return classMismatch
}

// extendChains 按序列精度向两侧外扩每条链，但不越过相邻链的锚点。
func extendChains(chains []alignChain, matcher segmentMatcher, limits runLimits) {
	for c := range chains {
		lo, hi := -1, len(matcher.hd)
		if c > 0 {
			lo = chains[c-1].anchorLast
		}
		if c+1 < len(chains) {
			hi = chains[c+1].anchorFirst
		}
		offset := chains[c].offset
		classify := func(k int) sampleClass { return matcher.classify(k, offset) }
		chains[c].hdStart = extendRun(chains[c].anchorFirst, -1, lo, hi, limits, classify)
		chains[c].hdEnd = extendRun(chains[c].anchorLast, +1, lo, hi, limits, classify)
	}
}

// mergeChains 合并相邻、偏移一致（±1 步）且间隔 ≤ gapSamples 个采样的链。
func mergeChains(chains []alignChain, gapSamples int) []alignChain {
	var merged []alignChain
	for _, chain := range chains {
		if n := len(merged); n > 0 {
			last := &merged[n-1]
			if absInt(last.offset-chain.offset) <= 1 && chain.hdStart-last.hdEnd-1 <= gapSamples {
				last.hdEnd = max(last.hdEnd, chain.hdEnd)
				last.anchorLast = chain.anchorLast
				last.offsets = append(last.offsets, chain.offsets...)
				last.offset = medianInt(last.offsets)
				continue
			}
		}
		merged = append(merged, chain)
	}
	return merged
}

// dropShort 丢弃外扩后仍短于 minSamples 个采样的链。
func dropShort(chains []alignChain, minSamples int) []alignChain {
	kept := chains[:0]
	for _, chain := range chains {
		if chain.hdEnd-chain.hdStart+1 >= minSamples {
			kept = append(kept, chain)
		}
	}
	return kept
}

// matchRate 是链区间内命中采样占全部采样的比例（低信息帧不计命中但留在分母）。
func (m segmentMatcher) matchRate(chain alignChain) float64 {
	hits := 0
	for k := chain.hdStart; k <= chain.hdEnd; k++ {
		if m.classify(k, chain.offset) == classMatch {
			hits++
		}
	}
	return float64(hits) / float64(chain.hdEnd-chain.hdStart+1)
}

// alignSegments 实现 AlignSegments：锚点投票 → 同偏移成链 → 序列精度外扩 → 合并近邻同偏移段 →
// 丢弃过短段 → 帧级细化 → 长版区间冲突判定。进度在锚点阶段按锚点数回调，全部完成后报 total/total。
func alignSegments(ctx context.Context, long, hd Source, opts AlignOptions, progress func(done, total int)) ([]AlignedSegment, error) {
	opts, err := normalizeAlignOptions(opts)
	if err != nil {
		return nil, err
	}
	step := hd.Hashes.StepMS
	if step <= 0 || long.Hashes.StepMS != step {
		return nil, fmt.Errorf("editalign: 两侧采样间隔不一致或非法（长版 %dms，高清 %dms）", long.Hashes.StepMS, step)
	}
	report := func(done, total int) {
		if progress != nil && total > 0 {
			progress(done, total)
		}
	}
	hdHashes, longHashes := hd.Hashes.Hashes, long.Hashes.Hashes
	if len(hdHashes) == 0 || len(longHashes) == 0 {
		return []AlignedSegment{}, ctx.Err()
	}
	window := max(1, int(roundDiv(opts.AnchorStepMS, step)))
	votes, err := findAnchors(ctx, hdHashes, longHashes, window, opts.MaxHamming, opts.SecondBestMargin, report)
	if err != nil {
		return nil, err
	}
	matcher := segmentMatcher{
		hd: hdHashes, long: longHashes,
		hdInf: informativeFlags(hdHashes), longInf: informativeFlags(longHashes),
		maxHamming: opts.MaxHamming,
	}
	chains := chainAnchors(votes)
	extendChains(chains, matcher, newRunLimits(step))
	gapSamples := int(opts.MergeGapMS / step)
	minSamples := int((opts.MinSegmentMS + step - 1) / step)
	// 合并→丢弃做两轮：夹在两段同偏移链之间的短误配链被丢弃后，两侧才相邻可合并。
	chains = dropShort(mergeChains(dropShort(mergeChains(chains, gapSamples), minSamples), gapSamples), minSamples)

	hdCache, longCache := newFrameCache(hd.Frames), newFrameCache(long.Frames)
	segments := make([]AlignedSegment, 0, len(chains))
	for _, chain := range chains {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		est := spanEstimate{
			aStart:    hd.Hashes.StartMS + int64(chain.hdStart)*step,
			aEnd:      hd.Hashes.StartMS + int64(chain.hdEnd+1)*step,
			offCenter: long.Hashes.StartMS - hd.Hashes.StartMS + int64(chain.offset)*step,
			offRadius: step + step/2,
			stepMS:    step,
		}
		refined, err := refineSpan(ctx, hdCache, longCache, est)
		if err != nil {
			return nil, err
		}
		segment, ok := clampSegment(AlignedSegment{
			HDStartMS: refined.aStart, HDEndMS: refined.aEnd,
			LongStartMS: refined.aStart + refined.offset, LongEndMS: refined.aEnd + refined.offset,
			MatchRate: matcher.matchRate(chain), Status: SegmentMatched,
		}, hd.DurationMS, long.DurationMS)
		if ok {
			segments = append(segments, segment)
		}
	}
	resolveConflicts(segments, step)
	slices.SortStableFunc(segments, func(a, b AlignedSegment) int {
		if c := compareInt64(a.LongStartMS, b.LongStartMS); c != 0 {
			return c
		}
		return compareInt64(a.HDStartMS, b.HDStartMS)
	})
	report(len(votes), len(votes))
	return segments, nil
}

// clampSegment 把段夹进两侧时长（未知时长≤0 不夹上界），两侧同步伸缩以保持等长。
func clampSegment(segment AlignedSegment, hdDuration, longDuration int64) (AlignedSegment, bool) {
	if lead := max(-segment.HDStartMS, -segment.LongStartMS, 0); lead > 0 {
		segment.HDStartMS += lead
		segment.LongStartMS += lead
	}
	overrun := int64(0)
	if hdDuration > 0 {
		overrun = max(overrun, segment.HDEndMS-hdDuration)
	}
	if longDuration > 0 {
		overrun = max(overrun, segment.LongEndMS-longDuration)
	}
	segment.HDEndMS -= overrun
	segment.LongEndMS -= overrun
	return segment, segment.HDEndMS > segment.HDStartMS
}

// resolveConflicts 按匹配率高、时长长、长版靠前的次序逐段认领长版区间：与已认领段重叠
// 不超过 tolerance（帧级边界的一两帧含糊）时把后者两侧同步裁到相接；重叠更多则标 conflict。
func resolveConflicts(segments []AlignedSegment, tolerance int64) {
	order := make([]int, len(segments))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		sa, sb := segments[a], segments[b]
		if sa.MatchRate != sb.MatchRate {
			if sa.MatchRate > sb.MatchRate {
				return -1
			}
			return 1
		}
		if c := compareInt64(sb.LongEndMS-sb.LongStartMS, sa.LongEndMS-sa.LongStartMS); c != 0 {
			return c
		}
		return compareInt64(sa.LongStartMS, sb.LongStartMS)
	})
	var claimed []int
	for _, i := range order {
		candidate, conflict := segments[i], false
		for _, j := range claimed {
			other := segments[j]
			overlap := min(candidate.LongEndMS, other.LongEndMS) - max(candidate.LongStartMS, other.LongStartMS)
			if overlap <= 0 {
				continue
			}
			if overlap > tolerance || candidate.LongEndMS-candidate.LongStartMS <= overlap {
				conflict = true
				break
			}
			if candidate.LongStartMS < other.LongStartMS {
				candidate.LongEndMS -= overlap
				candidate.HDEndMS -= overlap
			} else {
				candidate.LongStartMS += overlap
				candidate.HDStartMS += overlap
			}
		}
		if conflict {
			segments[i].Status = SegmentConflict
			continue
		}
		candidate.Status = SegmentMatched
		segments[i] = candidate
		claimed = append(claimed, i)
	}
}
