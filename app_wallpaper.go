package main

import "video-master/services"

// GetWallpaperPreflight 检查素材是否满足壁纸最低分辨率和文件可用性。
func (a *App) GetWallpaperPreflight(kind string, mediaID uint) services.WallpaperPreflight {
	return a.wallpaperService.Preflight(kind, mediaID)
}

// SetWallpaper 设置图片或动态视频壁纸，不记播放统计。
func (a *App) SetWallpaper(kind string, mediaID uint) (services.WallpaperStatus, error) {
	return a.wallpaperService.Set(kind, mediaID)
}

// GetWallpaperStatus 返回动态壁纸实际播放状态。
func (a *App) GetWallpaperStatus() services.WallpaperStatus {
	return a.wallpaperService.Status()
}

// StopVideoWallpaper 移除动态桌面层，露出系统原有壁纸。
func (a *App) StopVideoWallpaper() (services.WallpaperStatus, error) {
	return a.wallpaperService.Stop()
}
