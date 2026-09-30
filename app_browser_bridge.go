package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"gorm.io/gorm"

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

// CancelBrowserDownloadTask 取消一个下载任务。
func (a *App) CancelBrowserDownloadTask(id string) error {
	if a.browserDownloads == nil {
		return fmt.Errorf("下载服务未启用")
	}
	err := a.browserDownloads.CancelTask(id)
	log.Printf("API CancelBrowserDownloadTask id=%s err=%v", id, err)
	return err
}

// ListDownloadTasks 返回本次会话的下载任务与表里的历史（D-PC21）。历史任务不带请求头，
// retryable 为 false，只能回浏览器重新推送。
func (a *App) ListDownloadTasks() []services.BrowserDownloadTask {
	if a.browserDownloads == nil {
		return []services.BrowserDownloadTask{}
	}
	tasks := a.browserDownloads.ListDownloadTasks()
	if tasks == nil {
		return []services.BrowserDownloadTask{}
	}
	return tasks
}

// RetryDownload 在同一会话内重试失败或已取消的下载；重启后的任务返回 retry_requires_browser。
func (a *App) RetryDownload(taskUID string) (services.BrowserDownloadActionResult, error) {
	if a.browserDownloads == nil {
		return browserDownloadUnavailable(), nil
	}
	result, err := a.browserDownloads.RetryDownload(taskUID)
	log.Printf("API RetryDownload task=%s code=%s err=%v", taskUID, result.Code, err)
	return result, err
}

// AddDownloadDirectoryToScan 把下载目录加入扫描目录，再对它窄对账入库（D-PC25）。
//
// 先预检（M-5）：ValidateScanDirectory 判重复 / 嵌套 / 存在，再对照扫描黑名单；黑名单、包含已有
// 扫描根、目录不在时返回可读的结果码（BrowserDownloadScanDirectoryCheck），不加目录。目录已在
// 扫描范围内（与某个根相同或在其内）时不重复添加，直接重新入库。
//
// 真正加了目录时，与 App.AddDirectory 走同样的三步：DirectoryService.AddDirectory、重配监听、
// rescanAddedDirectory（窄对账并发 library-watcher-reconciled / library-scan-summary 事件，片库页据此
// 刷新）。这里同步调用 rescanAddedDirectory 而不是像 App.AddDirectory 那样另起 goroutine：随后的
// ReimportDownload 还要对同一目录跑一次入库并确认这一个任务的文件进没进库，两次扫描串行，
// 不会并发扫同一目录。
func (a *App) AddDownloadDirectoryToScan(taskUID string) (services.BrowserDownloadActionResult, error) {
	if a.browserDownloads == nil {
		return browserDownloadUnavailable(), nil
	}
	directory, check, err := a.browserDownloads.FinishedDownloadDirectory(taskUID)
	if err != nil || check.Code != services.BrowserDownloadCodeOK {
		log.Printf("API AddDownloadDirectoryToScan task=%s code=%s err=%v", taskUID, check.Code, err)
		return check, err
	}
	validation, err := a.directoryService.ValidateScanDirectory(directory)
	if err != nil {
		return services.BrowserDownloadActionResult{}, fmt.Errorf("检查扫描目录失败：%w", err)
	}
	settings, err := a.settingsService.GetSettings()
	if err != nil {
		return services.BrowserDownloadActionResult{}, fmt.Errorf("读取设置失败：%w", err)
	}
	covered, precheck := services.BrowserDownloadScanDirectoryCheck(validation,
		services.BrowserDownloadDirectoryExcluded(settings.ScanExcludePaths, directory))
	if precheck.Code != services.BrowserDownloadCodeOK {
		log.Printf("API AddDownloadDirectoryToScan task=%s code=%s", taskUID, precheck.Code)
		return precheck, nil
	}
	if !covered {
		added, err := a.directoryService.AddDirectory(directory, "")
		if err != nil {
			log.Printf("API AddDownloadDirectoryToScan task=%s add directory err=%v", taskUID, err)
			return services.BrowserDownloadActionResult{}, fmt.Errorf("加入扫描目录失败：%w", err)
		}
		a.reconfigureLibraryWatcher()
		if added != nil {
			a.rescanAddedDirectory(*added)
		}
	}
	result, err := a.browserDownloads.ReimportDownload(taskUID)
	log.Printf("API AddDownloadDirectoryToScan task=%s code=%s err=%v", taskUID, result.Code, err)
	return result, err
}

// ReimportDownload 对已完成下载的目录重跑一次入库（D-PC25）。
func (a *App) ReimportDownload(taskUID string) (services.BrowserDownloadActionResult, error) {
	if a.browserDownloads == nil {
		return browserDownloadUnavailable(), nil
	}
	result, err := a.browserDownloads.ReimportDownload(taskUID)
	log.Printf("API ReimportDownload task=%s code=%s err=%v", taskUID, result.Code, err)
	return result, err
}

// RevealDownload 在系统文件管理器里定位下载的文件（D-PC25）。
func (a *App) RevealDownload(taskUID string) (services.BrowserDownloadActionResult, error) {
	if a.browserDownloads == nil {
		return browserDownloadUnavailable(), nil
	}
	result, err := a.browserDownloads.RevealDownload(taskUID)
	log.Printf("API RevealDownload task=%s code=%s err=%v", taskUID, result.Code, err)
	return result, err
}

func browserDownloadUnavailable() services.BrowserDownloadActionResult {
	return services.BrowserDownloadActionResult{Code: services.BrowserDownloadCodeServiceUnavailable, Message: "下载服务未启用"}
}

// startBrowserBridge 在应用启动时按设置决定要不要开桥接。
//
// 下载任务的落库也在这里接上（D-PC21）：startup 只在数据库就绪后才走到这一步，而且此刻
// 桥接还没开始监听、不可能有新任务进来，正好先把上次遗留的非终态行置为 interrupted。
func (a *App) startBrowserBridge(ctx context.Context) {
	if a.browserDownloads != nil {
		a.browserDownloads.SetStore(func() *gorm.DB { return database.DB })
		if marked, err := a.browserDownloads.MarkInterruptedOnStartup(); err != nil {
			log.Printf("Browser download: 标记上次中断的任务失败 err=%v", err)
		} else if marked > 0 {
			log.Printf("Browser download: 上次有 %d 个下载任务没跑完，已标为已中断", marked)
		}
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
