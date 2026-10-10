package editalign

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// introRun 是两条片头序列间一段恒定偏移的连续匹配。a 侧区间 [aStart, aEnd]（采样下标，含），
// b 侧下标约为 a 下标 + diag：比较时同时看 b[i+diag] 与 b[i+diag+1]，吸收两片采样网格
// 不足一步的相位差，所以真实偏移落在 [diag, diag+1] 步之间。
type introRun struct {
	aStart, aEnd int
	diag         int
	matched      int
	neutral      int
}

func (r introRun) span() int   { return r.aEnd - r.aStart + 1 }
func (r introRun) bStart() int { return r.aStart + r.diag }
func (r introRun) bEnd() int   { return r.aEnd + r.diag + 1 }

// flipped 把 a/b 互换：b 下标 j∈{i+d, i+d+1} 等价于 i∈{j-d-1, j-d}，即新 diag=-d-1。
func (r introRun) flipped() introRun {
	return introRun{aStart: r.bStart(), aEnd: r.aEnd + r.diag, diag: -r.diag - 1, matched: r.matched, neutral: r.neutral}
}

// better 给出确定的优劣次序：命中多、跨度长、偏移小、位置早。
func (r introRun) better(o introRun) bool {
	if r.matched != o.matched {
		return r.matched > o.matched
	}
	if r.span() != o.span() {
		return r.span() > o.span()
	}
	if absInt(r.diag) != absInt(o.diag) {
		return absInt(r.diag) < absInt(o.diag)
	}
	if r.aStart != o.aStart {
		return r.aStart < o.aStart
	}
	return r.diag < o.diag
}

// disjoint：两段在任一侧不重叠即视为不同位置（同一处片头在相邻偏移上的重复命中不算）。
func (r introRun) disjoint(o introRun) bool {
	return r.aEnd < o.aStart || o.aEnd < r.aStart || r.bEnd() < o.bStart() || o.bEnd() < r.bStart()
}

func normalizeIntroOptions(opts IntroOptions) (IntroOptions, error) {
	if opts.MinDurationMS < 0 || opts.MaxHamming < 0 {
		return opts, fmt.Errorf("editalign: 片头选项不能为负: %+v", opts)
	}
	if opts.MinDurationMS == 0 {
		opts.MinDurationMS = defaultIntroMinDurationMS
	}
	if opts.MaxHamming == 0 {
		opts.MaxHamming = defaultMaxHamming
	}
	return opts, nil
}

// introSearch 是一轮片头识别共用的参数。
type introSearch struct {
	maxHamming  int
	limits      runLimits
	minSamples  int // 跨度至少这么多采样（MinDurationMS）
	minEvidence int // 命中至少占最短跨度的一半，黑场撑不起一段片头
}

func (s introSearch) qualifies(st runStats) bool {
	return st.span() >= s.minSamples && st.matched >= s.minEvidence && st.matched*2 >= st.span()-st.neutral
}

func informativeFlags(hashes []uint64) []bool {
	flags := make([]bool, len(hashes))
	for i, hash := range hashes {
		flags[i] = Informative(hash)
	}
	return flags
}

// findIntroRuns 沿每条对角线（恒定偏移）找出全部合格的连续匹配。
// 复杂度 O(len(a)·len(b))，10 分钟×4fps 的两条序列约 600 万次比较。
func findIntroRuns(ctx context.Context, a, b []uint64, aInf, bInf []bool, search introSearch) ([]introRun, error) {
	n, m := len(a), len(b)
	var runs []introRun
	tracker := runTracker{limits: search.limits}
	emit := func(diag int, st runStats) {
		if search.qualifies(st) {
			runs = append(runs, introRun{aStart: st.first, aEnd: st.last, diag: diag, matched: st.matched, neutral: st.neutral})
		}
	}
	for diag := -(n - 1); diag <= m-1; diag++ {
		if diag&127 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		lo, hi := max(0, -diag), min(n, m-diag)
		if hi-lo < search.minSamples {
			continue
		}
		for i := lo; i < hi; i++ {
			j := i + diag
			class := classMismatch
			if aInf[i] {
				if (bInf[j] && hamming(a[i], b[j]) <= search.maxHamming) ||
					(j+1 < m && bInf[j+1] && hamming(a[i], b[j+1]) <= search.maxHamming) {
					class = classMatch
				}
			} else if !bInf[j] {
				class = classNeutral
			}
			if st, closed := tracker.push(i, class); closed {
				emit(diag, st)
			}
		}
		if st, closed := tracker.flush(); closed {
			emit(diag, st)
		}
	}
	return runs, nil
}

