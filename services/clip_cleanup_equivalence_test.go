package services

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"video-master/models"
)

// legacyMatchClipSequences 是并行化之前 loadCleanupClipGroups 两两比较段的原样拷贝
// （只把 MatchClip 换成同样原样保留的 legacyMatchClip），作为整类输出的参照实现。
func legacyMatchClipSequences(s *CleanupService, ctx context.Context, sequences []clipSequence, excluded map[[2]uint]struct{}, dismissed map[[2]uint]models.ClipDismissal) ([]CleanupClipGroup, int, int, error) {
	sort.Slice(sequences, func(i, j int) bool {
		if len(sequences[i].hashes) != len(sequences[j].hashes) {
			return len(sequences[i].hashes) > len(sequences[j].hashes)
		}
		return sequences[i].video.ID < sequences[j].video.ID
	})
	best := make(map[uint]CleanupClipGroup)
	comparedPairs := 0
	skippedVerification := 0
	for full := 0; full < len(sequences); full++ {
		if ctx.Err() != nil {
			return nil, 0, 0, ctx.Err()
		}
		maxClipFrames := int(clipMaxDurationRatio * float64(len(sequences[full].hashes)))
		start := sort.Search(len(sequences), func(index int) bool {
			return len(sequences[index].hashes) <= maxClipFrames
		})
		if start <= full {
			start = full + 1
		}
		for clip := start; clip < len(sequences); clip++ {
			if ctx.Err() != nil {
				return nil, 0, 0, ctx.Err()
			}
			comparedPairs++
			candidate, ok := legacyEvaluateClipPair(sequences[full], sequences[clip], excluded, dismissed)
			if !ok {
				continue
			}
			existing, exists := best[sequences[clip].video.ID]
			if !exists || candidate.MatchRate > existing.MatchRate {
				verified, err := s.verifyClipPair(ctx, sequences[full], sequences[clip], candidate)
				if err != nil {
					if ctx.Err() != nil {
						return nil, 0, 0, ctx.Err()
					}
					skippedVerification++
					continue
				}
				if !verified {
					continue
				}
				best[sequences[clip].video.ID] = candidate
			}
		}
	}
	groups := make([]CleanupClipGroup, 0, len(best))
	for _, group := range best {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Full.ID != groups[j].Full.ID {
			return groups[i].Full.ID < groups[j].Full.ID
		}
		return groups[i].Clip.ID < groups[j].Clip.ID
	})
	return groups, comparedPairs, skippedVerification, nil
}

func legacyEvaluateClipPair(full, clip clipSequence, excluded map[[2]uint]struct{}, dismissed map[[2]uint]models.ClipDismissal) (CleanupClipGroup, bool) {
	if full.video.ID == clip.video.ID {
		return CleanupClipGroup{}, false
	}
	if full.intervalMS != clip.intervalMS || full.intervalMS <= 0 {
		return CleanupClipGroup{}, false
	}
	if _, skip := excluded[cleanupVideoPairKey(full.video.ID, clip.video.ID)]; skip {
		return CleanupClipGroup{}, false
	}
	if clipDismissalStillApplies(dismissed, full, clip) {
		return CleanupClipGroup{}, false
	}
	offset, rate, ok := legacyMatchClip(full.hashes, clip.hashes, full.intervalMS)
	if !ok {
		return CleanupClipGroup{}, false
	}
	return CleanupClipGroup{
		Full:             full.video,
		Clip:             clip.video,
		OffsetSeconds:    float64(offset) * clipFrameIntervalSeconds(full.intervalMS),
		MatchRate:        rate,
		EstimatedSavings: clip.video.Size,
		fullFingerprint:  mediaProbeFingerprint{size: full.sourceSize, modTimeNS: full.sourceMod},
		clipFingerprint:  mediaProbeFingerprint{size: clip.sourceSize, modTimeNS: clip.sourceMod},
	}, true
}

