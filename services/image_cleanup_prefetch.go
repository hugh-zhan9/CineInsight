package services

import (
	"context"
	"runtime"
	"sync"
)

const (
	// imageCleanupPrefetchWindow 是预取最多领先复核的张数，远低于画面证据缓存（4096）与
	// 缩略图磁盘缓存（约 256 MB、五千到八千张）的容量：不一次性预取整批，免得把自己挤掉。
	imageCleanupPrefetchWindow = 1024
	// 同一卷上的并发读取数；卷未知（无 st_dev）时只开一个，不放大对未知设备的压力。
	imageCleanupPrefetchPerDevice  = 3
	imageCleanupPrefetchPerUnknown = 1
)

// imageCleanupPrefetchTotalCap 是跨全部卷同时进行的预取读取上限：同一台 NAS 的多个共享会被
// 识别成多个卷，每卷 3 路叠加起来可能同时拉起过多 ffmpeg/sips，进而让复核读取撞上超时。
func imageCleanupPrefetchTotalCap() int {
	return max(2, min(runtime.NumCPU()/2, 6))
}

type imageCleanupPrefetchStatus int

const (
	prefetchQueued imageCleanupPrefetchStatus = iota
	prefetchRunning
	prefetchReady
	// prefetchDone：预取失败、已被复核取走，或复核已自行同步读取；不再提供结果。
	prefetchDone
)

type imageCleanupPrefetchSlot struct {
	state    imageCleanupFileState
	neededAt int
	status   imageCleanupPrefetchStatus
	visual   *imageCleanupVisual
	finished chan struct{}
}

type imageCleanupDeviceKey struct {
	known  bool
	device uint64
}

// imageCleanupVisualPrefetch 按复核将要读取的顺序，在有界窗口内提前读取画面证据。
//
// 它只是读取的提前量，不参与判定：复核仍在原 goroutine 里按原顺序调用缓存的 get，
// 缓存未命中时取走已成功的预取结果，当作自己刚读到的；预取失败、尚未开始或不在计划里
// 的图片照旧同步读取并按原规则记失败。因此成组、跳过计数与 LRU 次序都与不预取时相同；
// 唯一差别是读取时刻提前，分析期间文件被改动时可能看到改动前的画面（复核读取本身仍核对
// 首轮 stat 的大小与 mtime），以及预测多读的图片会多生成几张缩略图。
type imageCleanupVisualPrefetch struct {
	read     imageCleanupVisualReader
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	cond     *sync.Cond
	position int
	slots    map[uint]*imageCleanupPrefetchSlot
	pending  []uint
	stopped  bool
	queues   map[imageCleanupDeviceKey]chan uint
	// running 限制跨卷同时读取的总数（容量即 imageCleanupPrefetchTotalCap）。
	running chan struct{}
	wg      sync.WaitGroup
}

func startImageCleanupVisualPrefetch(ctx context.Context, read imageCleanupVisualReader, valid []imageCleanupHashEntry, excluded map[[2]uint]struct{}) *imageCleanupVisualPrefetch {
	prefetchCtx, cancel := context.WithCancel(ctx)
	p := &imageCleanupVisualPrefetch{
		read: read, ctx: prefetchCtx, cancel: cancel,
		slots: make(map[uint]*imageCleanupPrefetchSlot), queues: make(map[imageCleanupDeviceKey]chan uint),
		running: make(chan struct{}, imageCleanupPrefetchTotalCap()),
	}
	p.cond = sync.NewCond(&p.mu)
	// 等窗口的计划协程由 stop 唤醒；复核在任何返回路径上都会调用 stop。
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.plan(valid, excluded)
	}()
	return p
}

// plan 按与复核相同的召回与廉价排除预测读取顺序：每个召回候选（其所在组的代表）先读，
// 再读当前这张；同一张只预取一次。预测落空只是白读或少读，不影响判定。
func (p *imageCleanupVisualPrefetch) plan(valid []imageCleanupHashEntry, excluded map[[2]uint]struct{}) {
	recall := newImageCleanupRecall()
	planned := make([]bool, len(valid))
	for index, entry := range valid {
		if p.ctx.Err() != nil {
			return
		}
		for _, other := range recall.next(index, entry.hash) {
			left := valid[other]
			if !imageCleanupPairCheaplyCompatible(left, entry, excluded) ||
				imageCleanupFirstFrameOnly(left.state) || imageCleanupFirstFrameOnly(entry.state) {
				continue
			}
			for _, target := range [2]int{other, index} {
				if planned[target] {
					continue
				}
				planned[target] = true
				if !p.enqueue(valid[target].state, index) {
					return
				}
			}
		}
	}
}

