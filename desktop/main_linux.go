package main

import (
	_ "embed"
	"os"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed build/linux/phantom.png
var appIconPNG []byte

func platformCommand(args []string) bool { return false }

func platformPreflight() bool {
	// WebKitGTK's DMA-BUF renderer leaves the window blank on a good share of
	// GPUs and virtual machines (a widely reported WebKitGTK 2.42+ issue);
	// the fallback costs nothing this UI would notice. An explicit setting in
	// the environment still wins.
	if os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER") == "" {
		os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
	}
	return true
}

func platformOptions(opts *options.App, app *App) {
	// A second launch (the menu entry clicked again while the window sits in
	// the tray) brings the running window back instead of starting another.
	opts.SingleInstanceLock = &options.SingleInstanceLock{
		UniqueId: "com.phantom.vpn",
		OnSecondInstanceLaunch: func(options.SecondInstanceData) {
			if app.ctx != nil {
				runtime.WindowUnminimise(app.ctx)
				runtime.WindowShow(app.ctx)
			}
		},
	}
	gpu := linux.WebviewGpuPolicyOnDemand
	if os.Getenv("PHANTOM_NO_GPU") != "" {
		gpu = linux.WebviewGpuPolicyNever
	}
	opts.Linux = &linux.Options{
		Icon:             appIconPNG,
		ProgramName:      "phantom",
		WebviewGpuPolicy: gpu,
	}
}
