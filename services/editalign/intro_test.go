package editalign

import (
	"context"
	"errors"
	"testing"
)

const introMS = 12000

// episode 由冷开场 + 共享片头 + 正片组成；extraIntroAt>0 时在该处再放一遍片头。
func episode(seed uint64, intro *synthContent, coldMS, extraIntroAt, frameMS int64) synthVideo {
	cold := newSynthContent(seed, 120)
	body := newSynthContent(seed+1000, 120)
	pieces := []piece{}
	if coldMS > 0 {
		pieces = append(pieces, piece{content: cold, fileMS: 0, contentMS: 0, durMS: coldMS})
	}
	pieces = append(pieces, piece{content: intro, fileMS: coldMS, contentMS: 0, durMS: introMS})
	duration := int64(100000)
	if extraIntroAt > 0 {
		pieces = append(pieces,
			piece{content: body, fileMS: coldMS + introMS, contentMS: 0, durMS: extraIntroAt - coldMS - introMS},
			piece{content: intro, fileMS: extraIntroAt, contentMS: 0, durMS: introMS},
			piece{content: body, fileMS: extraIntroAt + introMS, contentMS: 60000, durMS: duration - extraIntroAt - introMS})
	} else {
		pieces = append(pieces, piece{content: body, fileMS: coldMS + introMS, contentMS: 0, durMS: duration - coldMS - introMS})
	}
	return synthVideo{pieces: pieces, durationMS: duration, frameMS: frameMS}
}

func TestDetectIntrosSharedAtDifferentOffsets(t *testing.T) {
	intro := newSynthContent(7, 20)
	colds := []int64{30000, 45120, 7360, 0}
	for _, withFrames := range []bool{false, true} {
		var sources []Source
		for i, cold := range colds {
			sources = append(sources, episode(uint64(100+i*10), intro, cold, 0, 40).source(uint(i+1), 250, withFrames))
		}
		results, err := DetectIntros(context.Background(), sources, IntroOptions{})
		if err != nil {
			t.Fatal(err)
		}
		tolerance := int64(500) // 序列精度：±2 个 4fps 采样
		if withFrames {
			tolerance = 40 // 帧级：±1 帧（25fps）
		}
		for i, result := range results {
			if result.Status != IntroDetected || result.SourceID != uint(i+1) {
				t.Fatalf("frames=%v source %d: %+v", withFrames, i, result)
			}
			within(t, "start", result.StartMS, colds[i], tolerance)
			within(t, "end", result.EndMS, colds[i]+introMS, tolerance)
			if result.MatchedWith != len(colds)-1 || result.MatchRate < 0.8 {
				t.Errorf("frames=%v source %d: MatchedWith=%d MatchRate=%.2f", withFrames, i, result.MatchedWith, result.MatchRate)
			}
		}
	}
}

