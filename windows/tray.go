package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/energye/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed build/windows/icon.ico
var trayIconBytes []byte

var (
	trayStatusItem *systray.MenuItem
	trayToggleItem *systray.MenuItem
	trayOpenItem   *systray.MenuItem
	trayQuitItem   *systray.MenuItem
)

// runTray starts the system tray icon and blocks until systray.Quit() is
// called (from the "Выход" item) - meant to run in its own goroutine
// alongside wails.Run(), which owns the main window's event loop. Windows'
// tray icon defaults to showing the context menu on right-click without any
// extra wiring; SetOnClick below just makes left-click restore the window
// too, matching how most tray-resident apps behave.
func runTray(app *App) {
	systray.Run(func() { onTrayReady(app) }, func() {})
}

func onTrayReady(app *App) {
	systray.SetIcon(trayIconBytes)
	systray.SetTooltip("Phantom VPN")
	systray.SetOnClick(func(menu systray.IMenu) {
		if app.ctx != nil {
			runtime.WindowShow(app.ctx)
		}
	})

	setTrayLang(loadLanguage())

	// Labels are set by refreshTrayLanguage below (and re-set live by
	// pollTrayStatus for the state-dependent ones); created empty here.
	trayStatusItem = systray.AddMenuItem("", "")
	trayStatusItem.Disable()

	systray.AddSeparator()

	trayToggleItem = systray.AddMenuItem("", "")
	trayToggleItem.Click(func() {
		go handleTrayToggle(app)
	})

	trayOpenItem = systray.AddMenuItem("", "")
	trayOpenItem.Click(func() {
		if app.ctx != nil {
			runtime.WindowShow(app.ctx)
		}
	})

	systray.AddSeparator()

	trayQuitItem = systray.AddMenuItem("", "")
	trayQuitItem.Click(func() {
		app.Disconnect()
		systray.Quit()
		os.Exit(0)
	})

	refreshTrayLanguage()
	go pollTrayStatus(app)
}

// refreshTrayLanguage re-labels the tray items that don't depend on connection
// state (open/quit and all tooltips) for the current trayLang. Called at
// startup and whenever the user switches language in the WebView
// (App.SetLanguage). The status label and connect/disconnect toggle depend on
// live state, so pollTrayStatus owns those and picks up the new language on its
// next tick (~2s). Safe to call before the menu exists (no-op).
func refreshTrayLanguage() {
	if trayToggleItem == nil {
		return
	}
	lang := getTrayLang()
	trayToggleItem.SetTooltip(trayT(lang, "toggle_tip"))
	trayOpenItem.SetTitle(trayT(lang, "open"))
	trayOpenItem.SetTooltip(trayT(lang, "open_tip"))
	trayQuitItem.SetTitle(trayT(lang, "quit"))
	trayQuitItem.SetTooltip(trayT(lang, "quit_tip"))
}

// handleTrayToggle mirrors the notification-driven reconnect logic the
// Android app uses: disconnect if something's already up, otherwise resume
// the last-active saved config (falling back to the first one).
func handleTrayToggle(app *App) {
	var status statusResponse
	if err := json.Unmarshal([]byte(app.Status()), &status); err == nil && status.Connected {
		app.Disconnect()
		return
	}

	configs, err := loadConfigs()
	if err != nil || len(configs) == 0 {
		log.Println("tray connect: no saved configs")
		return
	}
	lastID := loadLastActiveID()
	target := configs[0]
	for _, c := range configs {
		if c.ID == lastID {
			target = c
			break
		}
	}
	app.Connect(target.ID, target.Yaml)
}

func pollTrayStatus(app *App) {
	for {
		time.Sleep(2 * time.Second)
		var status statusResponse
		if err := json.Unmarshal([]byte(app.Status()), &status); err != nil {
			continue
		}

		// Two independent facts on one line, mirroring the Android notification's
		// "VPN: … | Proxy: …": the full-tunnel VPN and the standalone per-config
		// proxies are unrelated features that can each be on or off separately.
		lang := getTrayLang()
		vpnState := trayT(lang, "vpn_off")
		if status.Connected {
			vpnState = trayT(lang, "vpn_on")
			trayToggleItem.SetTitle(trayT(lang, "disconnect"))
		} else {
			trayToggleItem.SetTitle(trayT(lang, "connect"))
		}
		proxyState := trayT(lang, "proxy_off")
		if anyConfigProxyRunning() {
			proxyState = trayT(lang, "proxy_on")
		}
		trayStatusItem.SetTitle(fmt.Sprintf("VPN: %s · Proxy: %s", vpnState, proxyState))
	}
}
