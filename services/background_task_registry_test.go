package services

import (
	"reflect"
	"sync"
	"testing"
)

// 快照顺序固定为面板顺序，与 Begin 的先后无关：前端按它渲染，顺序抖动会让角标
// 与列表每次刷新都跳一下。
func TestBackgroundTaskRegistrySnapshotOrderIsStable(t *testing.T) {
	registry := NewBackgroundTaskRegistry()
	registry.Begin(BackgroundTaskBackup)
	registry.Begin(BackgroundTaskSubtitle)
	registry.Begin(BackgroundTaskPerceptualHash)

	want := []string{"subtitle", "phash", "backup"}
	if got := registry.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("快照顺序错误: got=%v want=%v", got, want)
	}
}

// 同一个 key 被两条 worker 同时登记（本地元数据的补全与写出）：任一在跑就算在跑，
// 两次 End 之后才清空。
func TestBackgroundTaskRegistryCountsRepeatedKey(t *testing.T) {
	registry := NewBackgroundTaskRegistry()
	registry.Begin(BackgroundTaskLocalMetadata)
	registry.Begin(BackgroundTaskLocalMetadata)
	registry.End(BackgroundTaskLocalMetadata)
	if got := registry.Snapshot(); !reflect.DeepEqual(got, []string{"local_metadata"}) {
		t.Fatalf("还有一条在跑时不该从快照消失: %v", got)
	}
	registry.End(BackgroundTaskLocalMetadata)
	if got := registry.Snapshot(); len(got) != 0 {
		t.Fatalf("全部结束后快照应为空: %v", got)
	}
}

// 并发 Begin/End 配对（defer 形态）之后计数必须归零，不能漂移。
func TestBackgroundTaskRegistryConcurrentBeginEndLeavesNoDrift(t *testing.T) {
	registry := NewBackgroundTaskRegistry()
	keys := []BackgroundTaskKey{
		BackgroundTaskSubtitle, BackgroundTaskEnhancement, BackgroundTaskPerceptualHash,
		BackgroundTaskTechnical, BackgroundTaskLocalMetadata, BackgroundTaskCleanup,
	}
	var wg sync.WaitGroup
	for _, key := range keys {
		for round := 0; round < 50; round++ {
			wg.Add(1)
			go func(key BackgroundTaskKey) {
				defer wg.Done()
				registry.Begin(key)
				defer registry.End(key)
			}(key)
		}
	}
	wg.Wait()
	if got := registry.Snapshot(); len(got) != 0 {
		t.Fatalf("并发配对后应无残留: %v", got)
	}
}

// 变化回调是角标与 background-tasks 事件的唯一数据源：每次增删都要回调，
// 且拿到的必须是变化之后的快照。
func TestBackgroundTaskRegistryNotifiesOnChange(t *testing.T) {
	registry := NewBackgroundTaskRegistry()
	var mu sync.Mutex
	var seen [][]string
	registry.SetOnChange(func(running []string) {
		mu.Lock()
		seen = append(seen, running)
		mu.Unlock()
	})
	registry.Begin(BackgroundTaskEXIF)
	registry.End(BackgroundTaskEXIF)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("应回调两次，实际 %d 次: %v", len(seen), seen)
	}
	if !reflect.DeepEqual(seen[0], []string{"exif"}) {
		t.Fatalf("Begin 后的快照错误: %v", seen[0])
	}
	if len(seen[1]) != 0 {
		t.Fatalf("End 后的快照应为空: %v", seen[1])
	}
}

// 多余的 End 是配对错误：静默吞掉会让角标永远挂着一个不存在的任务。
func TestBackgroundTaskRegistryUnbalancedEndPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("未配对的 End 应当 panic")
		}
	}()
	NewBackgroundTaskRegistry().End(BackgroundTaskSubtitle)
}

// key 集合是固定的（D-014）：写错一个字符必须立刻炸，而不是悄悄丢进快照缝里。
func TestBackgroundTaskRegistryRejectsUnknownKey(t *testing.T) {
	if IsBackgroundTaskKey("not_a_task") {
		t.Fatal("未知 key 不该被认作合法")
	}
	if len(BackgroundTaskKeys()) != 16 {
		t.Fatalf("固定 key 集合应有 16 项，实际 %d", len(BackgroundTaskKeys()))
	}
	defer func() {
		if recover() == nil {
			t.Fatal("未知 key 应当 panic")
		}
	}()
	NewBackgroundTaskRegistry().Begin(BackgroundTaskKey("not_a_task"))
}

// nil 登记表是允许的：没接入登记表的服务（单测夹具）照常工作。
func TestBackgroundTaskRegistryNilIsNoop(t *testing.T) {
	var registry *BackgroundTaskRegistry
	registry.Begin(BackgroundTaskSubtitle)
	registry.End(BackgroundTaskSubtitle)
	if got := registry.Snapshot(); len(got) != 0 {
		t.Fatalf("nil 登记表的快照应为空: %v", got)
	}
}
