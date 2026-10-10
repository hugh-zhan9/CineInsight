package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// blockingSession 在 EmbedTexts 里跑 during 钩子，模拟"请求进行中"。
type blockingSession struct {
	fakeSession
	during func()
}

func (b *blockingSession) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if b.during != nil {
		b.during()
	}
	return b.fakeSession.EmbedTexts(ctx, texts)
}

// M-1：空闲定时器恰好在 use() 停表之前触发时，迟到的 closeIdle 不能关掉正在使用的会话。
func TestSceneWorkerHostIdleCloseNeverHitsInUseSession(t *testing.T) {
	session := &blockingSession{fakeSession: fakeSession{id: 1}}
	host := newSceneWorkerHost(func(context.Context) (sceneEmbedder, error) { return session, nil })
	host.idle = time.Hour
	if _, err := host.EmbedTexts(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	armed := host.generation
	host.mu.Unlock()
	// 定时器已经"触发"、closeIdle 正排队等锁：它带着上一次用完时的代次。
	session.during = func() { host.closeIdle(armed) }
	if _, err := host.EmbedTexts(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if session.closedCount() != 0 {
		t.Fatal("正在使用的会话被空闲关闭了")
	}
	host.Close()
}

// M-1：会话被关闭时，进行中的请求要立刻以 gone 返回，不能因为读协程选了 done 而干等到超时。
func TestSceneWorkerProcessRequestReturnsPromptlyWhenClosed(t *testing.T) {
	previous := sceneWorkerRequestTimeout
	sceneWorkerRequestTimeout = 4 * time.Second
	defer func() { sceneWorkerRequestTimeout = previous }()
	for attempt := 0; attempt < 6; attempt++ {
		session, err := startHelperSceneWorker(t, "hang")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var requestErr error
		started := time.Now()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, requestErr = session.EmbedTexts(context.Background(), []string{"x"})
		}()
		time.Sleep(50 * time.Millisecond)
		_ = session.Close()
		wg.Wait()
		if elapsed := time.Since(started); elapsed > 2500*time.Millisecond || !errors.Is(requestErr, errSceneWorkerGone) {
			t.Fatalf("第 %d 次：关闭后请求应立刻以 gone 返回，实际 %v 后返回 %v", attempt+1, elapsed, requestErr)
		}
	}
}

// M-7：worker 输出超长的一行属于协议错误，会话作废，而不是被整行读进内存再解析。
func TestSceneWorkerProcessRejectsOverlongLine(t *testing.T) {
	session, err := startHelperSceneWorker(t, "huge")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.EmbedTexts(context.Background(), []string{"x"}); !errors.Is(err, errSceneWorkerGone) {
		t.Fatalf("超长输出应让会话作废: %v", err)
	}
	if !session.(*sceneWorkerProcess).Gone() {
		t.Fatal("超长输出后会话应标记为作废")
	}
}
