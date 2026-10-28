package main

import (
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/options"
	winoptions "github.com/wailsapp/wails/v2/pkg/options/windows"
)

// platformCommand runs a non-GUI subcommand, reporting whether it did.
func platformCommand(args []string) bool {
	if len(args) > 2 && args[1] == "selftest" {
		runSelfTest(args[2])
		return true
	}
	return false
}

// platformPreflight runs before the window exists; false means exit now.
func platformPreflight() bool {
	// Refuse to start a second GUI instance - bring the existing one to the
	// front (it may be hidden in the tray) and exit instead.
	if !acquireSingleInstanceLock() {
		return false
	}
	if err := ensureWintunDLL(); err != nil {
		println("Failed to prepare wintun.dll:", err.Error())
		return false
	}
	return true
}

func platformOptions(opts *options.App, app *App) {
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
	opts.Windows = &winoptions.Options{
		WebviewUserDataPath: webviewUserDataPath,
	}
}
