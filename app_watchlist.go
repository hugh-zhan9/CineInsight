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
func (a *App) CreateWatchlistEntry(title string) (*models.WatchlistEntry, error) {
	return a.watchlistService.Create(title)
}

// UpdateWatchlistEntry 修改指定记录的片名。
func (a *App) UpdateWatchlistEntry(id uint, title string) error {
	return a.watchlistService.Update(id, title)
}

// DeleteWatchlistEntry 手动移除指定片名备忘。
func (a *App) DeleteWatchlistEntry(id uint) error {
	return a.watchlistService.Delete(id)
}
