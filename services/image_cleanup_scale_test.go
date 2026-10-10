package services

import (
	"context"
	"fmt"
	"testing"
)

func imageCleanupScaleStates(count int) []imageCleanupFileState {
	states := make([]imageCleanupFileState, count)
	for i := range states {
		states[i] = imageCleanupHashState(uint(i+1), "copy.jpg", fmt.Sprintf("%016x", uint64(i/2)*0x9e3779b97f4a7c15))
	}
	return states
}

func TestImageCleanupLargeRecallAllocationBudget(t *testing.T) {
	states := imageCleanupScaleStates(4000)
	svc := newImageCleanupTestService()
	var groups []ImageCleanupDuplicateGroup
	allocs := testing.AllocsPerRun(1, func() {
		groups, _ = svc.buildNearDuplicateGroups(context.Background(), states, nil)
	})
	if len(groups) != len(states)/2 {
		t.Fatalf("large recall lost duplicate pairs: got %d groups", len(groups))
	}
	for _, group := range groups {
		if len(group.Candidates) != 1 || group.Candidates[0].ID != group.Original.ID+1 {
			t.Fatalf("unexpected pair: %v", imageCleanupGroupIDs(group))
		}
	}
	// 包含结果对象与画面缓存；候选预算填满后，不应为每张图反复分配整套查找表。
	if allocs > float64(len(states)*25) {
		t.Fatalf("large recall allocated %.0f objects for %d images (budget 25/image)", allocs, len(states))
	}
}

func BenchmarkImageCleanup100K(b *testing.B) {
	states := imageCleanupScaleStates(100000)
	svc := newImageCleanupTestService()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.buildNearDuplicateGroups(context.Background(), states, nil)
	}
}