// clipEquivalenceFixture 生成一批有效序列：独立视频、同一部片的两个转码版本、从中
// 截下的带噪片段（含 12/16 边界附近的）、间隔不同的旧行，以及排除与忽略记录。
// 每条序列对应 root 下一个真实文件，复核前的源指纹核对因此会真的执行。
func clipEquivalenceFixture(t *testing.T, seed uint64, count int) ([]clipSequence, map[[2]uint]struct{}, map[[2]uint]models.ClipDismissal, map[string]string) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed^0x5eed))
	root := t.TempDir()
	hashes := make([][]uint64, count)
	for index := range hashes {
		hashes[index] = equivalenceWalk(rng, 20+rng.IntN(380), 0.04)
	}
	for index := 0; index < count; index++ {
		source := hashes[rng.IntN(count)]
		switch roll := rng.IntN(10); {
		case roll < 2 && len(source) >= 60: // 截取片段，噪声 0..9 位
			length := 5 + rng.IntN(int(float64(len(source))*0.85)-5)
			start := rng.IntN(len(source) - length + 1)
			noise := rng.IntN(10)
			clip := make([]uint64, length)
			for frame := range clip {
				clip[frame] = equivalenceFlip(rng, source[start+frame], rng.IntN(noise+1))
			}
			hashes[index] = clip
		case roll == 2: // 同一部片的另一个版本，片段会同时对上两个完整片
			version := make([]uint64, len(source))
			for frame := range version {
				version[frame] = equivalenceFlip(rng, source[frame], rng.IntN(3))
			}
			hashes[index] = version
		}
	}
	modes := []string{"ok", "ok", "ok", "ok", "ok", "ok", "mismatch", "broken", "changed"}
	sequences := make([]clipSequence, count)
	pathMode := make(map[string]string, count)
	for index := range sequences {
		id := uint(index + 1)
		path := filepath.Join(root, fmt.Sprintf("v%03d.mp4", id))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("video-%d", id)), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		mode := modes[rng.IntN(len(modes))]
		pathMode[path] = mode
		interval := clipFrameIntervalMS
		if rng.IntN(25) == 0 {
			interval = 4000
		}
		sourceSize := info.Size()
		if mode == "changed" {
			sourceSize++ // 复核前核对源指纹失败 → 跳过计数
		}
		sequences[index] = clipSequence{
			video:      models.Video{ID: id, Name: filepath.Base(path), Path: path, Size: int64(100 + index)},
			hashes:     hashes[index],
			intervalMS: interval,
			sourceSize: sourceSize,
			sourceMod:  info.ModTime().UnixNano(),
		}
	}
	excluded := make(map[[2]uint]struct{})
	dismissed := make(map[[2]uint]models.ClipDismissal)
	// 排除与忽略只有落在真正匹配的配对上才有意义：从参照实现的命中里抽一部分。
	for _, full := range sequences {
		for _, clip := range sequences {
			if _, ok := legacyEvaluateClipPair(full, clip, nil, nil); !ok {
				continue
			}
			switch rng.IntN(6) {
			case 0:
				excluded[cleanupVideoPairKey(full.video.ID, clip.video.ID)] = struct{}{}
			case 1:
				record := models.ClipDismissal{VideoFullID: full.video.ID, VideoClipID: clip.video.ID,
					FullSourceSize: full.sourceSize, FullSourceModTimeNS: full.sourceMod, ClipSourceSize: clip.sourceSize, ClipSourceModTimeNS: clip.sourceMod}
				if rng.IntN(3) == 0 {
					record.ClipSourceSize++ // 文件变过：忽略失效
				}
				dismissed[[2]uint{full.video.ID, clip.video.ID}] = record
			}
		}
	}
	return sequences, excluded, dismissed, pathMode
}

// clipEquivalenceReader 是确定性的抽帧替身：mismatch 的画面结构不同（复核否决），
// broken 读失败（跳过计数）。记录调用顺序，用来证明复核仍按原顺序逐对串行执行。
func clipEquivalenceReader(pathMode map[string]string, calls *[]string) clipFrameReader {
	same := clipTestRGB(120, 80, 40)
	different := clipDifferentStructure(same)
	return func(_ context.Context, path string, second float64) ([]byte, error) {
		*calls = append(*calls, fmt.Sprintf("%s@%.1f", filepath.Base(path), second))
		switch pathMode[path] {
		case "broken":
			return nil, errors.New("decoder failure")
		case "mismatch":
			return different, nil
		}
		return same, nil
	}
}

