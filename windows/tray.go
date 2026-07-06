package main

import (
	_ "embed"
	"encoding/json"
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

	trayStatusItem = systray.AddMenuItem("Отключено", "")
	trayStatusItem.Disable()

	systray.AddSeparator()

	trayToggleItem = systray.AddMenuItem("Подключить", "Подключить/отключить последний активный профиль")
	trayToggleItem.Click(func() {
		go handleTrayToggle(app)
	})

	openItem := systray.AddMenuItem("Открыть", "Открыть окно Phantom")
	openItem.Click(func() {
		if app.ctx != nil {
			runtime.WindowShow(app.ctx)
		}
	})

	systray.AddSeparator()

	quitItem := systray.AddMenuItem("Выход", "Полностью закрыть Phantom")
	quitItem.Click(func() {
		app.Disconnect()
		systray.Quit()
		os.Exit(0)
	})

	go pollTrayStatus(app)
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
		if status.Connected {
			trayStatusItem.SetTitle("Подключено")
			trayToggleItem.SetTitle("Отключить")
		} else {
			trayStatusItem.SetTitle("Отключено")
			trayToggleItem.SetTitle("Подключить")
		}
	}
}
