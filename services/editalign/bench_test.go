package editalign

import (
	"context"
	"sync"
	"testing"
	"time"
)

var twoHours = sync.OnceValues(func() (Source, Source) {
	m, x := newSynthContent(700, 7300), newSynthContent(701, 120)
	long := video(40, cut(m, 0, 0, 7200000))
	// HD：删去长版 [3000s,3100s)，并在 3000s 处插入 60 秒新内容。
	hd := video(40, cut(m, 0, 0, 3000000), cut(x, 3000000, 0, 60000), cut(m, 3060000, 3100000, 4100000))
	return long.source(1, 250, false), hd.source(2, 250, false)
})

// 2 小时长版（4fps，28800 个指纹）对 2 小时 HD（7160 个锚点）：结果正确且远低于 5 秒。
func TestAlignTwoHoursSynthetic(t *testing.T) {
	if testing.Short() {
		t.Skip("2 小时规模测试在 -short 下跳过")
	}
	long, hd := twoHours()
	start := time.Now()
	got, err := AlignSegments(context.Background(), long, hd, AlignOptions{}, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("2h alignment: %v, %d long hashes, %d HD hashes", elapsed, len(long.Hashes.Hashes), len(hd.Hashes.Hashes))
	if elapsed > 5*time.Second {
		t.Fatalf("2h alignment took %v", elapsed)
	}
	want := []wantSegment{{0, 3000000, 0, ""}, {3060000, 7160000, 3100000, ""}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		within(t, "HDStart", got[i].HDStartMS, w.hdStart, 500)
		within(t, "HDEnd", got[i].HDEndMS, w.hdEnd, 500)
		within(t, "LongStart", got[i].LongStartMS, w.longStart, 500)
	}
}

func BenchmarkAlignTwoHoursFourFPS(b *testing.B) {
	long, hd := twoHours()
	b.ResetTimer()
	for range b.N {
		if _, err := AlignSegments(context.Background(), long, hd, AlignOptions{}, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// 10 集、每集前 600 秒（合同默认窗口）× 4fps：45 对两两对角线扫描。
func BenchmarkDetectIntrosTenEpisodes600s(b *testing.B) {
	intro := newSynthContent(7, 20)
	sources := make([]Source, 10)
	for i := range sources {
		cold := int64(5000 + i*7040)
		body := newSynthContent(uint64(800+i), 700)
		v := video(40, cut(newSynthContent(uint64(900+i), 120), 0, 0, cold), cut(intro, cold, 0, introMS),
			cut(body, cold+introMS, 0, 600000-cold-introMS))
		sources[i] = v.source(uint(i+1), 250, false)
	}
	b.ResetTimer()
	for range b.N {
		results, err := DetectIntros(context.Background(), sources, IntroOptions{})
		if err != nil || results[0].Status != IntroDetected {
			b.Fatalf("%v %+v", err, results)
		}
	}
}
