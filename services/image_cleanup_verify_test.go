package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/bits"
	"os"
	"path/filepath"
	"testing"
	"video-master/models"
)

func cleanupPattern(size int, reversed bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			slot := (y * 128 / size / 8) % 2
			if reversed {
				slot = 1 - slot
			}
			v := byte(40 + x*64/size + slot*50)
			img.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return img
}

func TestImageCleanupVisualVerification(t *testing.T) {
	original := cleanupPattern(128, false)
	other := cleanupPattern(128, true)
	if d := bits.OnesCount64(differenceHash(original, original.Bounds()) ^ differenceHash(other, other.Bounds())); d != 0 {
		t.Fatalf("反例必须是不同内容而 dHash 相同，实际距离 %d", d)
	}
	var compressed bytes.Buffer
	if err := jpeg.Encode(&compressed, original, &jpeg.Options{Quality: 65}); err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	tinted := cleanupPattern(128, false)
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			c := tinted.RGBAAt(x, y)
			c.R += 60
			c.B -= 30
			tinted.SetRGBA(x, y, c)
		}
	}
	flat := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for _, tc := range []struct {
		name string
		a, b image.Image
		want bool
	}{
		{"same", original, original, true},
		{"resized", original, cleanupPattern(256, false), true},
		{"compressed", original, decoded, true},
		{"same_hash_different_structure", original, other, false},
		{"same_structure_different_color", original, tinted, false},
		{"flat_has_no_evidence", flat, flat, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := imageCleanupVisualsMatch(imageCleanupVisualFromImage(tc.a), imageCleanupVisualFromImage(tc.b)); got != tc.want {
				t.Fatalf("match=%v want=%v", got, tc.want)
			}
		})
	}
	left, right := imageCleanupVisualFromImage(original), imageCleanupVisualFromImage(original)
	right.aspect = 1.3
	if imageCleanupVisualsMatch(left, right) {
		t.Fatal("比例不同不能因被压成方形而视为同图")
	}
}

type cleanupThumbnailResolverFunc func(context.Context, uint) (*ImageMedia, error)

func (fn cleanupThumbnailResolverFunc) ResolveImageThumbnail(ctx context.Context, id uint) (*ImageMedia, error) {
	return fn(ctx, id)
}

func TestImageCleanupVerifiesRealPicturesEndToEnd(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	paths := make(map[uint]string)
	var ids []uint
	for index, pic := range []image.Image{cleanupPattern(128, false), cleanupPattern(256, false), cleanupPattern(128, true)} {
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, pic); err != nil {
			t.Fatal(err)
		}
		img := imageCleanupCreateImage(t, filepath.Join(dir, fmt.Sprintf("%d.png", index)), encoded.Bytes(), fmt.Sprintf("%016x", differenceHash(pic, pic.Bounds())), pic.Bounds().Dx(), pic.Bounds().Dy())
		paths[img.ID] = img.Path
		ids = append(ids, img.ID)
	}
	svc := NewImageCleanupService(cleanupThumbnailResolverFunc(func(_ context.Context, id uint) (*ImageMedia, error) { return &ImageMedia{Path: paths[id]}, nil }))
	result, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.NearDuplicateGroups) != 1 || len(result.NearDuplicateGroups[0].Candidates) != 1 {
		t.Fatalf("只应包含同内容缩放的一对: %+v", result)
	}
	members := imageCleanupGroupIDs(result.NearDuplicateGroups[0])
	if !imageCleanupContainsID(members, ids[0]) || !imageCleanupContainsID(members, ids[1]) || imageCleanupContainsID(members, ids[2]) {
		t.Fatalf("误判图片成员: %v", members)
	}
	// 解码失败不再根据 dHash 给出候选，并向界面报告。
	svc = NewImageCleanupService(cleanupThumbnailResolverFunc(func(context.Context, uint) (*ImageMedia, error) { return nil, errors.New("unreadable") }))
	result, err = svc.AnalyzeImageCleanupCandidates()
	if err != nil || len(result.NearDuplicateGroups) != 0 || result.SkippedVerification == 0 {
		t.Fatalf("失败不得绕过画面复核: %+v %v", result, err)
	}
}

func TestImageCleanupVerificationSourceChangeAndCancellation(t *testing.T) {
	setupImageServiceTestDB(t)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, cleanupPattern(128, false)); err != nil {
		t.Fatal(err)
	}
	img := imageCleanupCreateImage(t, filepath.Join(t.TempDir(), "a.png"), encoded.Bytes(), "", 128, 128)
	info, _ := os.Stat(img.Path)
	state := imageCleanupFileState{image: *img, size: info.Size(), modTimeNS: info.ModTime().UnixNano()}
	reader := imageCleanupThumbnailReader(cleanupThumbnailResolverFunc(func(context.Context, uint) (*ImageMedia, error) {
		if err := os.WriteFile(img.Path, append(encoded.Bytes(), 1), 0644); err != nil {
			return nil, err
		}
		return &ImageMedia{Path: img.Path}, nil
	}))
	if _, err := reader(context.Background(), state); err == nil {
		t.Fatal("解码期间源文件变化应拒绝证据")
	}
	if _, err := imageCleanupFullHash(context.Background(), state); err == nil {
		t.Fatal("旧版本不得复用完整哈希")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := imageCleanupFullHash(ctx, state); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消完整哈希: %v", err)
	}
	if _, err := reader(ctx, state); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消画面读取: %v", err)
	}
}

