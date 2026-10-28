package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"phantom/internal/routing"
)

// How long a single candidate's probe is allowed to run before it's counted
// as unreachable this round - see routing.WithProbeTimeout.
const probeTimeout = 6 * time.Second

// The Windows half of the routing feature. The decision logic itself lives in
// internal/routing and is shared with Android; what's here is the platform
// glue - persistence, the direct dialer, and the Wails bindings the UI calls.

const (
	smartEnabledFileName = "smart_vpn_enabled"
	smartSitesFileName   = "smart_vpn_sites"
	smartConfigsFileName = "smart_vpn_configs"
	autoEnabledFileName  = "auto_config_enabled"
	autoConfigsFileName  = "auto_config_configs"
)

// Files the removed per-app split tunneling kept - its mode switch ("по
// приложениям" vs Умный VPN), its own on/off and include/exclude switches, and
// the app list. Nothing reads them any more; removeSplitTunnelLeftovers
// deletes them so they don't linger in the config dir.
var splitTunnelLeftovers = []string{"routing_mode", "apps_mode_enabled", "apps_mode_include", "split_tunnel.json"}

// removeSplitTunnelLeftovers deletes splitTunnelLeftovers. Idempotent - after
// the first start on a build without split tunneling there's nothing left.
func removeSplitTunnelLeftovers() {
	dir, err := configDir()
	if err != nil {
		return
	}
	for _, name := range splitTunnelLeftovers {
		os.Remove(filepath.Join(dir, name))
	}
}

var (
	selectorMu sync.Mutex
	selector   *routing.Selector
	// Set by the App so the selector can ask the UI layer to reconnect.
	onSelectorSwitch func(configID string)
)

// --- persistence -----------------------------------------------------------

func loadSmartEnabled() bool { return loadSetting(smartEnabledFileName, "0", "1", "0") == "1" }

func saveSmartEnabled(on bool) {
	saveSetting(smartEnabledFileName, boolToSetting(on), "0", "1", "0")
}

func loadAutoEnabled() bool { return loadSetting(autoEnabledFileName, "0", "1", "0") == "1" }

func saveAutoEnabled(on bool) {
	saveSetting(autoEnabledFileName, boolToSetting(on), "0", "1", "0")
}

func boolToSetting(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

// readSettingFile/writeSettingFile are the unvalidated cousins of
// loadSetting/saveSetting in settings.go: those check the value against a
// fixed set of allowed tokens, which by design can't express a user-typed
// list. Same one-file-per-key layout.
func readSettingFile(name string) string {
	dir, err := configDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeSettingFile(name, value string) {
	dir, err := configDir()
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name), []byte(value), 0600)
}

