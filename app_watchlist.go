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

// ListWatchlistLibraryState 返回这些想看条目的片库关联状态（UI 二期 D-P2-03），最多 200 条。
func (a *App) ListWatchlistLibraryState(entryIDs []uint) (map[uint]services.WatchlistLibraryState, error) {
	return a.watchlistService.ListLibraryState(entryIDs)
}

// LinkWatchlistEntryVideo 手动确认想看条目与片库视频的关联，并清掉这一对的「解除过」记录。
func (a *App) LinkWatchlistEntryVideo(entryID, videoID uint) error {
	return a.watchlistService.LinkVideo(entryID, videoID)
}

// UnlinkWatchlistEntryVideo 解除关联并记下「解除过」，之后不再自动关联这一对。
func (a *App) UnlinkWatchlistEntryVideo(entryID, videoID uint) error {
	return a.watchlistService.UnlinkVideo(entryID, videoID)
}

// ListWatchlistInLibrary 返回首页「想看的片片库里已有」：已关联且还没看过，按关联时间倒序。
func (a *App) ListWatchlistInLibrary(limit int) ([]services.WatchlistInLibraryItem, error) {
	return a.watchlistService.ListInLibrary(limit)
}

// triggerWatchlistLibraryMatch 在扫描入库了新视频之后，后台给全部未关联的想看条目跑一轮片库自动关联
// （D-P2-03）。不过空闲门：开销与想看条数成正比，且只读片库、只写关联表。
func (a *App) triggerWatchlistLibraryMatch(reason string, added int) {
	if added <= 0 || a.watchlistService == nil {
		return
	}
	a.watchlistService.TriggerLibraryMatch(reason)
}

// importDownloadAndMatchWatchlist 包一层浏览器下载入库：下载的片进了库，同样触发一轮想看自动关联。
func (a *App) importDownloadAndMatchWatchlist(importer func(string, string) (uint, error)) func(string, string) (uint, error) {
	return func(directory, outputPath string) (uint, error) {
		videoID, err := importer(directory, outputPath)
		if err == nil && videoID > 0 {
			a.triggerWatchlistLibraryMatch("download", 1)
		}
		return videoID, err
	}
}
