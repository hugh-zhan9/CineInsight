package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// ===== 浏览器插件桥接（D-B03、D-B06）=====
//
// 桥接是一条能让桌面端按外部请求去取任意地址并落盘的通道，因此它的开关、
// 令牌、下载目录都由用户显式决定，这里只提供入口，不替他做任何一项。

// settingsBridgeAuth 把设置表接到桥接服务的鉴权接口上。
// 每个请求现读一次：用户在设置页关掉桥接或换了令牌，下一个请求立刻按新的判。
type settingsBridgeAuth struct {
	settings *services.SettingsService
}

func (a settingsBridgeAuth) BridgeCredentials() (string, bool, error) {
	settings, err := a.settings.GetSettings()
	if err != nil {
		return "", false, err
	}
	return settings.BrowserBridgeToken, settings.BrowserBridgeEnabled, nil
}

// GetBrowserBridgeStatus 返回桥接服务当前状态，供设置页显示。
func (a *App) GetBrowserBridgeStatus() services.BrowserBridgeStatus {
	if a.browserBridge == nil {
		return services.BrowserBridgeStatus{AllowedAccess: "127.0.0.1 only, token required"}
	}
	status := a.browserBridge.Status()
	log.Printf("API GetBrowserBridgeStatus running=%v port=%d err=%q", status.Running, status.Port, status.StartupError)
	return status
}

// RegenerateBrowserBridgeToken 生成一枚新令牌并立刻生效。
//
// 令牌只从这一个入口写：通用的设置保存不碰它，免得前端某次漏带字段就把
// 已配对的插件悄悄踢下线。旧令牌在这一刻立即失效，调用方必须把这件事告诉用户。
func (a *App) RegenerateBrowserBridgeToken() (string, error) {
	token, err := generateBridgeToken()
	if err != nil {
		return "", err
	}
	result := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("browser_bridge_token", token)
	if result.Error != nil {
		log.Printf("API RegenerateBrowserBridgeToken err=%v", result.Error)
		return "", fmt.Errorf("保存令牌失败: %w", result.Error)
	}
	// 影响 0 行说明设置行不在（库没初始化好）。这时候不能把令牌交出去：
	// 用户会把它填进插件，然后对着一个永远认证不过的配对找不着北。
	if result.RowsAffected == 0 {
		log.Printf("API RegenerateBrowserBridgeToken err=no settings row")
		return "", fmt.Errorf("保存令牌失败: 设置行不存在")
	}
	a.restartBrowserBridge()
	log.Printf("API RegenerateBrowserBridgeToken ok")
	return token, nil
}

// SelectBrowserDownloadDirectory 选下载目录。为空表示用户取消了选择。
func (a *App) SelectBrowserDownloadDirectory() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择浏览器插件的下载目录",
	})
}

// ListBrowserDownloadTasks 返回桥接下载队列的任务快照。
func (a *App) ListBrowserDownloadTasks() []services.BrowserDownloadTask {
	if a.browserDownloads == nil {
		return []services.BrowserDownloadTask{}
	}
	tasks := a.browserDownloads.ListTasks()
	if tasks == nil {
		return []services.BrowserDownloadTask{}
	}
	return tasks
}

// CancelBrowserDownloadTask 取消一个下载任务。
func (a *App) CancelBrowserDownloadTask(id string) error {
	if a.browserDownloads == nil {
		return fmt.Errorf("下载服务未启用")
	}
	err := a.browserDownloads.CancelTask(id)
	log.Printf("API CancelBrowserDownloadTask id=%s err=%v", id, err)
	return err
}

// startBrowserBridge 在应用启动时按设置决定要不要开桥接。
func (a *App) startBrowserBridge(ctx context.Context) {
	if a.browserDownloads != nil {
		a.browserDownloads.Start(ctx)
	}
	if a.browserBridge == nil {
		return
	}
	a.browserBridge.Start(ctx)
	status := a.browserBridge.Status()
	if status.StartupError != "" {
		log.Printf("Browser bridge not started: %s", status.StartupError)
		return
	}
	if status.Running {
		log.Printf("Browser bridge listening on %s", status.URL)
	}
}

// restartBrowserBridge 在开关或令牌变化后重开服务。
func (a *App) restartBrowserBridge() {
	if a.browserBridge == nil {
		return
	}
	a.browserBridge.Restart(a.backgroundContext())
}

func generateBridgeToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成令牌失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
