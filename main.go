package main

import (
	"context"
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/yazzyk/stock-agent/internal/bootstrap"
	"github.com/yazzyk/stock-agent/internal/headless"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	cfg := bootstrap.LoadConfig()
	logger := bootstrap.NewLogger(cfg)

	k, err := bootstrap.New(cfg, logger)
	if err != nil {
		logger.Error("kernel 初始化失败", "error", err)
		os.Exit(1)
	}
	if err := k.Start(context.Background()); err != nil {
		logger.Error("kernel 启动失败", "error", err)
		os.Exit(1)
	}

	if cfg.Headless {
		code := headless.Run(context.Background(), k, cfg.Task, cfg.Args, os.Stdout)
		_ = k.Stop(context.Background())
		os.Exit(code)
	}

	app := NewApp(k)
	err = wails.Run(&options.App{
		Title:     "观澜 · Stock Agent",
		Width:     1440,
		Height:    900,
		MinWidth:  1180,
		MinHeight: 720,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 16, G: 18, B: 23, A: 1},
		OnStartup:        app.Startup,
		OnShutdown:       app.Shutdown,
		Bind:             []interface{}{app},
	})
	if err != nil {
		logger.Error("wails 运行失败", "error", err)
		os.Exit(1)
	}
}
