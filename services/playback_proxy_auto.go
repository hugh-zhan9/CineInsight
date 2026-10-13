package services

import (
	"context"
	"errors"
)

// 自动代理（2026-10-13「直接播 + 自动代理」，评审修订）：预览宿主打开一部不能直放的视频时自动排一项代理。
//
// 每个宿主实例（内嵌播放器、字幕工作台、版本对比的一侧）带一个 owner。服务按视频记「还在等它的 owner」：
// 请求加入 owner（没在排队或处理就入队），取消移除 owner，最后一个 owner 走了才取消这一项。
// 同时在等的自动代理最多 autoPlaybackProxyLimit 部，第三部直接拒绝（ErrPlaybackProxyAutoLimit），
// 不去取消别人还在等的那一项。手动入口（按钮、批量、按筛选、扫描后自动）从不被预览取消。

// autoPlaybackProxyLimit 是同时在等的自动代理视频数上限。
const autoPlaybackProxyLimit = 2

// ErrPlaybackProxyAutoLimit：已有 autoPlaybackProxyLimit 部视频在等自动代理。前端按 auto_proxy_limit 识别。
var ErrPlaybackProxyAutoLimit = errors.New("auto_proxy_limit: 已有 2 部视频在等自动生成的播放代理")

// ErrPlaybackProxyOwnerMissing：自动代理请求没带 owner（无法按宿主记账）。
var ErrPlaybackProxyOwnerMissing = errors.New("自动播放代理请求缺少 owner")

// EnqueueAuto 为 owner 正在预览的视频请求自动代理。用户打开预览即算用户发起，不过空闲门。
//   - 已是自动项（排队、处理中，或处理中被取消、结果还没记下）：只加入 owner；被取消的那一项记下结果后重排。
//   - 已被手动入口排进去：跟着它走，不记 owner（手动项不被预览取消）。
//   - 否则入队；同时在等的自动代理已满时拒绝。
func (s *PlaybackProxyService) EnqueueAuto(parent context.Context, videoID uint, owner string) (PlaybackProxyStatus, error) {
	if s == nil {
		return PlaybackProxyStatus{}, errPlaybackProxyServiceMissing
	}
	if owner == "" {
		return s.Status(), ErrPlaybackProxyOwnerMissing
	}
	if videoID == 0 {
		return s.Status(), nil
	}
	return s.enqueueItems(parent, []uint{videoID}, owner)
}

// autoPrecheckLocked 处理自动请求里不需要新入队的情况。handled 为真时调用方直接返回当前快照（与 err）。
func (s *PlaybackProxyService) autoPrecheckLocked(videoID uint, owner string) (handled bool, err error) {
	if owners, ok := s.autoOwners[videoID]; ok {
		owners[owner] = struct{}{}
		return true, nil
	}
	if _, running := s.inFlight[videoID]; running {
		if _, cancelling := s.autoCancelling[videoID]; cancelling {
			// 处理中的自动项刚被取消、结果还没记下：先记下 owner，结果落下时重排（settleAutoLocked）。
			s.autoOwners[videoID] = map[string]struct{}{owner: {}}
		}
		return true, nil
	}
	if len(s.autoOwners) >= autoPlaybackProxyLimit {
		return true, ErrPlaybackProxyAutoLimit
	}
	return false, nil
}

// promoteAutoLocked：手动入口接管了这一项，之后预览关闭不再取消它；处理中被取消的，结果落下时按手动重排。
func (s *PlaybackProxyService) promoteAutoLocked(videoID uint) {
	delete(s.autoOwners, videoID)
	if _, cancelling := s.autoCancelling[videoID]; cancelling {
		s.autoCancelling[videoID] = true
	}
}

// CancelAuto 移除 owner；这一项没有别的 owner 时才取消它（排队中当场摘掉，处理中只停这一项）。
// 返回是否真的取消了。owner 不在（已完成、已转手动、从未请求）时什么都不做。
func (s *PlaybackProxyService) CancelAuto(videoID uint, owner string) bool {
	if s == nil || videoID == 0 {
		return false
	}
	s.mu.Lock()
	owners, ok := s.autoOwners[videoID]
	if !ok {
		s.mu.Unlock()
		return false
	}
	// 没请求过的 owner 删了也是空操作：集合非空就不取消。
	delete(owners, owner)
	if len(owners) > 0 {
		s.mu.Unlock()
		return false
	}
	delete(s.autoOwners, videoID)
	var cancelItem context.CancelFunc
	if !s.removeQueuedLocked(videoID) {
		if _, already := s.autoCancelling[videoID]; !already && s.status.CurrentVideoID == videoID {
			s.autoCancelling[videoID] = false
			cancelItem = s.itemCancel
		}
	}
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	if cancelItem != nil {
		cancelItem()
	}
	if emitter != nil {
		emitter(status)
	}
	return true
}

// removeQueuedLocked 把还在排队的一项摘掉并记 cancelled。不在队列里返回 false。
func (s *PlaybackProxyService) removeQueuedLocked(videoID uint) bool {
	for index, queued := range s.queue {
		if queued != videoID {
			continue
		}
		s.queue = append(s.queue[:index], s.queue[index+1:]...)
		delete(s.inFlight, videoID)
		s.appendResultLocked(PlaybackProxyItemResult{VideoID: videoID, Code: PlaybackProxyCodeCancelled, Message: "已取消"})
		return true
	}
	return false
}

// settleAutoLocked 在一项有了结果时收账，返回是否要把它重新排进队列：处理中被取消、而取消之后又有人
// 请求（自动 owner 或手动入口）时重排；其余情况这一项不再是自动项。
func (s *PlaybackProxyService) settleAutoLocked(result PlaybackProxyItemResult) bool {
	videoID := result.VideoID
	manualWanted, cancelling := s.autoCancelling[videoID]
	delete(s.autoCancelling, videoID)
	if cancelling && result.Code == PlaybackProxyCodeCancelled && !s.stopping &&
		(manualWanted || len(s.autoOwners[videoID]) > 0) {
		return true
	}
	delete(s.autoOwners, videoID)
	return false
}
