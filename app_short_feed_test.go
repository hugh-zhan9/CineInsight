package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// P-021 评审修复（Minor 3 / Minor 5）：二维码地址由服务端决定；启停经同一把生命周期锁。
func TestGetShortFeedQRCodeTakesNoArgumentAndEmptyWithoutServerPLAY14(t *testing.T) {
	app := &App{}
	dataURL, err := app.GetShortFeedQRCode()
	if err != nil || dataURL != "" {
		t.Fatalf("没有手机端服务时应返回空串: %q err=%v", dataURL, err)
	}
}

func TestWithShortFeedLifecycleSerializesCallersPLAY01(t *testing.T) {
	app := &App{}
	var running, overlap int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = app.withShortFeedLifecycle(func() error {
				if atomic.AddInt32(&running, 1) > 1 {
					atomic.AddInt32(&overlap, 1)
				}
				time.Sleep(5 * time.Millisecond)
				atomic.AddInt32(&running, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	if overlap != 0 {
		t.Fatalf("启停不应并发执行，重叠 %d 次", overlap)
	}
}
