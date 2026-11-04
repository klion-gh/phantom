package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	"phantom/internal/config"
)

// The "use a relay" setting on the configs screen: one relay (cmd/relay) that
// every saved config connects through while it's on. It is the app's setting,
// not part of any config - the configs stay exactly as imported, and the relay
// is added to each on its way to the core (withRelaySetting).
const (
	relayEnabledFileName = "relay_enabled"
	relayAddressFileName = "relay_address"
)

func loadRelay() (enabled bool, address string) {
	enabled = loadSetting(relayEnabledFileName, "0", "1", "0") == "1"
	dir, err := configDir()
	if err != nil {
		return enabled, ""
	}
	data, err := os.ReadFile(filepath.Join(dir, relayAddressFileName))
	if err != nil {
		return enabled, ""
	}
	address, err = config.NormalizeRelayAddr(string(data))
	if err != nil {
		return enabled, ""
	}
	return enabled, address
}

func saveRelay(enabled bool, address string) {
	value := "0"
	if enabled {
		value = "1"
	}
	saveSetting(relayEnabledFileName, value, "0", "1", "0")
	if dir, err := configDir(); err == nil {
		_ = os.WriteFile(filepath.Join(dir, relayAddressFileName), []byte(address), 0600)
	}
}

// withRelaySetting returns configYAML as the core should get it: with the
// relay added while the setting is on, otherwise unchanged. A config the
// relay can't be added to goes through as it is - connecting directly beats
// not connecting.
func withRelaySetting(configYAML string) string {
	enabled, address := loadRelay()
	if !enabled || address == "" {
		return configYAML
	}
	out, err := config.WithRelay(configYAML, address)
	if err != nil {
		log.Printf("[relay] could not add the relay to a config, connecting directly: %v", err)
		return configYAML
	}
	return out
}

// GetRelay returns the relay setting as {"enabled":bool,"address":string}.
func (a *App) GetRelay() string {
	enabled, address := loadRelay()
	data, _ := json.Marshal(struct {
		Enabled bool   `json:"enabled"`
		Address string `json:"address"`
	}{enabled, address})
	return string(data)
}

// SetRelay saves the relay setting and, when a tunnel is up, rebuilds it so
// the change applies at once. Returns "" or "bad_address" (an i18n key) when
// address can't be one; turning the relay off never needs a valid address.
func (a *App) SetRelay(enabled bool, address string) string {
	normalized := ""
	if strings.TrimSpace(address) != "" {
		var err error
		if normalized, err = config.NormalizeRelayAddr(address); err != nil {
			return "bad_address"
		}
	}
	if enabled && normalized == "" {
		return "bad_address"
	}
	oldEnabled, oldAddress := loadRelay()
	saveRelay(enabled, normalized)
	diag(diagCatVPN, "setRelay", "enabled", enabled)

	changed := oldEnabled != enabled || (enabled && oldAddress != normalized)
	if changed {
		go func() {
			if msg := a.ReconnectActive(); msg != "" {
				log.Printf("[relay] reconnect after the relay changed: %s", msg)
			}
		}()
	}
	return ""
}
