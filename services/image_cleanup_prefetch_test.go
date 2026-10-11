package services

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// prefetchFixture 生成近似重复复核的随机夹具：连拍组（含同一精确组的副本）、截图式的
// 近哈希异内容、GIF、已忽略的配对，以及分布在两个卷和"未知卷"上的图片。
func prefetchFixture(seed uint64, count int) ([]imageCleanupFileState, map[[2]uint]struct{}, map[uint]int) {
	rng := rand.New(rand.NewPCG(seed, seed^0xfeed))
	protos := make([]uint64, 400)
	for index := range protos {
		protos[index] = rng.Uint64()
	}
	states := make([]imageCleanupFileState, 0, count)
	content := make(map[uint]int, count)
	excluded := make(map[[2]uint]struct{})
	nextContent := 0
	for len(states) < count {
		nextContent++
		base := protos[rng.IntN(len(protos))]
		size := 1
		if rng.IntN(4) == 0 {
			size = 2 + rng.IntN(6)
		}
		exact := uint(0)
		if rng.IntN(10) == 0 {
			exact = uint(len(states) + 1)
		}
		for member := 0; member < size && len(states) < count; member++ {
			id := uint(len(states) + 1)
			hash := equivalenceFlip(rng, base, rng.IntN(5))
			state := imageCleanupHashState(id, fmt.Sprintf("p%d.jpg", id), fmt.Sprintf("%016x", hash))
			if rng.IntN(50) == 0 {
				state.image.Format = "gif"
			}
			state.exactGroup = exact
			state.device, state.deviceKnown = uint64(id%2), id%7 != 0
			itemContent := nextContent
			if rng.IntN(8) == 0 {
				itemContent = -int(id) // 指纹接近但画面不同
			}
			content[id] = itemContent
			if member > 0 && rng.IntN(12) == 0 {
				excluded[imageCleanupPairKey(id-1, id)] = struct{}{}
			}
			states = append(states, state)
		}
	}
	return states, excluded, content
}

// prefetchReader 是线程安全的确定性画面读取替身：同内容画面相同（加少量噪声），部分 ID
// 恒定读失败，每次读取有随机延迟并响应取消。
func prefetchReader(content map[uint]int, reads *atomic.Int64) imageCleanupVisualReader {
	return func(ctx context.Context, state imageCleanupFileState) (*imageCleanupVisual, error) {
		reads.Add(1)
		id := state.image.ID
		rng := rand.New(rand.NewPCG(uint64(id), 7))
		select {
		case <-time.After(time.Duration(rng.IntN(300)) * time.Microsecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if id%13 == 0 {
			return nil, fmt.Errorf("decode failure %d", id)
		}
		base := rand.New(rand.NewPCG(uint64(content[id]+1_000_000), 3))
		visual := &imageCleanupVisual{aspect: 1.5}
		for index := range visual.rgb {
			visual.rgb[index] = byte(base.IntN(200) + rng.IntN(4))
		}
		return visual, nil
	}
}

func runPrefetchVerification(t *testing.T, states []imageCleanupFileState, excluded map[[2]uint]struct{}, content map[uint]int, prefetch bool, capacity int) ([]ImageCleanupDuplicateGroup, int64, map[uint]struct{}, int64) {
	t.Helper()
	var reads atomic.Int64
	svc := NewImageCleanupService(nil)
	svc.readVisual = prefetchReader(content, &reads)
	cache := newImageCleanupVisualCache(svc.readVisual)
	cache.prefetchReads = prefetch
	cache.capacity = capacity
	groups, stale := svc.buildVerifiedNearDuplicateGroups(context.Background(), states, excluded, cache)
	if cache.prefetch != nil {
		t.Fatal("复核结束后预取器必须已停止")
	}
	return groups, stale, cache.failed, reads.Load()
}

// 预取开与关、证据缓存大与小，成组、待补全计数与复核失败集合都必须完全相同。
func TestImageCleanupPrefetchAndCacheSizeKeepVerdicts(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		states, excluded, content := prefetchFixture(seed, 2500)
		wantGroups, wantStale, wantFailed, sequentialReads := runPrefetchVerification(t, states, excluded, content, false, imageCleanupVisualCacheCapacity)
		for _, variant := range []struct {
			name     string
			prefetch bool
			capacity int
		}{{"prefetch", true, imageCleanupVisualCacheCapacity}, {"small-cache", false, 16}, {"prefetch-small-cache", true, 16}, {"legacy-256", false, 256}} {
			groups, stale, failed, reads := runPrefetchVerification(t, states, excluded, content, variant.prefetch, variant.capacity)
			if !reflect.DeepEqual(groups, wantGroups) || stale != wantStale || !reflect.DeepEqual(failed, wantFailed) {
				t.Fatalf("seed %d %s 结果不一致: groups %d/%d stale %d/%d failed %d/%d", seed, variant.name, len(groups), len(wantGroups), stale, wantStale, len(failed), len(wantFailed))
			}
			t.Logf("seed %d %s reads=%d (sequential %d)", seed, variant.name, reads, sequentialReads)
		}
		if len(wantGroups) < 20 || len(wantFailed) == 0 {
			t.Fatalf("seed %d 夹具覆盖不足: groups=%d failed=%d", seed, len(wantGroups), len(wantFailed))
		}
	}
}

// 取消时复核立即返回，预取的计划与读取协程全部退出，不留 goroutine。
func TestImageCleanupPrefetchStopsPromptlyOnCancel(t *testing.T) {
	states, excluded, content := prefetchFixture(4, 2500)
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reads atomic.Int64
	read := prefetchReader(content, &reads)
	svc := NewImageCleanupService(nil)
	svc.readVisual = func(readCtx context.Context, state imageCleanupFileState) (*imageCleanupVisual, error) {
		if reads.Load() >= 200 {
			cancel()
		}
		return read(readCtx, state)
	}
	cache := newImageCleanupVisualCache(svc.readVisual)
	cache.prefetchReads = true
	started := time.Now()
	groups, _ := svc.buildVerifiedNearDuplicateGroups(ctx, states, excluded, cache)
	if groups != nil || ctx.Err() == nil {
		t.Fatalf("取消后不应返回结果: %d", len(groups))
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("取消后迟迟不返回: %v", elapsed)
	}
	if cache.prefetch != nil {
		t.Fatal("预取器未停止")
	}
	stoppedAt := reads.Load()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if now := runtime.NumGoroutine(); now > before {
		t.Fatalf("goroutine 泄漏: before=%d after=%d", before, now)
	}
	if reads.Load() != stoppedAt {
		t.Fatal("返回后仍有读取")
	}
}

// 预取最多领先复核 imageCleanupPrefetchWindow 张：复核不前进时计划停在窗口上，
// 复核前进后才继续；stop 能唤醒停在窗口上的计划。
func TestImageCleanupPrefetchWindowIsBounded(t *testing.T) {
	count := imageCleanupPrefetchWindow*2 + 100
	valid := make([]imageCleanupHashEntry, count)
	for index := range valid {
		id := uint(index + 1)
		// 全部同一指纹：每张都召回前面的图，计划会想预取全部。
		valid[index] = imageCleanupHashEntry{state: imageCleanupHashState(id, fmt.Sprintf("w%d.jpg", id), "abcd000000000000"), hash: 0xabcd000000000000}
		valid[index].state.deviceKnown = true
	}
	var reads atomic.Int64
	p := startImageCleanupVisualPrefetch(context.Background(), prefetchReader(map[uint]int{}, &reads), valid, nil)
	outstanding := func() (int, int) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.pending), len(p.slots)
	}
	deadline := time.Now().Add(5 * time.Second)
	for pending, _ := outstanding(); pending < imageCleanupPrefetchWindow && time.Now().Before(deadline); pending, _ = outstanding() {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if pending, slots := outstanding(); pending != imageCleanupPrefetchWindow || slots != imageCleanupPrefetchWindow || reads.Load() > imageCleanupPrefetchWindow {
		t.Fatalf("窗口没有上限: pending=%d slots=%d reads=%d", pending, slots, reads.Load())
	}
	p.advance(imageCleanupPrefetchWindow / 2)
	deadline = time.Now().Add(5 * time.Second)
	for pending, _ := outstanding(); pending <= imageCleanupPrefetchWindow/2+10 && time.Now().Before(deadline); pending, _ = outstanding() {
		time.Sleep(5 * time.Millisecond)
	}
	if pending, _ := outstanding(); pending > imageCleanupPrefetchWindow || pending <= imageCleanupPrefetchWindow/2 {
		t.Fatalf("前进后应继续预取且仍不超过窗口: %d", pending)
	}
	p.stop()
}

