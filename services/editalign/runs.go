package editalign

// sampleClass 是两条序列在某个对齐位置上的判定。
type sampleClass uint8

const (
	classMismatch sampleClass = iota
	// classMatch：两侧都有信息量且汉明距离在容差内，计一票。
	classMatch
	// classNeutral：两侧都是低信息帧（黑场等），不计票也不立即打断。
	classNeutral
)

// runLimits 把毫秒容差换算成采样数。
type runLimits struct {
	maxMismatch int
	maxNeutral  int
}

func newRunLimits(stepMS int64) runLimits {
	return runLimits{
		maxMismatch: max(1, int(runMaxMismatchMS/stepMS)),
		maxNeutral:  max(1, int(runMaxNeutralMS/stepMS)),
	}
}

// runStats 是一段连续匹配：first/last 是首末命中位置（含），neutral 是夹在其中的低信息位置数。
type runStats struct {
	first, last int
	matched     int
	neutral     int
}

func (r runStats) span() int { return r.last - r.first + 1 }

// runTracker 沿一条对角线（恒定偏移）累计连续匹配。两次命中之间失配超过 maxMismatch
// 或低信息位置超过 maxNeutral 即断开；区间两端总是命中位置。
type runTracker struct {
	limits         runLimits
	active         bool
	current        runStats
	pendingNeutral int
	mismatchSince  int
}

// push 记录位置 pos 的判定；若此前的一段因此结束，返回它。
func (t *runTracker) push(pos int, class sampleClass) (runStats, bool) {
	switch class {
	case classMatch:
		if !t.active {
			t.active = true
			t.current = runStats{first: pos}
		} else {
			t.current.neutral += t.pendingNeutral
		}
		t.pendingNeutral, t.mismatchSince = 0, 0
		t.current.last = pos
		t.current.matched++
	case classNeutral:
		if t.active {
			t.pendingNeutral++
			if t.pendingNeutral > t.limits.maxNeutral {
				return t.flush()
			}
		}
	default:
		if t.active {
			t.mismatchSince++
			if t.mismatchSince > t.limits.maxMismatch {
				return t.flush()
			}
		}
	}
	return runStats{}, false
}

// flush 结束当前段（若有）。
func (t *runTracker) flush() (runStats, bool) {
	if !t.active {
		return runStats{}, false
	}
	t.active = false
	t.pendingNeutral, t.mismatchSince = 0, 0
	return t.current, true
}

// extendRun 从已知命中位置 from 沿 dir（±1）外扩，返回仍可达的最远命中位置。
// 位置 k 只在 (lo, hi) 开区间内考察；断开规则与 runTracker 相同。
func extendRun(from, dir, lo, hi int, limits runLimits, classify func(int) sampleClass) int {
	last := from
	mismatches, neutrals := 0, 0
	for k := from + dir; k > lo && k < hi; k += dir {
		switch classify(k) {
		case classMatch:
			last = k
			mismatches, neutrals = 0, 0
		case classNeutral:
			neutrals++
			if neutrals > limits.maxNeutral {
				return last
			}
		default:
			mismatches++
			if mismatches > limits.maxMismatch {
				return last
			}
		}
	}
	return last
}
