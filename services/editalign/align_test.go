package editalign

import (
	"context"
	"errors"
	"testing"
)

// wantSegment 是期望的对应段（HD 区间与长版起点；时长两侧相等）。
type wantSegment struct {
	hdStart, hdEnd, longStart int64
	status                    SegmentStatus
}

func cut(content *synthContent, fileMS, contentMS, durMS int64) piece {
	return piece{content: content, fileMS: fileMS, contentMS: contentMS, durMS: durMS}
}

func video(frameMS int64, pieces ...piece) synthVideo {
	end := int64(0)
	for _, p := range pieces {
		end = max(end, p.fileMS+p.durMS)
	}
	return synthVideo{pieces: pieces, durationMS: end, frameMS: frameMS}
}

func alignAndCheck(t *testing.T, long, hd synthVideo, withFrames bool, tolerance int64, want []wantSegment) []AlignedSegment {
	t.Helper()
	got, err := AlignSegments(context.Background(), long.source(1, 250, withFrames), hd.source(2, 250, withFrames), AlignOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("frames=%v: got %d segments %+v, want %d", withFrames, len(got), got, len(want))
	}
	for i, w := range want {
		g := got[i]
		within(t, "HDStart", g.HDStartMS, w.hdStart, tolerance)
		within(t, "HDEnd", g.HDEndMS, w.hdEnd, tolerance)
		within(t, "LongStart", g.LongStartMS, w.longStart, tolerance)
		if g.LongEndMS-g.LongStartMS != g.HDEndMS-g.HDStartMS {
			t.Errorf("segment %d durations differ: %+v", i, g)
		}
		status := w.status
		if status == "" {
			status = SegmentMatched
		}
		if g.Status != status || g.MatchRate < 0.7 {
			t.Errorf("segment %d: status %s rate %.2f, want %s", i, g.Status, g.MatchRate, status)
		}
	}
	return got
}

// 序列精度 ±2 个采样；帧级 ±1 帧。
func tolerances(withFrames bool, frameMS int64) int64 {
	if withFrames {
		return frameMS
	}
	return 500
}

func TestAlignSingleContiguousSegment(t *testing.T) {
	m := newSynthContent(500, 200)
	long := video(40, cut(m, 0, 0, 120000))
	hd := video(40, cut(m, 0, 20000, 60000))
	for _, withFrames := range []bool{false, true} {
		alignAndCheck(t, long, hd, withFrames, tolerances(withFrames, 40), []wantSegment{{0, 60000, 20000, ""}})
	}
}

func TestAlignDifferentFrameRates(t *testing.T) {
	m := newSynthContent(505, 200)
	long := video(42, cut(m, 0, 0, 120000)) // ≈23.8fps
	hd := video(40, cut(m, 0, 20000, 30000), cut(newSynthContent(506, 60), 30000, 0, 10000), cut(m, 40000, 60000, 30000))
	alignAndCheck(t, long, hd, true, 42, []wantSegment{{0, 30000, 20000, ""}, {40000, 70000, 60000, ""}})
}

func TestAlignDeletions(t *testing.T) {
	m := newSynthContent(510, 200)
	long := video(40, cut(m, 0, 0, 120000))
	hd := video(40, cut(m, 0, 0, 40000), cut(m, 40000, 55000, 45000))
	for _, withFrames := range []bool{false, true} {
		alignAndCheck(t, long, hd, withFrames, tolerances(withFrames, 40), []wantSegment{
			{0, 40000, 0, ""}, {40000, 85000, 55000, ""},
		})
	}
}

func TestAlignInsertions(t *testing.T) {
	m, extra := newSynthContent(520, 200), newSynthContent(521, 60)
	long := video(40, cut(m, 0, 0, 90000))
	hd := video(40, cut(m, 0, 0, 40000), cut(extra, 40000, 0, 10000), cut(m, 50000, 40000, 50000))
	for _, withFrames := range []bool{false, true} {
		alignAndCheck(t, long, hd, withFrames, tolerances(withFrames, 40), []wantSegment{
			{0, 40000, 0, ""}, {50000, 100000, 40000, ""},
		})
	}
}

// 长版里同一场景 D 出现两次；HD 只在一段新内容中间出现一次 D：D 的锚点次优与最优同样好，
// 全部作废，不能产生自信的对应段。
func TestAlignDuplicatedSceneInLongDoesNotMatch(t *testing.T) {
	m, d, y := newSynthContent(530, 200), newSynthContent(531, 20), newSynthContent(532, 60)
	long := video(40, cut(m, 0, 0, 60000), cut(d, 60000, 0, 8000), cut(m, 68000, 60000, 40000),
		cut(d, 108000, 0, 8000), cut(m, 116000, 100000, 40000))
	hd := video(40, cut(m, 0, 10000, 40000), cut(y, 40000, 0, 10000), cut(d, 50000, 0, 8000),
		cut(y, 58000, 10000, 10000), cut(m, 68000, 100000, 30000))
	for _, withFrames := range []bool{false, true} {
		alignAndCheck(t, long, hd, withFrames, tolerances(withFrames, 40), []wantSegment{
			{0, 40000, 10000, ""}, {68000, 98000, 116000, ""},
		})
	}
}