// take 的约定：排队中的交给复核同步读（预取不再读它），正在读的等读完再交，
// 成功的只交一次，预取失败的不交（复核自己重读并按原规则记失败）。
func TestImageCleanupPrefetchTakeContract(t *testing.T) {
	valid := make([]imageCleanupHashEntry, 10)
	for index := range valid {
		id := uint(index + 1)
		valid[index] = imageCleanupHashEntry{state: imageCleanupHashState(id, fmt.Sprintf("t%d.jpg", id), "abcd000000000000"), hash: 0xabcd000000000000}
		valid[index].state.deviceKnown = true
	}
	gate := make(chan struct{})
	var mu sync.Mutex
	called := make(map[uint]int)
	visual := &imageCleanupVisual{aspect: 1}
	p := startImageCleanupVisualPrefetch(context.Background(), func(ctx context.Context, state imageCleanupFileState) (*imageCleanupVisual, error) {
		id := state.image.ID
		mu.Lock()
		called[id]++
		mu.Unlock()
		if id <= imageCleanupPrefetchPerDevice {
			select { // 前三张占住本卷全部读取协程
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if id == 9 {
			return nil, fmt.Errorf("decode failure")
		}
		return visual, nil
	}, valid, nil)
	defer p.stop()
	calls := func(id uint) int {
		mu.Lock()
		defer mu.Unlock()
		return called[id]
	}
	waitFor := func(cond func() bool) {
		deadline := time.Now().Add(5 * time.Second)
		for !cond() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !cond() {
			t.Fatal("等待超时")
		}
	}
	waitFor(func() bool { return calls(1)+calls(2)+calls(3) == 3 })
	if got, ok := p.take(context.Background(), 5); got != nil || ok {
		t.Fatalf("排队中的应交给复核同步读取: %v %v", got, ok)
	}
	close(gate)
	if got, ok := p.take(context.Background(), 1); got != visual || !ok {
		t.Fatalf("正在读的应等读完后交出: %v %v", got, ok)
	}
	if _, ok := p.take(context.Background(), 1); ok {
		t.Fatal("同一份结果只能交一次")
	}
	waitFor(func() bool { return calls(10) == 1 })
	if _, ok := p.take(context.Background(), 9); ok {
		t.Fatal("预取失败不能当作读到")
	}
	if calls(5) != 0 {
		t.Fatal("已交给复核的图片预取不应再读")
	}
}
