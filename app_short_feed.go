package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"video-master/services"
)

// 手机端访问控制的 App 层入口（D-PC45 / D-PC47）。PIN、会话与开关的规则都在
// services.ShortFeedService 里，这里只做「持久化之后启停服务」这一步——它需要 App 持有的
// shortFeedServer 与 ctx，所以只能放在 App 层。日志不记录 PIN。

// shortFeedLifecycleMu 串行化开关的启停：同一时刻只有一次「持久化 + 启停」在进行。
// 没有给 App 结构体加字段，所以用包级变量。
var shortFeedLifecycleMu sync.Mutex

// SetShortFeedEnabled 开启或关闭手机端访问。开启时启动监听，启动失败则回写 false 并返回错误；
// 关闭时立刻停止监听。
func (a *App) SetShortFeedEnabled(enabled bool) error {
	shortFeedLifecycleMu.Lock()
	defer shortFeedLifecycleMu.Unlock()

	if err := a.shortFeedService.SetShortFeedEnabled(enabled); err != nil {
		return err
	}
	if !enabled {
		return a.stopShortFeedForSetting()
	}
	if a.shortFeedServer != nil && a.shortFeedServer.Status().Running {
		return nil
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	a.startShortFeedServer(ctx)
	status := services.ShortFeedServerStatus{}
	if a.shortFeedServer != nil {
		status = a.shortFeedServer.Status()
	}
	if status.Running {
		return nil
	}
	reason := status.StartupError
	if reason == "" {
		reason = a.shortFeedStartupError
	}
	if reason == "" {
		reason = "服务未能启动"
	}
	// 启动失败：回写关闭，避免设置页显示「已开启」而端口其实没在监听。
	if rollbackErr := a.shortFeedService.SetShortFeedEnabled(false); rollbackErr != nil {
		log.Printf("API SetShortFeedEnabled rollback failed err=%v", rollbackErr)
	}
	return fmt.Errorf("手机端访问启动失败：%s", reason)
}

func (a *App) stopShortFeedForSetting() error {
	server := a.shortFeedServer
	if server == nil {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Stop(stopCtx); err != nil {
		return fmt.Errorf("停止手机端访问失败: %w", err)
	}
	a.shortFeedServer = nil
	a.shortFeedStartupError = ""
	return nil
}

// SetShortFeedPIN 设置手机端访问 PIN（4–32 个字符，不含控制字符）。设置后所有已登录的手机会话失效。
func (a *App) SetShortFeedPIN(pin string) error {
	err := a.shortFeedService.SetShortFeedPIN(pin)
	log.Printf("API SetShortFeedPIN err=%v", err)
	return err
}

// ClearShortFeedPIN 清除 PIN，同时使全部会话失效。
func (a *App) ClearShortFeedPIN() error {
	err := a.shortFeedService.ClearShortFeedPIN()
	log.Printf("API ClearShortFeedPIN err=%v", err)
	return err
}

// GetShortFeedAccessStatus 返回开关、是否已设 PIN、是否在监听与首选访问地址。
// PIN 哈希不在其中：前端只靠 pin_set 决定是否显示「建议设置 PIN」。
func (a *App) GetShortFeedAccessStatus() (services.ShortFeedAccessStatus, error) {
	status, err := a.shortFeedService.AccessStatus()
	if err != nil {
		return services.ShortFeedAccessStatus{}, err
	}
	if a.shortFeedServer != nil {
		server := a.shortFeedServer.Status()
		status.Listening = server.Running
		if server.Running {
			status.URL = server.URL
			if len(server.LANURLs) > 0 {
				status.URL = server.LANURLs[0]
			}
		}
	}
	return status, nil
}

// GetShortFeedQRCode 把访问地址渲染成二维码（PNG 的 data URL）。不支持二维码的平台返回空串。
func (a *App) GetShortFeedQRCode(url string) (string, error) {
	dataURL, err := services.ShortFeedQRCodeDataURL(url)
	if err != nil {
		log.Printf("API GetShortFeedQRCode err=%v", err)
	}
	return dataURL, err
}
