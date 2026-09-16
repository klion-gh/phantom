package main

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"phantom/internal/geoip"
	"phantom/internal/pingcheck"
	"phantom/internal/routing"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// How many times a network-change reconnect retries before finally giving up,
// and the gap between attempts on top of however long the failed dial itself
// took (its own timeout is StartWindows's 15s dial context - see wintun.go).
const (
	maxNetworkChangeRetries = 4
	networkChangeRetryDelay = 3 * time.Second
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

	// The selector decides *which* config should be carrying traffic, but
	// connecting is the App's job - so it hands the decision back here rather
	// than reaching into the tunnel itself. Without this hook the selector
	// picks a server and nothing ever acts on it.
	onSelectorSwitch = func(configID string) { a.switchToConfig(configID) }
	applyRoutingToEngine()
	syncSelector()

	go checkAndSelfUpdate(ctx)
}

// switchToConfig connects to a saved config the smart selector has chosen.
// A no-op when that config is already the live one, so a re-pick of the
// current server doesn't tear down a working tunnel.
func (a *App) switchToConfig(configID string) {
	a.mu.Lock()
	alreadyLive := a.tunnel != nil && a.activeConfigID == configID
	a.mu.Unlock()
	if alreadyLive {
		return
	}

	configs, err := loadConfigs()
	if err != nil {
		log.Printf("[routing] auto-switch could not read configs: %v", err)
		return
	}
	for _, c := range configs {
		if c.ID != configID {
			continue
		}
		log.Printf("[routing] auto-selecting config %s", configID)
		if msg := a.Connect(c.ID, c.Yaml); msg != "" {
			log.Printf("[routing] auto-connect failed: %s", msg)
			return
		}
		// Nudge the UI instead of making it wait for the next Status() poll.
		runtime.EventsEmit(a.ctx, "routing:switched", configID)
		return
	}
	log.Printf("[routing] auto-selected config %s no longer exists", configID)
}

// ReconnectActive rebuilds the tunnel for whatever config is currently
// connected, re-applying routing settings from scratch. Returns "" on
// success, an error message on failure, or "" as a no-op if nothing is
// connected (there's nothing to reconnect).
//
// A brand new connection already honours an edited smart-VPN site list
// immediately - the netstack asks the shared engine fresh on every flow, no
// reconnect needed. This exists for the flow that ISN'T new: a browser tab
// already open to a site before it was added, sitting on a warm connection
// dialed under the old list. Nothing re-evaluates an established connection's
// routing on its own, so the only reliable way to make the browser open a
// fresh one is to make the tunnel disappear out from under it.
func (a *App) ReconnectActive() string {
	a.mu.Lock()
	connected := a.tunnel != nil
	configID := a.activeConfigID
	a.mu.Unlock()
	if !connected {
		return ""
	}

	configs, err := loadConfigs()
	if err != nil {
		return err.Error()
	}
	for _, c := range configs {
		if c.ID == configID {
			log.Printf("[routing] reconnecting %s to apply routing changes", configID)
			return a.Connect(c.ID, c.Yaml)
		}
	}
	return "active configuration no longer exists"
}

