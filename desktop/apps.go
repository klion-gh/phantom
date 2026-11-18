package main

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// "Приложения под обход": programs whose every connection goes through the
// tunnel in Умный VPN, whatever site it is to - the per-app counterpart of the
// site list. The engine (internal/routing) asks the platform per flow whether
// it came from one of them (appOwnerLookup, apps_windows.go); here is the
// list itself and the bindings the UI edits it with.

const smartAppsFileName = "smart_vpn_apps"

// SmartApp is one listed program. Matched by its exe's file name, not the
// whole path: apps such as Discord install every update into a new
// versioned folder, and a full-path match would silently stop working after
// the first one.
type SmartApp struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func loadSmartApps() []SmartApp {
	out := []SmartApp{}
	raw := readSettingFile(smartAppsFileName)
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []SmartApp{}
	}
	return out
}

func saveSmartApps(apps []SmartApp) {
	data, err := json.Marshal(apps)
	if err != nil {
		return
	}
	writeSettingFile(smartAppsFileName, string(data))
}

// exeKey is what a listed app and a running process are compared by.
func exeKey(path string) string {
	return strings.ToLower(filepath.Base(strings.ReplaceAll(path, "/", `\`)))
}

func smartAppKeys(apps []SmartApp) map[string]bool {
	keys := make(map[string]bool, len(apps))
	for _, a := range apps {
		if k := exeKey(a.Path); k != "" && k != "." {
			keys[k] = true
		}
	}
	return keys
}

// appDisplayName turns an exe path into a name for the list: the file name
// without ".exe".
func appDisplayName(path string) string {
	base := filepath.Base(strings.ReplaceAll(path, "/", `\`))
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

// SetSmartApps replaces the list (a JSON array of {name, path}) and puts it
// into effect: new connections right away, and the listed apps' already-open
// direct ones are closed so they reopen through the tunnel.
func (a *App) SetSmartApps(appsJSON string) {
	var apps []SmartApp
	if err := json.Unmarshal([]byte(appsJSON), &apps); err != nil {
		return
	}
	if apps == nil {
		apps = []SmartApp{}
	}
	saveSmartApps(apps)
	applyAppsPlatform(apps)
	a.resetMisroutedFlows()
}

// GetAdBlock returns {"enabled":..,"supported":..} for the ad-blocking switch.
func (a *App) GetAdBlock() string {
	data, _ := json.Marshal(struct {
		Enabled   bool `json:"enabled"`
		Supported bool `json:"supported"`
	}{loadAdBlock(), adBlockSupported})
	return string(data)
}

// SetAdBlock turns "Блокировать рекламу YouTube" on or off - live, no
// reconnect: the next DNS query already sees it.
func (a *App) SetAdBlock(on bool) {
	saveAdBlock(on)
	applyAdBlockPlatform(on)
}

// ListRunningApps returns the programs running now, as a JSON array of
// {name, path} sorted by name - the picker's "from running processes" list.
// System processes (anything under the Windows folder) are left out.
func (a *App) ListRunningApps() string {
	apps := runningApps()
	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})
	data, err := json.Marshal(apps)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// PickAppExe opens a file dialog for an .exe and returns its path, or "".
func (a *App) PickAppExe() string {
	lang := getTrayLang()
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: trayT(lang, "pick_app_title"),
		Filters: []runtime.FileFilter{
			{DisplayName: trayT(lang, "programs_filter"), Pattern: "*.exe"},
		},
	})
	if err != nil {
		return ""
	}
	return path
}
