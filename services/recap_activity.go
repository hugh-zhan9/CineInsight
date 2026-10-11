package services

import (
	"sync"
	"time"
)

// RecapActivity 是 recap-activity 事件的载荷：一条播放或图片查看事实刚提交。
// 只带种类、来源与时间，回顾页据此重新读取，不靠载荷拼数据。
type RecapActivity struct {
	Kind   string    `json:"kind"` // video / image
	Source string    `json:"source"`
	At     time.Time `json:"at" ts_type:"string"`
}

const (
	RecapActivityVideo = "video"
	RecapActivityImage = "image"
)

var recapActivityListener struct {
	mu sync.RWMutex
	fn func(RecapActivity)
}

// SetRecapActivityListener 由 App 在启动时注册，把事实写入通知转成前端事件。传 nil 取消。
// 写入方分散在播放、内嵌观看、手机端与 Jellyfin 多条路径上，用包级钩子而不是逐个服务注入，
// 免得每条路径都要多带一个回调参数。
func SetRecapActivityListener(fn func(RecapActivity)) {
	recapActivityListener.mu.Lock()
	recapActivityListener.fn = fn
	recapActivityListener.mu.Unlock()
}

// notifyRecapActivity 只在事务提交成功之后调用；没有监听者时什么也不做。
func notifyRecapActivity(kind, source string, at time.Time) {
	recapActivityListener.mu.RLock()
	fn := recapActivityListener.fn
	recapActivityListener.mu.RUnlock()
	if fn != nil {
		fn(RecapActivity{Kind: kind, Source: source, At: at})
	}
}