// shutdown runs when the window is closed. Without this, closing the window
// while connected (instead of clicking Disconnect first) would leave the TUN
// adapter, routing table entries, and pooled connections dangling. In
// practice this now only fires on a real process exit (the tray's "Выход"
// calls Disconnect itself before os.Exit, which skips this hook entirely) -
// kept as a safety net for any other path that tears the app down.
func (a *App) shutdown(ctx context.Context) {
	a.Disconnect()
	stopAllConfigProxies()
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
		a.attemptReconnect(configID, configYAML, 1)
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

// attemptReconnect retries a network-change-triggered reconnect with a short
// backoff, up to maxNetworkChangeRetries times, before finally giving up.
//
// A single slow or failed TLS handshake right after a network change is far
// more often transient - the network is still settling, a DNS server hasn't
// updated yet - than it is terminal. Without this, the very first miss just
// leaves the tunnel down (Connect() already tore down any previous one before
// this dial even started) and nothing ever tries again: the user is left
// thinking the VPN is on when it silently isn't, until they notice and
// reconnect by hand.
func (a *App) attemptReconnect(configID, configYAML string, attempt int) {
	log.Printf("reconnecting after network change (attempt %d/%d)", attempt, maxNetworkChangeRetries)
	errMsg := a.Connect(configID, configYAML)
	if errMsg == "" {
		return
	}
	log.Printf("network-change reconnect attempt %d failed: %s", attempt, errMsg)
	if attempt >= maxNetworkChangeRetries {
		log.Printf("giving up reconnecting after %d attempts", maxNetworkChangeRetries)
		return
	}
	time.AfterFunc(networkChangeRetryDelay, func() {
		a.attemptReconnect(configID, configYAML, attempt+1)
	})
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

// LookupCountry resolves which country a server IP sits in, for the location
// label on a saved-config tile. Returns a JSON blob
// {"country":"Netherlands","country_code":"NL"}, or an empty string on failure so
// the frontend can retry rather than pin a blank label.
//
// This asks a third-party geolocation service and so tells it the address of the
// user's server - see internal/geoip for the trade-off and the constraints:
// resolved once per saved config and pinned, never polled.
func (a *App) LookupCountry(ip string) string {
	res, err := geoip.Lookup(context.Background(), ip)
	if err != nil {
		log.Printf("country lookup for a saved config failed: %v", err)
		return ""
	}
	data, err := json.Marshal(struct {
		Country     string `json:"country"`
		CountryCode string `json:"country_code"`
	}{Country: res.Country, CountryCode: res.CountryCode})
	if err != nil {
		return ""
	}
	return string(data)
}

// Version is the running build's version string, shown at the bottom of the
// settings panel. Read from the same constant the updater compares against
// GitHub's latest release tag, so what the user is shown is exactly what decides
// whether an update is offered.
func (a *App) Version() string {
	return AppVersion
}

// SetConfigGeo persists a saved config's tile metadata. An empty argument means
// "leave that field alone", so the country (a field in the yaml, no network) and
// the IP (a Ping, needs connectivity) can be written independently - see
// setConfigGeo for why that separation matters.
func (a *App) SetConfigGeo(id string, ip string, country string, countryCode string) string {
	if err := setConfigGeo(id, ip, country, countryCode); err != nil {
		return err.Error()
	}
	return ""
}

// ClearConfigCountry blanks a saved config's cached country/country_code -
// the frontend calls this right before re-resolving them once it notices the
// config's dialed IP has changed (see resolveTileMetadata in main.js), so a
// label from the old server doesn't linger while the new one is being looked
// up. Returns "" on success or an error message.
func (a *App) ClearConfigCountry(id string) string {
	if err := clearConfigCountry(id); err != nil {
		return err.Error()
	}
	return ""
}

// DeleteConfig removes a saved config, disconnecting first if it's the one
// currently active (otherwise the tunnel would keep running with no tile
// left in the UI to represent or control it) and stopping its independent
// proxy if one is running, for the same reason.
func (a *App) DeleteConfig(id string) string {
	a.mu.Lock()
	if a.activeConfigID == id && a.tunnel != nil {
		a.tunnel.Stop()
		a.tunnel = nil
		a.activeConfigID = ""
	}
	a.mu.Unlock()
	stopConfigProxy(id)

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
	lang := getTrayLang()
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: trayT(lang, "pick_app_title"),
		Filters: []runtime.FileFilter{
			{DisplayName: trayT(lang, "programs_filter"), Pattern: "*.exe"},
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

type proxyStatusResponse struct {
	Running bool   `json:"running"`
	Port    int    `json:"port"`
	Error   string `json:"error"`
}

// StartProxy starts (or, if already running, just reports) configID's
// independent local SOCKS5 proxy on requestedPort (0 = any free port) - see
// windows/proxymanager.go. Unlike Connect/the full-tunnel VPN, this doesn't
// touch routes or a TUN adapter at all, and works whether or not the full
// VPN is active for this or any other config. The frontend's port field is
// only editable while the proxy is off, pre-filled with whatever port this
// config used successfully last time; failing to bind requestedPort is
// returned as a real error rather than silently substituting a different
// port, so the user finds out and can pick another one themselves. On
// success, whichever port actually got used is remembered for next time.
// Returns {"running":true,"port":N} on success or
// {"running":false,"error":"..."} on failure.
func (a *App) StartProxy(configID string, configYAML string, requestedPort int) string {
	port, err := startConfigProxy(configID, configYAML, requestedPort)
	if err == nil {
		if saveErr := setConfigProxyPort(configID, port); saveErr != nil {
			log.Printf("failed to remember proxy port for %s: %v", configID, saveErr)
		}
	}

	resp := proxyStatusResponse{Running: err == nil, Port: port}
	if err != nil {
		resp.Error = err.Error()
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

// StopProxy stops configID's independent proxy, if one is running.
func (a *App) StopProxy(configID string) string {
	stopConfigProxy(configID)
	return ""
}

// GetLanguage returns the persisted UI language ("ru" or "en") - the frontend
// reads it once on load to pick the initial language.
func (a *App) GetLanguage() string {
	return loadLanguage()
}

// GetAppearance returns the persisted look as
// {"palette":"midnight","background":"orbs"}. Read once at startup and
// applied before the first paint, so the window doesn't flash the default
// palette on its way to the chosen one.
func (a *App) GetAppearance() string {
	data, _ := json.Marshal(struct {
		Palette    string `json:"palette"`
		Background string `json:"background"`
	}{Palette: loadPalette(), Background: loadBackground()})
	return string(data)
}

// SetAppearance persists the colour palette (see settings.go's palettes) and
// animated-backdrop variant (see backgrounds). Unrecognised values fall back
// to the defaults - see saveSetting.
func (a *App) SetAppearance(palette string, background string) {
	savePalette(palette)
	saveBackground(background)
}

// SetLanguage persists the chosen UI language and re-labels the tray menu to
// match (the WebView side re-renders itself). "ru" or "en"; anything else is
// stored as "ru".
func (a *App) SetLanguage(lang string) {
	saveLanguage(lang)
	setTrayLang(lang)
	refreshTrayLanguage()
}

// --- routing (Маршрутизация section) ---------------------------------------
//
// The decision logic is shared with Android (internal/routing); these are just
// the accessors the WebView calls. Every setter re-applies the settings to the
// live engine, so edits take effect on the next connection an app opens rather
// than needing a reconnect.

// GetRoutingState returns everything the Маршрутизация section renders from,
// as one JSON blob - a single round trip instead of six, and no chance of the
// UI seeing a half-updated mix of old and new values.
func (a *App) GetRoutingState() string {
	data, _ := json.Marshal(struct {
		Mode          string   `json:"mode"`
		SmartEnabled  bool     `json:"smartEnabled"`
		Sites         []string `json:"sites"`
		SmartConfigs  []string `json:"smartConfigs"`
		AutoEnabled   bool     `json:"autoEnabled"`
		AutoConfigs   []string `json:"autoConfigs"`
		AppsEnabled   bool     `json:"appsEnabled"`
		AppsInclude   bool     `json:"appsInclude"`
	}{
		Mode:         loadRoutingMode(),
		SmartEnabled: loadSmartEnabled(),
		Sites:        loadSmartSites(),
		SmartConfigs: loadSmartConfigs(),
		AutoEnabled:  loadAutoEnabled(),
		AutoConfigs:  loadAutoConfigs(),
		AppsEnabled:  loadAppsEnabled(),
		AppsInclude:  loadAppsInclude(),
	})
	return string(data)
}

// SetRoutingMode switches between per-app exclusion and per-site inclusion.
func (a *App) SetRoutingMode(mode string) {
	saveRoutingMode(mode)
	applyRoutingToEngine()
	syncSelector()
}

// SetSmartEnabled turns smart routing on or off.
func (a *App) SetSmartEnabled(enabled bool) {
	saveSmartEnabled(enabled)
	applyRoutingToEngine()
	syncSelector()
}

// SetSmartSites replaces the routed-site list. sites is newline-separated,
// matching the textarea the UI edits it in.
func (a *App) SetSmartSites(sites string) {
	saveSmartSites(splitSiteLines(sites))
	applyRoutingToEngine()
}

// SetSmartConfigs replaces the configs smart mode may choose between.
func (a *App) SetSmartConfigs(idsJSON string) {
	var ids []string
	if err := json.Unmarshal([]byte(idsJSON), &ids); err != nil {
		return
	}
	saveSmartConfigs(ids)
	syncSelector()
}

// SetAutoEnabled turns whole-device automatic routing on or off.
func (a *App) SetAutoEnabled(enabled bool) {
	saveAutoEnabled(enabled)
	applyRoutingToEngine()
	syncSelector()
}

// SetAutoConfigs narrows which configs "Автоматически" chooses between. An
// empty list means "all of them".
func (a *App) SetAutoConfigs(idsJSON string) {
	var ids []string
	if err := json.Unmarshal([]byte(idsJSON), &ids); err != nil {
		return
	}
	saveAutoConfigs(ids)
	syncSelector()
}

// SetAppsEnabled turns per-app routing on or off.
func (a *App) SetAppsEnabled(enabled bool) {
	saveAppsEnabled(enabled)
	applyRoutingToEngine()
}

// SetAppsInclude picks which way the app list is read - see loadAppsInclude.
func (a *App) SetAppsInclude(include bool) {
	saveAppsInclude(include)
}

// RoutingHealth returns per-config health from the smart selector for the UI's
// own rows, as [{"id":..,"alive":..,"latency_ms":..,"probed":..,"active":..}].
func (a *App) RoutingHealth() string { return selectorHealthJSON() }

// PopularResources returns the built-in catalogue of commonly-blocked services
// for the picker. Shared with Android so both clients show the same list.
func (a *App) PopularResources() string { return routing.PopularResourcesJSON() }

// UILog lets the frontend put an uncaught error into the app log. The WebView
// has no console the user can open, so without this a JS failure is invisible
// to both them and anyone reading a bug report.
func (a *App) UILog(msg string) { log.Printf("[ui] %s", msg) }

// GetShowProxySettings returns whether the per-config proxy button and port
// field should be shown - read once at startup, same as GetAppearance.
func (a *App) GetShowProxySettings() bool {
	return loadShowProxySettings()
}

// SetShowProxySettings persists the show/hide choice for the proxy controls.
func (a *App) SetShowProxySettings(show bool) {
	saveShowProxySettings(show)
}

// ApplyUpdate downloads and installs whatever release checkAndSelfUpdate
// found (see the "update:available" event) - swapping in the new exe and
// relaunching. Only ever triggered by the user clicking the update button;
// the app no longer installs a found update on its own. Returns an error
// message on failure, or "" if there was nothing pending - on success this
// call never actually returns, since the process relaunches and exits.
func (a *App) ApplyUpdate() string {
	return applyPendingUpdate(a.ctx)
}