// enqueue 等窗口有空位后登记并派给所在卷的队列；停止时返回 false。
func (p *imageCleanupVisualPrefetch) enqueue(state imageCleanupFileState, neededAt int) bool {
	p.mu.Lock()
	for len(p.pending) >= imageCleanupPrefetchWindow && !p.stopped {
		p.cond.Wait()
	}
	if p.stopped {
		p.mu.Unlock()
		return false
	}
	if neededAt < p.position {
		// 复核已经越过这一张的预计读取点，再读也用不上。
		p.mu.Unlock()
		return true
	}
	id := state.image.ID
	p.slots[id] = &imageCleanupPrefetchSlot{state: state, neededAt: neededAt, finished: make(chan struct{})}
	p.pending = append(p.pending, id)
	queue := p.queueLocked(imageCleanupDeviceKey{known: state.deviceKnown, device: state.device})
	p.mu.Unlock()
	select {
	case queue <- id:
		return true
	case <-p.ctx.Done():
		return false
	}
}

// queueLocked 返回卷的队列，首次见到某个卷时为它起固定数量的读取协程。调用方持有 mu。
func (p *imageCleanupVisualPrefetch) queueLocked(key imageCleanupDeviceKey) chan uint {
	if queue, ok := p.queues[key]; ok {
		return queue
	}
	queue := make(chan uint, imageCleanupPrefetchWindow)
	p.queues[key] = queue
	workers := imageCleanupPrefetchPerDevice
	if !key.known {
		workers = imageCleanupPrefetchPerUnknown
	}
	for range workers {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.work(queue)
		}()
	}
	return queue
}

// work 读取队列里仍在计划内、复核还没自己读过的图片。读到的证据只在成功时留给复核。
func (p *imageCleanupVisualPrefetch) work(queue chan uint) {
	for {
		var id uint
		select {
		case <-p.ctx.Done():
			return
		case id = <-queue:
		}
		select {
		case p.running <- struct{}{}:
		case <-p.ctx.Done():
			return
		}
		p.mu.Lock()
		slot := p.slots[id]
		if slot == nil || slot.status != prefetchQueued {
			p.mu.Unlock()
			<-p.running
			continue
		}
		slot.status = prefetchRunning
		p.mu.Unlock()
		visual, err := p.read(p.ctx, slot.state)
		<-p.running
		p.mu.Lock()
		if err == nil && visual != nil {
			slot.status, slot.visual = prefetchReady, visual
		} else {
			// 失败不替复核下结论：复核会自己重读，并按原规则记失败或跳过。
			slot.status = prefetchDone
		}
		close(slot.finished)
		p.mu.Unlock()
	}
}

// take 由复核在缓存未命中时调用：返回已成功预取的证据并把它交出（只交一次）。
// 正在读的等它读完；还在排队的标记为不再预取，由复核同步读取。p 为 nil 时不预取。
func (p *imageCleanupVisualPrefetch) take(ctx context.Context, id uint) (*imageCleanupVisual, bool) {
	if p == nil {
		return nil, false
	}
	p.mu.Lock()
	slot := p.slots[id]
	if slot == nil {
		p.mu.Unlock()
		return nil, false
	}
	switch slot.status {
	case prefetchQueued:
		slot.status = prefetchDone
		p.mu.Unlock()
		return nil, false
	case prefetchRunning:
		p.mu.Unlock()
		select {
		case <-slot.finished:
		case <-ctx.Done():
			return nil, false
		}
		p.mu.Lock()
	}
	visual, ready := slot.visual, slot.status == prefetchReady
	slot.status, slot.visual = prefetchDone, nil
	p.mu.Unlock()
	return visual, ready
}

// advance 告知复核已进行到第 position 张：预计读取点在它之前的预取全部作废，腾出窗口。
func (p *imageCleanupVisualPrefetch) advance(position int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.position = position
	for len(p.pending) > 0 {
		// 同一图片在计划里只出现一次（valid 按 id 唯一）；槽位缺失时也只是把它从 pending 里移走。
		if slot := p.slots[p.pending[0]]; slot != nil && slot.neededAt >= position {
			break
		}
		delete(p.slots, p.pending[0])
		p.pending = p.pending[1:]
	}
	p.cond.Broadcast()
	p.mu.Unlock()
}

// stop 停止计划与全部读取协程并等它们退出；返回后不留 goroutine。
func (p *imageCleanupVisualPrefetch) stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.stopped = true
	p.cond.Broadcast()
	p.mu.Unlock()
	p.cancel()
	p.wg.Wait()
}
