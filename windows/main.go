package main

import (
	"embed"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	winoptions "github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if len(os.Args) > 2 && os.Args[1] == "selftest" {
		runSelfTest(os.Args[2])
		return
	}

	// Refuse to start a second GUI instance - bring the existing one to the
	// front (it may be hidden in the tray) and exit instead.
	if !acquireSingleInstanceLock() {
		return
	}

	if err := ensureWintunDLL(); err != nil {
		println("Failed to prepare wintun.dll:", err.Error())
		return
	}

	// Create an instance of the app structure
	app := NewApp()

	// Without this, WebView2 defaults its user-data folder (cache, cookies,
	// history, etc. - none of which this app's own frontend ever uses, since it
	// has no cookies/history features) to %APPDATA%\phantom.exe - a second,
	// separate folder from the app's own %APPDATA%\Phantom (configstore.go's
	// configDir) that has nothing to do with Phantom's actual data. Nesting it
	// under the same Phantom directory keeps everything the app touches on disk
	// in one place.
	var webviewUserDataPath string
	if dir, err := configDir(); err == nil {
		webviewUserDataPath = filepath.Join(dir, "WebView2")
	}

	// The tray icon runs its own native message loop on a locked OS thread,
	// independent of Wails' own window loop below - see tray.go.
	go runTray(app)

	// Create application with options
	err := wails.Run(&options.App{
		Title: "Phantom",
		// Wide enough for the two-column main screen (saved configs on the
		// left, resource-reachability tiles on the right - see
		// frontend/src/style.css's .main-body).
		Width:     760,
		Height:    680,
		MinWidth:  640,
		MinHeight: 500,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 7, G: 7, B: 12, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		Bind: []interface{}{
			app,
		},
		Windows: &winoptions.Options{
			WebviewUserDataPath: webviewUserDataPath,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