// bestIntroRun 选最优候选；与它偏移不同（相差超过 1 步）、位置不相交、证据达到 90% 的
// 另一候选存在时判为有歧义。同一偏移上被断开的几截是同一处片头，不构成歧义。
func bestIntroRun(runs []introRun) (best introRun, ok, ambiguous bool) {
	if len(runs) == 0 {
		return introRun{}, false, false
	}
	best = runs[0]
	for _, run := range runs[1:] {
		if run.better(best) {
			best = run
		}
	}
	for _, run := range runs {
		if run != best && absInt(run.diag-best.diag) > 1 &&
			run.matched*100 >= best.matched*introAmbiguityPercent && run.disjoint(best) {
			return best, true, true
		}
	}
	return best, true, false
}

// overlapping 只保留 a 侧区间与 [start, end] 相交的候选。
func overlapping(runs []introRun, start, end int) []introRun {
	var kept []introRun
	for _, run := range runs {
		if run.aStart <= end && start <= run.aEnd {
			kept = append(kept, run)
		}
	}
	return kept
}

func flipAll(runs []introRun) []introRun {
	flipped := make([]introRun, len(runs))
	for i, run := range runs {
		flipped[i] = run.flipped()
	}
	return flipped
}

func medianInt(values []int) int {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[(len(sorted)-1)/2]
}

var errTooFewSources = errors.New("editalign: 片头识别至少需要 2 个来源")

// detectIntros 实现 DetectIntros：两两找候选 → 选参照 → 以参照侧中位区间为共识筛候选 →
// 每项取与参照的匹配段 → 两端帧级细化。任何一步拿不准都标 undetected/ambiguous，不猜。
func detectIntros(ctx context.Context, sources []Source, opts IntroOptions) ([]IntroResult, error) {
	if len(sources) < 2 {
		return nil, errTooFewSources
	}
	opts, err := normalizeIntroOptions(opts)
	if err != nil {
		return nil, err
	}
	step := sources[0].Hashes.StepMS
	for _, source := range sources {
		if source.Hashes.StepMS <= 0 || source.Hashes.StepMS != step {
			return nil, fmt.Errorf("editalign: 来源 %d 的采样间隔 %dms 与其他来源不一致或非法", source.ID, source.Hashes.StepMS)
		}
	}
	minSamples := int((opts.MinDurationMS + step - 1) / step)
	search := introSearch{maxHamming: opts.MaxHamming, limits: newRunLimits(step), minSamples: minSamples, minEvidence: (minSamples + 1) / 2}

	n := len(sources)
	flags := make([][]bool, n)
	for i, source := range sources {
		flags[i] = informativeFlags(source.Hashes.Hashes)
	}
	runs := make([][][]introRun, n) // runs[i][j]：以 i 为 a 侧的全部合格候选
	for i := range runs {
		runs[i] = make([][]introRun, n)
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			found, err := findIntroRuns(ctx, sources[i].Hashes.Hashes, sources[j].Hashes.Hashes, flags[i], flags[j], search)
			if err != nil {
				return nil, err
			}
			runs[i][j], runs[j][i] = found, flipAll(found)
		}
	}

	plan := planIntros(runs)
	results := make([]IntroResult, n)
	caches := make([]*frameCache, n)
	for i, source := range sources {
		caches[i] = newFrameCache(source.Frames)
		results[i] = IntroResult{SourceID: source.ID, Status: plan.status[i]}
	}
	for s := range sources {
		if plan.status[s] != IntroDetected {
			continue
		}
		run, other := plan.run[s], plan.partner[s]
		est := spanEstimate{
			aStart:    sources[s].Hashes.StartMS + int64(run.aStart)*step,
			aEnd:      sources[s].Hashes.StartMS + int64(run.aEnd+1)*step,
			offCenter: sources[other].Hashes.StartMS - sources[s].Hashes.StartMS + int64(run.diag)*step + step/2,
			offRadius: step + step/2,
			stepMS:    step,
		}
		refined, err := refineSpan(ctx, caches[s], caches[other], est)
		if err != nil {
			return nil, err
		}
		start, end := clampRange(refined.aStart, refined.aEnd, sources[s].DurationMS)
		results[s].StartMS, results[s].EndMS = start, end
		results[s].MatchRate = float64(run.matched) / float64(run.span())
		results[s].MatchedWith = plan.matchedWith(s)
	}
	return results, nil
}

