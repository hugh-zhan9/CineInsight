package services

import "context"

// MediaWorkSlot 是容量 1 的进程内信号量（D-007）。
//
// 存在的理由：转封装、帧哈希回填、人脸抽帧都是吃满 CPU 的 ffmpeg 重活，
// 三条流水线各自单 worker 并不够——它们互相之间照样会撞在一起。让它们在处理
// 每一项之前共享同一个槽位，同一时刻就只有一个重媒体任务在跑。
//
// 槽只限制并发，不排优先级；等待顺序由 channel 语义保证。
type MediaWorkSlot struct {
	slot chan struct{}
}

// NewMediaWorkSlot 创建一个空闲的槽。零值不可用：必须经此构造。
func NewMediaWorkSlot() *MediaWorkSlot {
	return &MediaWorkSlot{slot: make(chan struct{}, 1)}
}

// Acquire 阻塞直到拿到槽位。ctx 取消时返回 ctx.Err() 且不占用槽位。
// 已取消的 ctx 一律不放行，避免"取消了还抢到槽"的竞态。
func (s *MediaWorkSlot) Acquire(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	select {
	case s.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release 归还槽位。没有持有就释放属于调用方的配对错误，直接 panic：
// 静默吞掉会让槽位凭空多出一格，之后所有并发限制都是假的。
func (s *MediaWorkSlot) Release() {
	select {
	case <-s.slot:
	default:
		panic("services: MediaWorkSlot.Release 未配对 Acquire")
	}
}
