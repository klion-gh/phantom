package main

import (
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if err := ensureWintunDLL(); err != nil {
		println("Failed to prepare wintun.dll:", err.Error())
		return
	}

	if len(os.Args) > 2 && os.Args[1] == "selftest" {
		runSelfTest(os.Args[2])
		return
	}

	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:     "Phantom",
		Width:     420,
		Height:    680,
		MinWidth:  380,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 7, G: 7, B: 12, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