// clampRange 把区间夹到 [0, duration]（duration≤0 表示未知，只夹下界）。
func clampRange(start, end, duration int64) (int64, int64) {
	start = max(start, 0)
	if duration > 0 {
		end = min(end, duration)
	}
	return start, max(end, start)
}

// introPlan 是序列层面的片头结论：每项状态、以该项为 a 侧的匹配段及其比较对象。
type introPlan struct {
	runs    [][][]introRun
	status  []IntroStatus
	run     []introRun
	partner []int
}

// planIntros 选参照（无歧义匹配的其他项最多；平票取总命中多、下标小者），以参照侧
// 各最优候选的中位区间为共识，只接受与共识相交的候选——别处的共享画面（前情提要等）
// 即使更长也不会被当成片头。参照自身取区间最接近共识的那一对。
func planIntros(runs [][][]introRun) introPlan {
	n := len(runs)
	plan := introPlan{runs: runs, status: make([]IntroStatus, n), run: make([]introRun, n), partner: make([]int, n)}
	partners, evidence := make([]int, n), make([]int, n)
	anyAmbiguous := make([]bool, n)
	for i := range n {
		plan.status[i] = IntroUndetected
		for j := range n {
			if i == j {
				continue
			}
			best, ok, ambiguous := bestIntroRun(runs[i][j])
			if ok && ambiguous {
				anyAmbiguous[i] = true
			} else if ok {
				partners[i]++
				evidence[i] += best.matched
			}
		}
	}
	ref := 0
	for i := 1; i < n; i++ {
		if partners[i] > partners[ref] || (partners[i] == partners[ref] && evidence[i] > evidence[ref]) {
			ref = i
		}
	}
	if partners[ref] == 0 {
		for i := range n {
			if anyAmbiguous[i] {
				plan.status[i] = IntroAmbiguous
			}
		}
		return plan
	}
	var starts, ends []int
	for p := range n {
		if best, ok, ambiguous := bestIntroRun(runs[ref][p]); p != ref && ok && !ambiguous {
			starts, ends = append(starts, best.aStart), append(ends, best.aEnd)
		}
	}
	consensusStart, consensusEnd := medianInt(starts), medianInt(ends)
	refPartner, refDistance, refAmbiguous := -1, 0, false
	var refRun introRun
	for p := range n {
		if p == ref {
			continue
		}
		best, ok, ambiguous := bestIntroRun(overlapping(runs[ref][p], consensusStart, consensusEnd))
		switch {
		case !ok:
			continue
		case ambiguous:
			plan.status[p], refAmbiguous = IntroAmbiguous, true
			continue
		}
		plan.status[p], plan.run[p], plan.partner[p] = IntroDetected, best.flipped(), ref
		distance := absInt(best.aStart-consensusStart) + absInt(best.aEnd-consensusEnd)
		if refPartner < 0 || distance < refDistance {
			refPartner, refDistance, refRun = p, distance, best
		}
	}
	switch {
	case refPartner >= 0:
		plan.status[ref], plan.run[ref], plan.partner[ref] = IntroDetected, refRun, refPartner
	case refAmbiguous:
		plan.status[ref] = IntroAmbiguous
	}
	return plan
}

// matchedWith 统计与 s 的片头区间存在合格候选的其他来源数。
func (p introPlan) matchedWith(s int) int {
	count := 0
	for q := range p.runs {
		if q != s && len(overlapping(p.runs[s][q], p.run[s].aStart, p.run[s].aEnd)) > 0 {
			count++
		}
	}
	return count
}
