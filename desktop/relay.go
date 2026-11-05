package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"phantom/internal/config"
)

// Relays (cmd/relay) on the configs screen: a list of them, one picked, and
// a "use a relay" switch. While it's on, every saved config connects through
// the picked relay. That's the app's setting, not part of any config - the
// configs stay exactly as imported, and the relay is added to each on its way
// to the core (withRelaySetting).

// SavedRelay is one relay in the list. IP/country are what its tile shows,
// cached the same way a config's are.
type SavedRelay struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	IP          string `json:"ip,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"countryCode,omitempty"`
	City        string `json:"city,omitempty"`
	Provider    string `json:"provider,omitempty"`
}

type relaySettings struct {
	Enabled  bool         `json:"enabled"`
	Selected string       `json:"selected"`
	Relays   []SavedRelay `json:"relays"`
}

const (
	relaysFileName = "relays.json"
	// 2.5.0-beta.3 kept a single relay in these two files.
	legacyRelayEnabledFileName = "relay_enabled"
	legacyRelayAddressFileName = "relay_address"
)

var relayMu sync.Mutex

func loadRelays() relaySettings {
	relayMu.Lock()
	defer relayMu.Unlock()
	return loadRelaysLocked()
}

func loadRelaysLocked() relaySettings {
	var s relaySettings
	dir, err := configDir()
	if err != nil {
		return s
	}
	data, err := os.ReadFile(filepath.Join(dir, relaysFileName))
	if err == nil {
		_ = json.Unmarshal(data, &s)
		return s
	}
	// The single relay of 2.5.0-beta.3 becomes the first in the list.
	if old, err := os.ReadFile(filepath.Join(dir, legacyRelayAddressFileName)); err == nil {
		if addr, err := config.NormalizeRelayAddr(string(old)); err == nil {
			r := SavedRelay{ID: uuid.NewString(), Address: addr}
			s.Relays = []SavedRelay{r}
			s.Selected = r.ID
			s.Enabled = loadSetting(legacyRelayEnabledFileName, "0", "1", "0") == "1"
			_ = saveRelaysLocked(s)
		}
	}
	return s
}

func saveRelaysLocked(s relaySettings) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, relaysFileName), data, 0600)
}

// activeAddress is the relay connections go through: the picked one while
// the switch is on, "" otherwise.
func (s relaySettings) activeAddress() string {
	if !s.Enabled {
		return ""
	}
	for _, r := range s.Relays {
		if r.ID == s.Selected {
			return r.Address
		}
	}
	return ""
}

// withRelaySetting returns configYAML as the core should get it: with the
// active relay added, otherwise unchanged. A config the relay can't be added
// to goes through as it is - connecting directly beats not connecting.
func withRelaySetting(configYAML string) string {
	addr := loadRelays().activeAddress()
	if addr == "" {
		return configYAML
	}
	out, err := config.WithRelay(configYAML, addr)
	if err != nil {
		log.Printf("[relay] could not add the relay to a config, connecting directly: %v", err)
		return configYAML
	}
	return out
}

// updateRelays applies change under the lock, saves, and - when that moved
// the active relay - rebuilds a running tunnel so the change applies at once.
func (a *App) updateRelays(change func(*relaySettings) string) string {
	relayMu.Lock()
	s := loadRelaysLocked()
	before := s.activeAddress()
	if msg := change(&s); msg != "" {
		relayMu.Unlock()
		return msg
	}
	err := saveRelaysLocked(s)
	after := s.activeAddress()
	relayMu.Unlock()
	if err != nil {
		return err.Error()
	}
	if before != after {
		diag(diagCatVPN, "relayChanged", "active", after != "")
		// Every config's health was measured on the other path.
		probeSelectorNow()
		go func() {
			if msg := a.ReconnectActive(); msg != "" {
				log.Printf("[relay] reconnect after the relay changed: %s", msg)
			}
		}()
	}
	return ""
}

// GetRelays returns {"enabled","selected","relays":[...]}.
func (a *App) GetRelays() string {
	s := loadRelays()
	if s.Relays == nil {
		s.Relays = []SavedRelay{}
	}
	data, _ := json.Marshal(s)
	return string(data)
}

// SetRelayEnabled turns "use a relay" on or off. Returns "" or an error.
func (a *App) SetRelayEnabled(enabled bool) string {
	return a.updateRelays(func(s *relaySettings) string {
		s.Enabled = enabled
		return ""
	})
}

// SelectRelay picks the relay connections go through.
func (a *App) SelectRelay(id string) string {
	return a.updateRelays(func(s *relaySettings) string {
		for _, r := range s.Relays {
			if r.ID == id {
				s.Selected = id
				return ""
			}
		}
		return "not_found"
	})
}

// AddRelay adds a relay - picked straight away when it's the first - and
// returns its id, or "error:bad_address".
func (a *App) AddRelay(address string) string {
	addr, err := config.NormalizeRelayAddr(address)
	if err != nil {
		return "error:bad_address"
	}
	id := uuid.NewString()
	if msg := a.updateRelays(func(s *relaySettings) string {
		s.Relays = append(s.Relays, SavedRelay{ID: id, Address: addr})
		if s.Selected == "" || len(s.Relays) == 1 {
			s.Selected = id
		}
		return ""
	}); msg != "" {
		return "error:" + msg
	}
	return id
}

// UpdateRelay changes a relay's address. Returns "" or "bad_address".
func (a *App) UpdateRelay(id, address string) string {
	addr, err := config.NormalizeRelayAddr(address)
	if err != nil {
		return "bad_address"
	}
	return a.updateRelays(func(s *relaySettings) string {
		for i := range s.Relays {
			if s.Relays[i].ID == id {
				if s.Relays[i].Address != addr {
					// A different relay: what was cached about the old one is not about it.
					s.Relays[i] = SavedRelay{ID: id, Address: addr}
				}
				return ""
			}
		}
		return "not_found"
	})
}

// DeleteRelay removes a relay. Deleting the picked one picks the first left.
func (a *App) DeleteRelay(id string) string {
	return a.updateRelays(func(s *relaySettings) string {
		kept := s.Relays[:0]
		for _, r := range s.Relays {
			if r.ID != id {
				kept = append(kept, r)
			}
		}
		s.Relays = kept
		if s.Selected == id {
			s.Selected = ""
			if len(kept) > 0 {
				s.Selected = kept[0].ID
			}
		}
		return ""
	})
}

// SetRelayGeo caches a relay's IP and place for its tile.
func (a *App) SetRelayGeo(id, ip, country, countryCode, city, provider string) string {
	relayMu.Lock()
	defer relayMu.Unlock()
	s := loadRelaysLocked()
	for i := range s.Relays {
		if s.Relays[i].ID == id {
			s.Relays[i].IP = strings.TrimSpace(ip)
			s.Relays[i].Country = country
			s.Relays[i].CountryCode = countryCode
			s.Relays[i].City = city
			s.Relays[i].Provider = provider
			if err := saveRelaysLocked(s); err != nil {
				return err.Error()
			}
			return ""
		}
	}
	return "not_found"
}

// PingRelay times a TCP connect to a relay, around the tunnel:
// {"ip":...,"latency_ms":...}, or "{}" when it doesn't answer.
func (a *App) PingRelay(address string) string {
	result, err := pingRelay(address)
	if err != nil {
		return "{}"
	}
	data, _ := json.Marshal(struct {
		IP        string `json:"ip"`
		LatencyMs int64  `json:"latency_ms"`
	}{result.IP, result.LatencyMs})
	return string(data)
}