// Site and config-id lists are stored as JSON rather than through
// loadSetting/saveSetting, which validates against a fixed set of allowed
// tokens and so can't express "arbitrary user-typed list".
// Always returns a non-nil slice: a nil one marshals to JSON `null`, and the
// frontend then calls .map()/.some() on it and throws - which silently killed
// every handler registered after that point rather than failing visibly.
func loadStringList(name string) []string {
	out := []string{}
	raw := readSettingFile(name)
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

func saveStringList(name string, values []string) {
	data, err := json.Marshal(values)
	if err != nil {
		return
	}
	writeSettingFile(name, string(data))
}

func loadSmartSites() []string { return loadStringList(smartSitesFileName) }

func saveSmartSites(sites []string) { saveStringList(smartSitesFileName, sites) }

func loadSmartConfigs() []string { return loadStringList(smartConfigsFileName) }

func saveSmartConfigs(ids []string) { saveStringList(smartConfigsFileName, ids) }

func loadAutoConfigs() []string { return loadStringList(autoConfigsFileName) }

func saveAutoConfigs(ids []string) { saveStringList(autoConfigsFileName, ids) }

// --- wiring into a running tunnel ------------------------------------------

// applyRoutingToEngine pushes the persisted settings into the routing engine -
// this process's own on Windows, phantomd's on Linux.
// Called at startup and after every edit; the tunnel reads the engine per
// flow, so this takes effect without reconnecting.
//
// DNS pre-seeding for the site list (net.LookupIP, not just sniffing DNS as
// it crosses the tunnel) now lives in Engine.SetSites itself - shared with
// Android via mobile.Tunnel.SetSmartRouting, rather than duplicated per
// platform.
//
// Smart routing is off whenever "Автоматически" is on: whole-device routing
// means everything is tunnelled, and leaving a site filter underneath it would
// quietly contradict that.
//
// The returned channel closes once the new list's DNS pre-seeding is done -
// see applySiteList, which waits on it.
func applyRoutingToEngine() <-chan struct{} {
	return applyRoutingPlatform(loadSmartSites(), loadSmartEnabled() && !loadAutoEnabled())
}

// seedWaitLimit bounds how long applySiteList waits for the new list's
// addresses before resetting flows anyway: a name that doesn't resolve
// shouldn't hold up the ones that did.
const seedWaitLimit = 4 * time.Second

// applySiteList saves a new smart-VPN site list and puts it into effect -
// including for connections that are already open. Routing is decided when a
// connection opens, so without the reset a browser tab already on a newly
// added site kept going direct over its warm connections; once the new
// entries' addresses are known, those connections are closed and the browser
// reopens them through the tunnel (see netstack.Tunnel.ResetMisroutedFlows).
func applySiteList(a *App, sites []string) {
	saveSmartSites(sites)
	seeded := applyRoutingToEngine()
	go func() {
		select {
		case <-seeded:
		case <-time.After(seedWaitLimit):
		}
		a.resetMisroutedFlows()
	}()
}

// --- smart config selection ------------------------------------------------

// syncSelector rebuilds the smart selector from the current settings, starting
// or stopping it to match. Same reconciliation role as Android's
// RoutingController.sync.
func syncSelector() {
	selectorMu.Lock()
	defer selectorMu.Unlock()

	candidates := selectorCandidates()
	if len(candidates) == 0 {
		if selector != nil {
			selector.Stop()
			selector = nil
		}
		return
	}

	if selector == nil {
		selector = routing.NewSelector(
			routing.WithProbeTimeout(func(configYAML string) (int64, error) {
				result, err := pingConfig(configYAML)
				if err != nil {
					return 0, err
				}
				return result.LatencyMs, nil
			}, probeTimeout),
			func(c routing.Candidate) {
				if onSelectorSwitch != nil {
					onSelectorSwitch(c.ID)
				}
			},
		)
		selector.SetCandidates(candidates)
		selector.Start()
		return
	}
	selector.SetCandidates(candidates)
}

// selectorCandidates mirrors Android's rule: "Автоматически" wins, and an
// empty selection under it means every saved config is fair game (the point of
// the toggle is not having to choose).
func selectorCandidates() []routing.Candidate {
	configs, err := loadConfigs()
	if err != nil {
		return nil
	}

	var ids map[string]bool
	switch {
	case loadAutoEnabled():
		chosen := loadAutoConfigs()
		if len(chosen) > 0 {
			ids = toSet(chosen)
		}
	case loadSmartEnabled():
		ids = toSet(loadSmartConfigs())
		if len(ids) == 0 {
			return nil
		}
	default:
		return nil
	}

	out := make([]routing.Candidate, 0, len(configs))
	for _, c := range configs {
		if ids == nil || ids[c.ID] {
			out = append(out, routing.Candidate{ID: c.ID, YAML: c.Yaml})
		}
	}
	return out
}

func toSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

func selectorHealthJSON() string {
	selectorMu.Lock()
	active := selector
	selectorMu.Unlock()
	if active == nil {
		return "[]"
	}
	type health struct {
		routing.Health
		Active bool `json:"active"`
	}
	current := active.Current()
	snapshot := active.Snapshot()
	out := make([]health, 0, len(snapshot))
	for _, h := range snapshot {
		out = append(out, health{Health: h, Active: h.ID == current})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func probeSelectorNow() {
	selectorMu.Lock()
	active := selector
	selectorMu.Unlock()
	if active != nil {
		go active.Probe()
	}
}

// --- helpers ---------------------------------------------------------------

// splitSiteLines turns the UI's newline-separated textarea content into the
// entry list the engine expects.
func splitSiteLines(s string) []string {
	raw := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
