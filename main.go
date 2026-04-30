//go:build !bindings

package main

import (
	"io"
	"log"
	"video-master/database"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// 初始化数据库
	if err := database.Init(); err != nil {
		log.Printf("数据库初始化失败: %v", err)
		app.setStartupError(err)
	} else {
		defer database.Close()
	}

	// 默认禁用日志，设置页开启后再写入
	log.SetOutput(io.Discard)

	// Create application with options
	err := wails.Run(&options.App{
		Title:         "析微影策",
		Width:         1280,
		Height:        800,
		MinWidth:      1024,
		MinHeight:     768,
		DisableResize: false,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: newAssetHandler(app),
		},
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0}, // 设为透明
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
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
