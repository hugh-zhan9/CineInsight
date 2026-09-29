package main

import (
	"errors"
	"fmt"
	"video-master/services"
)

// GetJellyfinStatus returns connection details without any login secrets.
func (a *App) GetJellyfinStatus() services.JellyfinStatus {
	if a.jellyfinServer == nil {
		return services.JellyfinStatus{Port: 8096}
	}
	return a.jellyfinServer.Status()
}

// GetJellyfinDiagnostics returns the switch and listener state plus the last request, client and
// failure seen by the Jellyfin server (D-PC47). It never carries tokens, passwords or media paths.
func (a *App) GetJellyfinDiagnostics() services.JellyfinDiagnostics {
	if a.jellyfinServer == nil {
		return services.JellyfinDiagnostics{}
	}
	return a.jellyfinServer.Diagnostics()
}

// errJellyfinRelaunchPending：后端切换或切回之前的后端已经成功，维护围栏保持到重启（与 relaunch_pending
// 同口径）。Jellyfin 设置要写库，这时写不进去，也不该写进下次启动就不用的那个库。
var errJellyfinRelaunchPending = errors.New("后端已切换，请先重启应用")

// ConfigureJellyfin owns the explicit desktop-only credential/settings mutation. Every successful
// save deletes all persisted Jellyfin sessions (disabling, account or configuration change).
//
// 与恢复备份、切换后端共用 restoreMu，但用 TryLock（m7）：那两件事可能要跑很久（迁移按表复制、
// pg_restore），设置页的保存按钮不能一直挂着等它们结束，拿不到锁就立即拒绝。
func (a *App) ConfigureJellyfin(input services.JellyfinConfigInput) (services.JellyfinStatus, error) {
	if a.databaseSwitchService != nil && a.databaseSwitchService.RelaunchPending() {
		return services.JellyfinStatus{}, errJellyfinRelaunchPending
	}
	if !a.restoreMu.TryLock() {
		return services.JellyfinStatus{}, errDatabaseMaintenanceBusy
	}
	defer a.restoreMu.Unlock()
	if a.restoreTerminal {
		if a.databaseSwitchService != nil && a.databaseSwitchService.RelaunchPending() {
			return services.JellyfinStatus{}, errJellyfinRelaunchPending
		}
		return services.JellyfinStatus{}, fmt.Errorf("数据库恢复后请先重启应用")
	}
	if a.jellyfinServer == nil {
		return services.JellyfinStatus{}, fmt.Errorf("Jellyfin 服务尚未初始化")
	}
	return a.jellyfinServer.Configure(input)
}
