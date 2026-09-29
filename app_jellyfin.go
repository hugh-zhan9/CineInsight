package main

import (
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

// ConfigureJellyfin owns the explicit desktop-only credential/settings mutation. Every successful
// save deletes all persisted Jellyfin sessions (disabling, account or configuration change).
func (a *App) ConfigureJellyfin(input services.JellyfinConfigInput) (services.JellyfinStatus, error) {
	a.restoreMu.Lock()
	defer a.restoreMu.Unlock()
	if a.restoreTerminal {
		return services.JellyfinStatus{}, fmt.Errorf("数据库恢复后请先重启应用")
	}
	if a.jellyfinServer == nil {
		return services.JellyfinStatus{}, fmt.Errorf("Jellyfin 服务尚未初始化")
	}
	return a.jellyfinServer.Configure(input)
}
