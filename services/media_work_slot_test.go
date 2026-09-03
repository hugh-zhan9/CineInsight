package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 容量 1：第二个 Acquire 必须等到第一个 Release 之后才拿得到。
func TestMediaWorkSlotSerializesHolders(t *testing.T) {
	slot := NewMediaWorkSlot()
	if err := slot.Acquire(context.Background()); err != nil {
		t.Fatalf("第一次获取槽位失败: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		if err := slot.Acquire(context.Background()); err != nil {
			return
		}
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("槽位被占用时不该放行第二个持有者")
	case <-time.After(30 * time.Millisecond):
	}

	slot.Release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("释放后第二个持有者应立即拿到槽位")
	}
	slot.Release()
}

// 等待中被取消：返回 ctx.Err()，并且不能偷偷占着槽位。
func TestMediaWorkSlotAcquireHonorsCancellation(t *testing.T) {
	slot := NewMediaWorkSlot()
	if err := slot.Acquire(context.Background()); err != nil {
		t.Fatalf("获取槽位失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- slot.Acquire(ctx) }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后应返回 context.Canceled，实际 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 Acquire 应立即返回")
	}
	// 前一个等待者没有占住槽位：释放后立刻能再拿到。
	slot.Release()
	if err := slot.Acquire(context.Background()); err != nil {
		t.Fatalf("释放后应能重新获取槽位: %v", err)
	}
	slot.Release()
}

// 已取消的 ctx 一律不放行，避免"取消了还抢到槽"。
func TestMediaWorkSlotRejectsAlreadyCancelledContext(t *testing.T) {
	slot := NewMediaWorkSlot()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := slot.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的 ctx 应被拒绝，实际 %v", err)
	}
	if err := slot.Acquire(context.Background()); err != nil {
		t.Fatalf("槽位应仍然空闲: %v", err)
	}
	slot.Release()
}

// 没持有就释放是配对错误，必须炸出来而不是静默多出一格。
func TestMediaWorkSlotReleaseWithoutAcquirePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("未配对的 Release 应当 panic")
		}
	}()
	NewMediaWorkSlot().Release()
}
