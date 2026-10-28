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
	if platformCommand(os.Args) {
		return
	}

	// Skipped entirely during binding generation - see isBindingsBuild.
	if !isBindingsBuild && !platformPreflight() {
		return
	}

	// Create an instance of the app structure
	app := NewApp()

	// The tray icon runs its own native message loop on a locked OS thread,
	// independent of Wails' own window loop below - see tray.go.
	if !isBindingsBuild && trayAvailable() {
		trayShown = true
		go runTray(app)
	}

	// Create application with options
	opts := &options.App{
		Title:     "Phantom",
		Width:     1280,
		Height:    720,
		MinWidth:  480,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// Matches the midnight palette's --bg (style.css) - what briefly
		// shows through before the first paint, so that flash is the right
		// colour instead of the old fixed near-black.
		BackgroundColour: &options.RGBA{R: 14, G: 11, B: 24, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		Bind: []interface{}{
			app,
		},
	}
	platformOptions(opts, app)
	err := wails.Run(opts)

	if err != nil {
		println("Error:", err.Error())
	}
}