func cloneClipSequences(sequences []clipSequence) []clipSequence {
	return append([]clipSequence(nil), sequences...)
}

func TestMatchClipSequencesEquivalentToSequentialLegacy(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		sequences, excluded, dismissed, pathMode := clipEquivalenceFixture(t, seed, 160)
		var legacyCalls, calls []string
		legacy := &CleanupService{clipFrame: clipEquivalenceReader(pathMode, &legacyCalls)}
		wantGroups, wantPairs, wantSkipped, err := legacyMatchClipSequences(legacy, context.Background(), cloneClipSequences(sequences), excluded, dismissed)
		if err != nil {
			t.Fatal(err)
		}
		svc := &CleanupService{clipFrame: clipEquivalenceReader(pathMode, &calls)}
		gotGroups, gotPairs, gotSkipped, err := svc.matchClipSequences(context.Background(), cloneClipSequences(sequences), excluded, dismissed)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotGroups, wantGroups) {
			t.Fatalf("seed %d 候选不一致:\n got %+v\nwant %+v", seed, gotGroups, wantGroups)
		}
		if gotPairs != wantPairs || gotSkipped != wantSkipped {
			t.Fatalf("seed %d 计数不一致: pairs %d/%d skipped %d/%d", seed, gotPairs, wantPairs, gotSkipped, wantSkipped)
		}
		if !reflect.DeepEqual(calls, legacyCalls) {
			t.Fatalf("seed %d 复核读取顺序不一致: got %d calls want %d", seed, len(calls), len(legacyCalls))
		}
		if len(wantGroups) < 5 || wantSkipped == 0 || len(excluded) == 0 || len(dismissed) == 0 {
			t.Fatalf("seed %d 夹具覆盖不足: groups=%d skipped=%d excluded=%d dismissed=%d", seed, len(wantGroups), wantSkipped, len(excluded), len(dismissed))
		}
		t.Logf("seed %d groups=%d pairs=%d skipped=%d verify_reads=%d identical", seed, len(wantGroups), wantPairs, wantSkipped, len(calls))
	}
}

func TestMatchClipSequencesStopsWorkersOnCancel(t *testing.T) {
	sequences, excluded, dismissed, pathMode := clipEquivalenceFixture(t, 9, 160)
	// 第一次复核读帧时用户取消：新旧实现都必须以 context.Canceled 结束，且读帧次数相同
	// （正在进行的这一对读完剩余采样点后发现取消，与原先一致）。
	run := func(match func(*CleanupService, context.Context) ([]CleanupClipGroup, error)) ([]string, error) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls []string
		read := clipEquivalenceReader(pathMode, &calls)
		svc := &CleanupService{clipFrame: func(readCtx context.Context, path string, second float64) ([]byte, error) {
			cancel()
			return read(readCtx, path, second)
		}}
		groups, err := match(svc, ctx)
		if groups != nil {
			t.Fatalf("取消后不应返回候选: %v", groups)
		}
		// 已取消的上下文不读帧、不起比较。
		before := len(calls)
		if _, again := match(svc, ctx); !errors.Is(again, context.Canceled) || len(calls) != before {
			t.Fatalf("已取消仍在工作: calls=%d→%d err=%v", before, len(calls), again)
		}
		return calls, err
	}
	wantCalls, wantErr := run(func(s *CleanupService, ctx context.Context) ([]CleanupClipGroup, error) {
		groups, _, _, err := legacyMatchClipSequences(s, ctx, cloneClipSequences(sequences), excluded, dismissed)
		return groups, err
	})
	gotCalls, gotErr := run(func(s *CleanupService, ctx context.Context) ([]CleanupClipGroup, error) {
		groups, _, _, err := s.matchClipSequences(ctx, cloneClipSequences(sequences), excluded, dismissed)
		return groups, err
	})
	if !errors.Is(gotErr, context.Canceled) || !errors.Is(wantErr, context.Canceled) {
		t.Fatalf("取消必须终止整轮: got %v want %v", gotErr, wantErr)
	}
	if len(wantCalls) == 0 || !reflect.DeepEqual(gotCalls, wantCalls) {
		t.Fatalf("取消前后读帧不一致: got %v want %v", gotCalls, wantCalls)
	}
}
