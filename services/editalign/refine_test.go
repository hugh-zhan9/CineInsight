package editalign

import (
	"context"
	"slices"
	"testing"
)

// a 在 [3000,13000) 放内容 m 的 [0,10s)，b 在 [7120,17120) 放同一段：真实偏移 4120ms。
// 粗结果两端各差一个采样、偏移差 120ms，细化必须回到准确的首/末帧。
func TestRefineSpanLocatesBoundaryFrames(t *testing.T) {
	m := newSynthContent(600, 30)
	for _, bFrameMS := range []int64{40, 42} {
		a := video(40, cut(newSynthContent(601, 30), 0, 0, 3000), cut(m, 3000, 0, 10000), cut(newSynthContent(602, 30), 13000, 0, 5000))
		b := video(bFrameMS, cut(newSynthContent(603, 30), 0, 0, 7120), cut(m, 7120, 0, 10000), cut(newSynthContent(604, 30), 17120, 0, 5000))
		est := spanEstimate{aStart: 3250, aEnd: 12750, offCenter: 4000, offRadius: 375, stepMS: 250}
		got, err := refineSpan(context.Background(), newFrameCache(a.reader()), newFrameCache(b.reader()), est)
		if err != nil {
			t.Fatal(err)
		}
		if !got.refined {
			t.Fatalf("b frame %dms: not refined: %+v", bFrameMS, got)
		}
		within(t, "start", got.aStart, 3000, 0)
		within(t, "end", got.aEnd, 13000, 0)
		within(t, "offset", got.offset, 4120, bFrameMS/2)
	}
}

func TestRefineSpanWithoutReadersKeepsCoarse(t *testing.T) {
	est := spanEstimate{aStart: 1000, aEnd: 5000, offCenter: 250, offRadius: 375, stepMS: 250}
	got, err := refineSpan(context.Background(), nil, newFrameCache(video(40).reader()), est)
	if err != nil || got.refined || got.aStart != 1000 || got.aEnd != 5000 || got.offset != 250 {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestRefineSpanRejectsMismatchedFrameSizes(t *testing.T) {
	m := newSynthContent(610, 20)
	small := func(ctx context.Context, start, duration int64) ([]GrayFrame, error) {
		var frames []GrayFrame
		for pts := (start + 39) / 40 * 40; pts < start+duration; pts += 40 {
			frames = append(frames, GrayFrame{PTSMS: pts, Pixels: make([]byte, 16*16)})
		}
		return frames, nil
	}
	a := video(40, cut(m, 0, 0, 10000))
	est := spanEstimate{aStart: 2000, aEnd: 8000, offCenter: 0, offRadius: 375, stepMS: 250}
	if _, err := refineSpan(context.Background(), newFrameCache(a.reader()), newFrameCache(small), est); err == nil {
		t.Fatal("expected frame size error")
	}
}

func TestFrameCacheReusesCoveringWindow(t *testing.T) {
	var reads [][2]int64
	reader := func(ctx context.Context, start, duration int64) ([]GrayFrame, error) {
		reads = append(reads, [2]int64{start, duration})
		var frames []GrayFrame
		for pts := start; pts < start+duration; pts += 40 {
			frames = append(frames, GrayFrame{PTSMS: pts, Pixels: []byte{1}})
		}
		return frames, nil
	}
	cache := newFrameCache(reader)
	first, _ := cache.window(context.Background(), 1200, 2200)
	second, _ := cache.window(context.Background(), 1300, 2100)
	if len(reads) != 1 || reads[0] != [2]int64{1000, 1500} {
		t.Fatalf("reads %v, want one read of [1000,2500)", reads)
	}
	if first[0].PTSMS != 1200 || first[len(first)-1].PTSMS != 2160 || second[0].PTSMS != 1320 {
		t.Fatalf("window subsets wrong: %d..%d, %d", first[0].PTSMS, first[len(first)-1].PTSMS, second[0].PTSMS)
	}
	if got, _ := cache.window(context.Background(), -500, 300); len(reads) != 2 || reads[1][0] != 0 || len(got) != 8 {
		t.Fatalf("negative start must clamp to 0: reads %v frames %d", reads, len(got))
	}
}

func TestChangePoints(t *testing.T) {
	f, m := false, true
	cases := []struct {
		labels     []bool
		start, end int
	}{
		{[]bool{f, f, f, m, m, m}, 3, 6},
		{[]bool{m, m, m, f, f}, 0, 3},
		{[]bool{f, f, m, f, m, m, m}, 2, 7}, // 单帧噪声不把起点拖后
		{[]bool{m, m, m, f, m, f, f}, 0, 5}, // 平票取最晚的终点
		{[]bool{f, f, f}, 3, 0},
		{nil, 0, 0},
	}
	for _, c := range cases {
		if got := startChangePoint(c.labels); got != c.start {
			t.Errorf("start(%v) = %d, want %d", c.labels, got, c.start)
		}
		if got := endChangePoint(c.labels); got != c.end {
			t.Errorf("end(%v) = %d, want %d", c.labels, got, c.end)
		}
	}
}

func TestMadThresholdIsClamped(t *testing.T) {
	if got := madThreshold(nil); got != madThresholdMin {
		t.Fatalf("empty: %v", got)
	}
	if got := madThreshold([]float64{0, 0, 0.5}); got != madThresholdMin {
		t.Fatalf("clean: %v", got)
	}
	if got := madThreshold([]float64{30, 40, 50}); got != madThresholdMax {
		t.Fatalf("noisy: %v", got)
	}
	diffs := []float64{1, 2, 3, 4, 5, 6, 7, 3, 2, 1}
	if got := madThreshold(diffs); got != 2*6+3 {
		t.Fatalf("p90 based: %v (sorted %v)", got, slices.Sorted(slices.Values(diffs)))
	}
}