// HD 把长版的一段（M[10s,20s)）作为闪回又放了一遍：两段都能对上同一长版区间，
// 只能有一段认领，另一段标 conflict 交给用户。
func TestAlignFlashbackMarksConflict(t *testing.T) {
	m, z := newSynthContent(540, 200), newSynthContent(541, 30)
	long := video(40, cut(m, 0, 0, 60000))
	hd := video(40, cut(m, 0, 0, 30000), cut(z, 30000, 0, 5000), cut(m, 35000, 10000, 10000), cut(m, 45000, 30000, 30000))
	for _, withFrames := range []bool{false, true} {
		alignAndCheck(t, long, hd, withFrames, tolerances(withFrames, 40), []wantSegment{
			{0, 30000, 0, SegmentMatched}, {35000, 45000, 10000, SegmentConflict}, {45000, 75000, 30000, SegmentMatched},
		})
	}
}

// 黑场（低信息帧）不投票：HD 只有黑场与长版没有的内容，长版也有大段黑场，结果必须为空。
func TestAlignBlackFramesDoNotVote(t *testing.T) {
	m, m2, x := newSynthContent(550, 100), newSynthContent(551, 60), newSynthContent(552, 60)
	long := video(40, cut(m, 0, 0, 60000), cut(m2, 90000, 0, 30000))      // [60s,90s) 黑场
	hd := video(40, cut(x, 20000, 0, 30000), cut(x, 60000, 30000, 10000)) // 前 20 秒与 [50s,60s) 黑场
	got, err := AlignSegments(context.Background(), long.source(1, 250, true), hd.source(2, 250, true), AlignOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no segments, got %+v", got)
	}
}

// 中间被替换成等长新内容：间隔 2 秒（≤3 秒）合并成一段，5 秒则保持两段。
func TestAlignMergeGap(t *testing.T) {
	m, y := newSynthContent(560, 200), newSynthContent(561, 30)
	long := video(40, cut(m, 0, 0, 90000))
	short := video(40, cut(m, 0, 0, 30000), cut(y, 30000, 0, 2000), cut(m, 32000, 32000, 28000))
	alignAndCheck(t, long, short, true, 40, []wantSegment{{0, 60000, 0, ""}})
	wide := video(40, cut(m, 0, 0, 30000), cut(y, 30000, 0, 5000), cut(m, 35000, 35000, 25000))
	alignAndCheck(t, long, wide, true, 40, []wantSegment{{0, 30000, 0, ""}, {35000, 60000, 35000, ""}})
}

// 短于 3 秒的共享片段丢弃。
func TestAlignDropsShortSegments(t *testing.T) {
	m, x := newSynthContent(570, 200), newSynthContent(571, 60)
	long := video(40, cut(m, 0, 0, 90000))
	hd := video(40, cut(x, 0, 0, 20000), cut(m, 20000, 50000, 2000), cut(x, 22000, 20000, 20000))
	got, err := AlignSegments(context.Background(), long.source(1, 250, false), hd.source(2, 250, false), AlignOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no segments, got %+v", got)
	}
}

func TestAlignProgressAndCancellation(t *testing.T) {
	m := newSynthContent(580, 400)
	long := video(40, cut(m, 0, 0, 300000)).source(1, 250, false)
	hd := video(40, cut(m, 0, 10000, 280000)).source(2, 250, false)

	var calls [][2]int
	if _, err := AlignSegments(context.Background(), long, hd, AlignOptions{}, func(done, total int) {
		calls = append(calls, [2]int{done, total})
	}); err != nil {
		t.Fatal(err)
	}
	last := calls[len(calls)-1]
	if len(calls) < 3 || last[0] != last[1] || last[1] != 280 {
		t.Fatalf("progress calls %v", calls)
	}
	for i := 1; i < len(calls); i++ {
		if calls[i][0] < calls[i-1][0] {
			t.Fatalf("progress went backwards: %v", calls)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	seen := 0
	_, err := AlignSegments(ctx, long, hd, AlignOptions{}, func(done, total int) {
		seen++
		if done > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || seen > 3 {
		t.Fatalf("err=%v after %d progress calls, want prompt context.Canceled", err, seen)
	}
}

func TestAlignRejectsBadInput(t *testing.T) {
	m := newSynthContent(590, 60)
	a := video(40, cut(m, 0, 0, 30000))
	if _, err := AlignSegments(context.Background(), a.source(1, 250, false), a.source(2, 500, false), AlignOptions{}, nil); err == nil {
		t.Fatal("expected step mismatch error")
	}
	if _, err := AlignSegments(context.Background(), a.source(1, 250, false), a.source(2, 250, false), AlignOptions{MaxHamming: -1}, nil); err == nil {
		t.Fatal("expected negative option error")
	}
	got, err := AlignSegments(context.Background(), Source{Hashes: Sequence{StepMS: 250}}, a.source(2, 250, false), AlignOptions{}, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty long: %v %v", got, err)
	}
}

// 段后两侧都是黑场：帧级边界停在最后一帧有内容的匹配帧，不随 ±1 秒窗口滑进黑场。
func TestAlignBoundaryStopsBeforeSharedBlack(t *testing.T) {
	m, other, x := newSynthContent(620, 100), newSynthContent(621, 60), newSynthContent(622, 60)
	long := video(40, cut(m, 0, 0, 30000), cut(other, 40000, 0, 20000)) // [30s,40s) 黑场
	hd := video(40, cut(m, 0, 0, 30000), cut(x, 40000, 0, 20000))       // [30s,40s) 黑场
	alignAndCheck(t, long, hd, true, 0, []wantSegment{{0, 30000, 0, ""}})
}