func TestDetectIntrosUndetectedAndAmbiguous(t *testing.T) {
	intro := newSynthContent(7, 20)
	sources := []Source{
		episode(200, intro, 30000, 0, 40).source(1, 250, true),
		episode(210, intro, 15000, 0, 40).source(2, 250, true),
		// 没有片头的一集（全片都是自己的内容）。
		{ID: 3, DurationMS: 100000, Hashes: synthVideo{pieces: []piece{{content: newSynthContent(220, 120), durMS: 100000}}, durationMS: 100000, frameMS: 40}.sequence(250)},
		// 片头出现两次：不知道剪哪一处，必须标 ambiguous。
		episode(230, intro, 20000, 60000, 40).source(4, 250, true),
		episode(240, intro, 40000, 0, 40).source(5, 250, true),
	}
	results, err := DetectIntros(context.Background(), sources, IntroOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []IntroStatus{IntroDetected, IntroDetected, IntroUndetected, IntroAmbiguous, IntroDetected}
	starts := []int64{30000, 15000, 0, 0, 40000}
	for i, result := range results {
		if result.Status != want[i] {
			t.Fatalf("source %d: status %s, want %s (%+v)", i+1, result.Status, want[i], result)
		}
		if want[i] == IntroDetected {
			within(t, "start", result.StartMS, starts[i], 40)
			within(t, "end", result.EndMS, starts[i]+introMS, 40)
		} else if result.StartMS != 0 || result.EndMS != 0 || result.MatchedWith != 0 {
			t.Errorf("source %d: non-detected result must be empty: %+v", i+1, result)
		}
	}
}

func TestDetectIntrosNothingShared(t *testing.T) {
	var sources []Source
	for i := range 3 {
		v := synthVideo{pieces: []piece{{content: newSynthContent(uint64(300+i), 120), durMS: 90000}}, durationMS: 90000, frameMS: 40}
		sources = append(sources, v.source(uint(i+1), 250, false))
	}
	results, err := DetectIntros(context.Background(), sources, IntroOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Status != IntroUndetected {
			t.Fatalf("unexpected %+v", result)
		}
	}
}

// 第 2 集开头的"前情提要"复制了第 1 集正片里 20 秒（比片头还长）；共识区间让它不被当成片头。
func TestDetectIntrosIgnoresLongerSharedRecap(t *testing.T) {
	intro := newSynthContent(7, 20)
	ep1 := episode(400, intro, 10000, 0, 40)
	ep1Body := ep1.pieces[len(ep1.pieces)-1].content
	ep2 := episode(410, intro, 25000, 0, 40)
	ep2.pieces[0] = piece{content: ep1Body, fileMS: 0, contentMS: 30000, durMS: 22000}
	ep3 := episode(420, intro, 5000, 0, 40)
	ep4 := episode(430, intro, 33000, 0, 40)
	sources := []Source{ep1.source(1, 250, false), ep2.source(2, 250, false), ep3.source(3, 250, false), ep4.source(4, 250, false)}
	results, err := DetectIntros(context.Background(), sources, IntroOptions{})
	if err != nil {
		t.Fatal(err)
	}
	starts := []int64{10000, 25000, 5000, 33000}
	for i, result := range results {
		if result.Status != IntroDetected {
			t.Fatalf("source %d: %+v", i+1, result)
		}
		within(t, "start", result.StartMS, starts[i], 500)
		within(t, "end", result.EndMS, starts[i]+introMS, 500)
	}
}

func TestDetectIntrosRequiresTwoSources(t *testing.T) {
	intro := newSynthContent(7, 20)
	for _, sources := range [][]Source{nil, {episode(1, intro, 0, 0, 40).source(1, 250, false)}} {
		if _, err := DetectIntros(context.Background(), sources, IntroOptions{}); !errors.Is(err, errTooFewSources) {
			t.Fatalf("len=%d: err=%v", len(sources), err)
		}
	}
}

func TestDetectIntrosRejectsMixedSteps(t *testing.T) {
	intro := newSynthContent(7, 20)
	a := episode(1, intro, 0, 0, 40).source(1, 250, false)
	b := episode(2, intro, 0, 0, 40).source(2, 500, false)
	if _, err := DetectIntros(context.Background(), []Source{a, b}, IntroOptions{}); err == nil {
		t.Fatal("expected step mismatch error")
	}
}

func TestDetectIntrosHonoursCancellation(t *testing.T) {
	intro := newSynthContent(7, 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sources := []Source{episode(1, intro, 0, 0, 40).source(1, 250, false), episode(2, intro, 9000, 0, 40).source(2, 250, false)}
	if _, err := DetectIntros(ctx, sources, IntroOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// 每集都有 15 秒黑场但没有共享内容：低信息帧不计票，不能凭黑场对上黑场认出"片头"。
func TestDetectIntrosIgnoresSharedBlack(t *testing.T) {
	var sources []Source
	for i, black := range []int64{10000, 32000, 51000} {
		c := newSynthContent(uint64(630+i), 120)
		v := video(40, cut(c, 0, 0, black), cut(c, black+15000, black+15000, 90000-black-15000))
		sources = append(sources, v.source(uint(i+1), 250, true))
	}
	results, err := DetectIntros(context.Background(), sources, IntroOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Status != IntroUndetected {
			t.Fatalf("black-only overlap must stay undetected: %+v", result)
		}
	}
}