func TestImageCleanupVisualCacheBoundAndGIF(t *testing.T) {
	calls := 0
	visual := imageCleanupVisualFromImage(cleanupPattern(128, false))
	cache := newImageCleanupVisualCache(func(context.Context, imageCleanupFileState) (*imageCleanupVisual, error) { calls++; return visual, nil })
	for id := uint(1); id <= 1000; id++ {
		cache.get(context.Background(), imageCleanupFileState{image: models.Image{ID: id}})
	}
	if len(cache.items) != 256 {
		t.Fatalf("画面缓存没有上限: %d", len(cache.items))
	}
	cache.get(context.Background(), imageCleanupFileState{image: models.Image{ID: 1000}})
	if calls != 1000 {
		t.Fatal("最近的画面应复用")
	}
	a := imageCleanupFileState{image: models.Image{ID: 1001, Format: "gif"}}
	b := imageCleanupFileState{image: models.Image{ID: 1002, Format: "gif"}}
	if cache.matches(context.Background(), a, b) || calls != 1000 {
		t.Fatal("GIF 不能凭首帧给出近似重复")
	}
}

func TestImageCleanupLargeExactGroupUsesNoVisualReads(t *testing.T) {
	svc := newImageCleanupTestService()
	svc.readVisual = func(context.Context, imageCleanupFileState) (*imageCleanupVisual, error) {
		t.Fatal("同精确组不需要读取画面")
		return nil, nil
	}
	states := make([]imageCleanupFileState, 10000)
	for i := range states {
		states[i] = imageCleanupHashState(uint(i+1), "copy.jpg", "abcd000000000000")
		states[i].exactGroup = 1
	}
	groups, stale := svc.buildNearDuplicateGroups(context.Background(), states, nil)
	if len(groups) != 0 || stale != 0 {
		t.Fatalf("精确副本不应出近似组: %d %d", len(groups), stale)
	}
}

func TestImageCleanupNearGroupSizeIsBounded(t *testing.T) {
	svc := newImageCleanupTestService()
	states := make([]imageCleanupFileState, 131)
	for i := range states {
		states[i] = imageCleanupHashState(uint(i+1), "copy.jpg", "abcd000000000000")
	}
	groups, _ := svc.buildNearDuplicateGroups(context.Background(), states, nil)
	for _, group := range groups {
		if n := len(group.Candidates) + 1; n > imageCleanupMaxGroupMembers {
			t.Fatalf("组无上限: %d", n)
		}
	}
	if len(groups) != 2 {
		t.Fatalf("应拆成两个完整组，末尾单张不报告: %d", len(groups))
	}
}

func TestImageCleanupAnimatedPicturesDoNotMatchByFirstFrame(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	var firstHash uint64
	for i, name := range []string{"animated-a.png", "animated-b.png", "animated-a.png"} {
		blob, err := os.ReadFile(filepath.Join("testdata", "image_cleanup", name))
		if err != nil {
			t.Fatal(err)
		}
		firstFrame, err := png.Decode(bytes.NewReader(blob))
		if err != nil {
			t.Fatal(err)
		}
		hash := differenceHash(firstFrame, firstFrame.Bounds())
		if i == 0 {
			firstHash = hash
		} else if hash != firstHash {
			t.Fatal("动图反例的首帧必须相同")
		}
		imageCleanupCreateImage(t, filepath.Join(dir, fmt.Sprintf("%d.png", i)), blob, fmt.Sprintf("%016x", hash), 128, 128)
	}
	svc := NewImageCleanupService(cleanupThumbnailResolverFunc(func(context.Context, uint) (*ImageMedia, error) {
		t.Fatal("动画不能按首帧缩略图复核")
		return nil, nil
	}))
	result, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.NearDuplicateGroups) != 0 || len(result.DuplicateGroups) != 1 {
		t.Fatalf("不同动画不应成近似组，完整副本仍可识别: %+v", result)
	}
}

func TestImageCleanupAnimationContainerHeaders(t *testing.T) {
	webp := make([]byte, 30)
	copy(webp, "RIFF")
	binary.LittleEndian.PutUint32(webp[4:], 22)
	copy(webp[8:], "WEBPVP8X")
	binary.LittleEndian.PutUint32(webp[16:], 10)
	for _, animated := range []bool{false, true} {
		if animated {
			webp[20] = 0x02
		}
		path := filepath.Join(t.TempDir(), "photo.webp")
		if err := os.WriteFile(path, webp, 0644); err != nil {
			t.Fatal(err)
		}
		got, err := imageCleanupAnimated(context.Background(), path)
		if err != nil || got != animated {
			t.Fatalf("WebP animated=%v got=%v err=%v", animated, got, err)
		}
	}
	truncated := filepath.Join(t.TempDir(), "bad.png")
	if err := os.WriteFile(truncated, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x10"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := imageCleanupAnimated(context.Background(), truncated); err == nil {
		t.Fatal("截断容器不能当成已确认静态")
	}
}
