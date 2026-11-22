//go:build !bindings

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"
	"video-master/database"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

func main() {
	log.SetOutput(io.Discard)
	started := time.Now()
	// Create an instance of the app structure
	app := NewApp()
	app.startupPerf.started = started
	app.startupPerf.newApp = time.Since(started)

	// 初始化数据库
	dbAt := time.Now()
	dbErr := database.Init()
	app.startupPerf.database = time.Since(dbAt)
	recoveryAt := time.Now()
	if err := dbErr; err != nil {
		fmt.Fprintln(os.Stderr, "数据库初始化失败:", err)
		app.setStartupError(err)
	} else {
		defer database.Close()
		if settings, err := app.settingsService.GetSettings(); err == nil {
			app.setLogEnabled(settings.LogEnabled)
			app.setPerformanceLogEnabled(settings.PerformanceLogsEnabled())
		}
		// 在 wails.Run 之前：darwin 上 OnStartup 与页面加载并发，前端一加载就会查询中断的字幕任务。
		app.recoverVideoEditBeforeBindings(context.Background())
		app.markInterruptedSubtitleJobs()
		if err := app.initializeBTLocal(); err != nil {
			log.Printf("下载管理恢复核实未完成；其他功能不受影响")
		}
		if err := app.initializeFunctionalPolish(); err != nil {
			log.Printf("失败诊断恢复失败，批量重试不可用: %v", err)
		}
	}

	app.startupPerf.recovery = time.Since(recoveryAt)
	app.startupPerf.toWindow = time.Since(started)
	app.startupPerf.flush()

	// Create application with options
	err := wails.Run(&options.App{
		Title:         "析微影策",
		Width:         1440,
		Height:        900,
		MinWidth:      1024,
		MinHeight:     768,
		DisableResize: false,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: newAssetHandler(app),
			// 送出首页时在 <html> 上写明启动状态，第一帧就定好外壳表面（G2，D-UD-02）。
			Middleware: startupStateMiddleware(app),
		},
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0}, // 设为透明
		OnStartup:        app.startup,
		OnDomReady:       app.nativePlaybackDOMReady,
		// 有字幕、超分、代理或下载任务在跑时先请前端确认再退出（D-PC21）；内部发起的退出直接放行。
		OnBeforeClose: app.beforeClose,
		OnShutdown:    app.shutdown,
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
			Preferences: &mac.Preferences{
				FullscreenEnabled: mac.Enabled,
			},
			About: &mac.AboutInfo{
				Title:   "析微影策",
				Message: "智能视频管理系统",
			},
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
