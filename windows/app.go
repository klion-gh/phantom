package main

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"phantom/internal/pingcheck"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails-bound backend: every exported method here is directly
// callable from the frontend's JS (window.go.main.App.*).
type App struct {
	ctx context.Context

	mu             sync.Mutex
	tunnel         *WinTunnel
	activeConfigID string
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	initLog()
	log.Println("App started")
	go checkAndSelfUpdate(ctx)
}

// shutdown runs when the window is closed. Without this, closing the window
// while connected (instead of clicking Disconnect first) would leave the TUN
// adapter, routing table entries, and pooled connections dangling. In
// practice this now only fires on a real process exit (the tray's "Выход"
// calls Disconnect itself before os.Exit, which skips this hook entirely) -
// kept as a safety net for any other path that tears the app down.
func (a *App) shutdown(ctx context.Context) {
	a.Disconnect()
}

// beforeClose runs when the user clicks the window's close button. Returning
// true cancels the default close-and-quit behavior; hiding the window
// instead is what makes the app "minimize to tray" - the process (and any
// active tunnel) keeps running until "Выход" is chosen from the tray menu.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	runtime.WindowHide(ctx)
	return true
}

// Connect blocks until the tunnel is either up or has definitively failed
// (StartWindows has its own internal dial timeout), returning an empty
// string on success or an error message otherwise. The frontend sets its own
// "connecting" UI state immediately after calling this, the same way the
// Android app's button does, rather than needing a separate polling step for
// this specific transition. Switching from one saved config to another reuses
// this same call - any existing tunnel is torn down first, which is also
// exactly what happens when the network-change watch below decides to
// reconnect from scratch.
func (a *App) Connect(configID string, configYAML string) string {
	a.mu.Lock()
	if a.tunnel != nil {
		a.tunnel.Stop()
		a.tunnel = nil
		a.activeConfigID = ""
	}
	a.mu.Unlock()

	log.Println("connect: establishing tunnel")
	tun, err := StartWindows(configYAML, func() {
		log.Println("underlying network changed, reconnecting")
		runtime.EventsEmit(a.ctx, "tunnel:reconnecting")
		if errMsg := a.Connect(configID, configYAML); errMsg != "" {
			log.Printf("network-change reconnect failed: %s", errMsg)
		}
	})
	if err != nil {
		log.Printf("connect failed: %v", err)
		return err.Error()
	}

	a.mu.Lock()
	a.tunnel = tun
	a.activeConfigID = configID
	a.mu.Unlock()
	saveLastActiveID(configID)
	log.Println("connected")
	return ""
}

func (a *App) Disconnect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tunnel == nil {
		return
	}
	a.tunnel.Stop()
	a.tunnel = nil
	a.activeConfigID = ""
	log.Println("disconnected")
}

type statusResponse struct {
	Connected      bool   `json:"connected"`
	Alive          bool   `json:"alive"`
	Stats          string `json:"stats"`
	ActiveConfigID string `json:"activeConfigId"`
}

