package main

import (
	"context"
	"embed"
	"os"
	goruntime "runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/yazzyk/stock-agent/internal/bootstrap"
	"github.com/yazzyk/stock-agent/internal/headless"
)

// markVibrancy 在 macOS（窗口材质真正可用）时给 <html> 打上 .vibrancy 类，
// 让前端把基础底色从「不透明实底」切成「低 alpha 遮罩」，从而透出窗口的
// NSVisualEffectView 模糊。其他平台不打类，前端保持不透明降级底色，
// 避免半透明玻璃铺在白底/桌面上糊成一片灰。
func markVibrancy(ctx context.Context) {
	if goruntime.GOOS != "darwin" {
		return
	}
	wailsruntime.WindowExecJS(ctx, "document.documentElement.classList.add('vibrancy')")
}

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
		// 窗口自身底色必须近乎全透明，NSVisualEffectView 的模糊才能露出来：
		// 真正的深色由前端 body 的低 alpha 遮罩 + 各层玻璃负责（见 style.css）。
		// wails 对该字段没有 alpha 校验（options.go 只在为 nil 时补默认值），A:0 可用。
		BackgroundColour: &options.RGBA{R: 10, G: 14, B: 21, A: 0},
		// macOS：隐藏原生标题栏，内容延伸到窗口顶部，由前端自绘标题栏（含拖拽区）；
		// 红绿灯保留在左上角（TitleBarHidden 版本：红绿灯右缘约 x=66，
		// 因此折叠态导航宽度 --w-nav-min 取 80px，竖线不会压到红绿灯）。
		Mac: &mac.Options{
			TitleBar:   mac.TitleBarHidden(),
			Appearance: mac.NSAppearanceNameDarkAqua,
			// 网页背景透明（WKWebView drawsBackground=false），窗口材质才有机会透上来。
			WebviewIsTransparent: true,
			// 真·毛玻璃：在 contentView 底部插入 NSVisualEffectView（BehindWindow 模糊）。
			// 该字段只在 darwin 生效，windows/linux 有各自的同名选项，跨平台构建安全。
			WindowIsTranslucent: true,
		},
		OnStartup:  app.Startup,
		OnDomReady: markVibrancy,
		OnShutdown: app.Shutdown,
		Bind:       []interface{}{app},
	})
	if err != nil {
		logger.Error("wails 运行失败", "error", err)
		os.Exit(1)
	}
}
