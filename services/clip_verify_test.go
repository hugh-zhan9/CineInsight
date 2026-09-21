package services

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func clipTestRGB(red, green, blue byte) []byte {
	frame := make([]byte, clipVerifyFrameBytes)
	for y := 0; y < clipVerifySide; y++ {
		for x := 0; x < clipVerifySide; x++ {
			index := (y*clipVerifySide + x) * 3
			shade := byte((x*13 + y*23 + x*y*7) % 100)
			frame[index], frame[index+1], frame[index+2] = red/2+shade, green/2+shade, blue/2+shade
		}
	}
	return frame
}

func clipDifferentStructure(frame []byte) []byte {
	result := make([]byte, len(frame))
	for pixel := 0; pixel < len(frame)/3; pixel++ {
		// 保留颜色分布，打乱空间位置。
		source := (pixel*31 + 17) % (len(frame) / 3)
		copy(result[pixel*3:pixel*3+3], frame[source*3:source*3+3])
	}
	return result
}

// 数据库夹具只模拟候选组织和审阅，源文件不是视频；复核算法与真正 ffmpeg
// 读取分别由本文件及 frame_hash_ffmpeg_test.go 覆盖。
func newClipFixtureCleanupService() *CleanupService {
	return &CleanupService{clipFrame: func(context.Context, string, float64) ([]byte, error) {
		return clipTestRGB(120, 80, 40), nil
	}}
}

func TestClipRGBFramesMatch(t *testing.T) {
	left := clipTestRGB(120, 80, 40)
	for _, test := range []struct {
		name  string
		right []byte
		want  bool
	}{
		{"same", clipTestRGB(120, 80, 40), true},
		{"compression noise", clipTestRGB(127, 76, 46), true},
		{"color grading", clipTestRGB(40, 80, 120), true},
		{"brightness grading", clipTestRGB(160, 120, 80), true},
		{"different structure", clipDifferentStructure(left), false},
		{"flat frames", make([]byte, clipVerifyFrameBytes), false},
		{"incomplete", left[:len(left)-1], false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := clipRGBFramesMatch(left, test.right); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
	if clipRGBFramesMatch(make([]byte, clipVerifyFrameBytes), make([]byte, clipVerifyFrameBytes)) {
		t.Fatal("two flat frames provide no structural evidence")
	}
	// 大部分画面相同，但四分之一的纹理被纯色块替换；不能只看背景。
	left = clipTestRGB(120, 120, 120)
	right := append([]byte(nil), left...)
	for y := 0; y < 8; y++ {
		for x := 0; x < clipVerifySide; x++ {
			value := byte(60)
			if x >= clipVerifySide/2 {
				value = 180
			}
			for channel := 0; channel < 3; channel++ {
				right[(y*clipVerifySide+x)*3+channel] = value
			}
		}
	}
	if clipRGBFramesMatch(left, right) {
		t.Fatal("different spatial content accepted")
	}
}

func TestClipVerificationPositions(t *testing.T) {
	hashes := randomFrameHashes(803, 40)
	for _, length := range []int{0, 1, 3, 4, 5, 20} {
		positions := clipVerificationPositions(hashes, hashes[10:10+length], 10)
		if len(positions) != min(length, clipVerifySamples) {
			t.Fatalf("length=%d positions=%v", length, positions)
		}
		for index, position := range positions {
			if position < 0 || position >= length || (index > 0 && position <= positions[index-1]) {
				t.Fatalf("invalid positions: %v", positions)
			}
		}
		if length > 1 && (positions[0] != 0 || positions[len(positions)-1] != length-1) {
			t.Fatalf("sampling does not span the clip: %v", positions)
		}
	}
}

func TestCleanupClipRequiresStructureVerification(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	full := clipFixtureVideo(t, root, "full.mp4", "full content")
	clip := clipFixtureVideo(t, root, "clip.mp4", "clip")
	hashes := randomFrameHashes(801, 40)
	seedFrameHashSequence(t, full, hashes)
	seedFrameHashSequence(t, clip, hashes[8:24])
	// 刻意制造哈希完全相同、实际结构不同的候选；不能展示 100% 命中误导用户。
	svc := &CleanupService{clipFrame: func(_ context.Context, path string, _ float64) ([]byte, error) {
		if path == clip.Path {
			return clipDifferentStructure(clipTestRGB(120, 80, 40)), nil
		}
		return clipTestRGB(120, 80, 40), nil
	}}
	result, err := svc.AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil || len(result.ClipGroups) != 0 {
		t.Fatalf("unrelated structure: %+v, %v", result, err)
	}
	svc.clipFrame = func(context.Context, string, float64) ([]byte, error) {
		return nil, errors.New("decoder error with /private/media/path")
	}
	result, err = svc.AnalyzeCleanupCandidates(CleanupCriteria{})
	if err == nil || result != nil || strings.Contains(err.Error(), "/private/media") {
		t.Fatalf("verification error must fail without unchecked candidates or paths: %+v %v", result, err)
	}
}

func TestVerifyClipPairSamplingAndFailures(t *testing.T) {
	root := t.TempDir()
	makeSequence := func(name string, hashes []uint64) clipSequence {
		path := root + "/" + name
		if err := os.WriteFile(path, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		sequence := clipSequence{hashes: hashes, intervalMS: 2000, sourceSize: info.Size(), sourceMod: info.ModTime().UnixNano()}
		sequence.video.Path = path
		return sequence
	}
	hashes := randomFrameHashes(802, 50)
	full, clip := makeSequence("full", hashes), makeSequence("clip", hashes[10:30])
	for _, test := range []struct {
		name       string
		mismatches int
		want       bool
	}{
		{"all agree", 0, true}, {"one overlay", 1, true}, {"two disagree", 2, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			var fullSecond float64
			svc := &CleanupService{clipFrame: func(_ context.Context, path string, second float64) ([]byte, error) {
				calls++
				if path == full.video.Path {
					fullSecond = second
				} else if second+20 != fullSecond {
					t.Fatalf("inconsistent offset: %.1f %.1f", second, fullSecond)
				}
				if calls%2 == 0 && calls/2 <= test.mismatches {
					return clipDifferentStructure(clipTestRGB(120, 80, 40)), nil
				}
				return clipTestRGB(120, 80, 40), nil
			}}
			got, err := svc.verifyClipPair(full, clip, CleanupClipGroup{OffsetSeconds: 20})
			if err != nil || got != test.want || calls != 10 {
				t.Fatalf("got %v err=%v calls=%d", got, err, calls)
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		svc := &CleanupService{ctx: ctx, clipFrame: func(ctx context.Context, _ string, _ float64) ([]byte, error) {
			return nil, ctx.Err()
		}}
		ok, err := svc.verifyClipPair(full, clip, CleanupClipGroup{OffsetSeconds: 20})
		if ok || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled: %v %v", ok, err)
		}
	})
	t.Run("source changed during verification", func(t *testing.T) {
		svc := newClipFixtureCleanupService()
		svc.clipFrame = func(context.Context, string, float64) ([]byte, error) {
			if err := os.WriteFile(clip.video.Path, []byte("changed content"), 0600); err != nil {
				t.Fatal(err)
			}
			return clipTestRGB(120, 80, 40), nil
		}
		if ok, err := svc.verifyClipPair(full, clip, CleanupClipGroup{OffsetSeconds: 20}); err == nil || ok {
			t.Fatalf("changed source: %v %v", ok, err)
		}
	})
}
