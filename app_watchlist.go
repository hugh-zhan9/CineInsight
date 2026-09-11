package main

import (
	"video-master/models"
	"video-master/services"
)

// ListWatchlist 返回匹配片名的下一页想看记录。
func (a *App) ListWatchlist(keyword string, cursorID uint, limit int) (*services.WatchlistPage, error) {
	return a.watchlistService.List(keyword, cursorID, limit)
}

// CreateWatchlistEntry 添加一条片名备忘。
//
// kind 为空按 movie，非法值由服务层拒绝，**不静默回退**（D-WM13）。界面上的番号
// 识别只改下拉框的值，提交上来的永远是用户最后选定的那个类型——这里不做任何
// 「看起来像番号就按 AV 存」的二次判定。
func (a *App) CreateWatchlistEntry(title string, kind string) (*models.WatchlistEntry, error) {
	return a.watchlistService.Create(title, kind)
}

// UpdateWatchlistEntry 修改指定记录的片名。
func (a *App) UpdateWatchlistEntry(id uint, title string) error {
	return a.watchlistService.Update(id, title)
}

// DeleteWatchlistEntry 手动移除指定片名备忘。
func (a *App) DeleteWatchlistEntry(id uint) error {
	return a.watchlistService.Delete(id)
}

// RetryWatchlistEnrichment 手动重试在线补全：状态转 pending 并清空失败分类码。
func (a *App) RetryWatchlistEnrichment(id uint) error {
	return a.watchlistService.RetryEnrichment(id)
}

// ListWatchlistCandidates 列出该条目在资料源上的候选，供用户重选匹配结果。
func (a *App) ListWatchlistCandidates(id uint) ([]services.WatchlistCandidateView, error) {
	return a.watchlistService.ListCandidates(id)
}

// ApplyWatchlistCandidate 把用户选中的候选写成该条目的补全结果。
func (a *App) ApplyWatchlistCandidate(id uint, sourceItemID string) error {
	return a.watchlistService.ApplyCandidate(id, sourceItemID)
}