// Status reports whether a tunnel is currently up, and which saved config it
// belongs to. The frontend polls this while "connected" to detect an
// unexpected drop (Alive=false while Connected=true means the session died
// without an explicit Disconnect).
func (a *App) Status() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	resp := statusResponse{Connected: a.tunnel != nil, ActiveConfigID: a.activeConfigID}
	if a.tunnel != nil {
		resp.Alive = a.tunnel.IsAlive()
		resp.Stats = a.tunnel.Stats()
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

// ReadLog returns the full contents of the log file for the in-app viewer.
func (a *App) ReadLog() string {
	return readLog()
}

// ListConfigs returns every saved config as a JSON array of {"id","yaml"}.
func (a *App) ListConfigs() string {
	configs, err := loadConfigs()
	if err != nil {
		return "[]"
	}
	data, err := json.Marshal(configs)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// AddConfig saves configYAML as a brand new tile (never overwrites an
// existing one - that's UpdateConfig's job). Returns the new config's ID on
// success (unlike most other methods here, which return "" for success -
// the frontend needs the ID to attach a one-time geo lookup via
// SetConfigGeo right after saving) or "" on failure.
func (a *App) AddConfig(configYAML string) string {
	cfg, err := addConfig(configYAML)
	if err != nil {
		log.Printf("AddConfig failed: %v", err)
		return ""
	}
	return cfg.ID
}

// UpdateConfig overwrites the yaml of an existing saved config in place,
// clearing any previously cached geo data (see updateConfig) since the
// edited yaml may point at a different server. Returns "" on success or an
// error message.
func (a *App) UpdateConfig(id string, configYAML string) string {
	if err := updateConfig(id, configYAML); err != nil {
		return err.Error()
	}
	return ""
}

// SetConfigGeo persists the one-time-resolved server IP/country/flag for a
// saved config. Called by the frontend right after Add/UpdateConfig, once a
// Ping and a geo-IP lookup (both done client-side in JS) have completed -
// see internal/pingcheck and the "why once, not on a timer" note on
// SavedConfig for the reasoning.
func (a *App) SetConfigGeo(id string, ip string, country string, countryCode string) string {
	if err := setConfigGeo(id, ip, country, countryCode); err != nil {
		return err.Error()
	}
	return ""
}

// DeleteConfig removes a saved config, disconnecting first if it's the one
// currently active (otherwise the tunnel would keep running with no tile
// left in the UI to represent or control it).
func (a *App) DeleteConfig(id string) string {
	a.mu.Lock()
	if a.activeConfigID == id && a.tunnel != nil {
		a.tunnel.Stop()
		a.tunnel = nil
		a.activeConfigID = ""
	}
	a.mu.Unlock()

	if err := deleteConfig(id); err != nil {
		return err.Error()
	}
	return ""
}

// Ping previews a saved config's server: one real disguised handshake (no
// tunnel built), returning {"ip":...,"latency_ms":...} - or "{}" on any
// failure (unreachable, bad config), which the frontend treats as "no data
// yet" rather than a hard error since this runs on a background timer.
func (a *App) Ping(configYAML string) string {
	result, err := pingcheck.Ping(configYAML)
	if err != nil {
		return "{}"
	}
	data, err := json.Marshal(struct {
		IP        string `json:"ip"`
		LatencyMs int64  `json:"latency_ms"`
	}{IP: result.IP, LatencyMs: result.LatencyMs})
	if err != nil {
		return "{}"
	}
	return string(data)
}

// ListResources returns every saved resource-reachability tile as a JSON
// array of {"id","name","url"} - seeded with a handful of well-known sites
// on first run. Actual reachability checks happen in the frontend (plain
// fetch(), subject to the real system routing table - see PingResource).
func (a *App) ListResources() string {
	resources, err := loadResources()
	if err != nil {
		return "[]"
	}
	data, err := json.Marshal(resources)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// AddResource saves a new user-defined resource tile. Returns "" on success
// or an error message.
func (a *App) AddResource(name string, url string) string {
	if _, err := addResource(name, url); err != nil {
		return err.Error()
	}
	return ""
}

// DeleteResource removes a resource tile (built-in or user-added).
func (a *App) DeleteResource(id string) string {
	if err := deleteResource(id); err != nil {
		return err.Error()
	}
	return ""
}

// ListExcludedApps returns the split-tunneling exclusion list as a JSON array
// of {"id","name","exePath"} - apps on this list bypass the tunnel entirely
// (see windows/splittunnel.go).
func (a *App) ListExcludedApps() string {
	apps, err := loadExcludedApps()
	if err != nil {
		return "[]"
	}
	data, err := json.Marshal(apps)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// PickExcludedAppExe opens a native file-open dialog for the user to browse
// to an .exe, returning its path (or "" if cancelled/failed) - the frontend
// follows this up with AddExcludedApp using the picked path.
func (a *App) PickExcludedAppExe() string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Выбрать приложение",
		Filters: []runtime.FileFilter{
			{DisplayName: "Программы (*.exe)", Pattern: "*.exe"},
		},
	})
	if err != nil || path == "" {
		return ""
	}
	return path
}

// AddExcludedApp adds exePath to the split-tunneling exclusion list under the
// given display name. Returns "" on success or an error message.
func (a *App) AddExcludedApp(name string, exePath string) string {
	if _, err := addExcludedApp(name, exePath); err != nil {
		return err.Error()
	}
	return ""
}

// DeleteExcludedApp removes an app from the split-tunneling exclusion list.
func (a *App) DeleteExcludedApp(id string) string {
	if err := deleteExcludedApp(id); err != nil {
		return err.Error()
	}
	return ""
}
