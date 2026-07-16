package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// SavedConfig is one saved client.yaml, shown as its own tile in the UI -
// mirrors the Android app's ConfigStore.SavedConfig exactly (same JSON shape
// isn't required since the two apps don't share storage, but keeping the
// model identical avoids re-deriving the design twice).
//
// IP/Country/CountryCode are resolved once (via a Ping + a geo-IP lookup)
// right after the config is added or edited, not on every ping cycle - the
// server behind a saved config essentially never moves, so re-resolving its
// location every few seconds on a timer was just wasted third-party calls
// (and is what rate-limited the geo-IP provider into 429s during
// development). They're blank until the frontend calls SetConfigGeo once.
type SavedConfig struct {
	ID          string `json:"id"`
	Yaml        string `json:"yaml"`
	IP          string `json:"ip,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"countryCode,omitempty"`
	// ProxyPort is the independent SOCKS5 proxy's port (see proxymanager.go),
	// remembered once it's first assigned so it stays the same across
	// restarts/toggles - otherwise every restart would bind a fresh
	// OS-assigned port, forcing whatever else points at it (e.g. Telegram's
	// own proxy settings) to be reconfigured each time.
	ProxyPort int `json:"proxyPort,omitempty"`
}

const (
	configsFileName      = "configs.json"
	legacyConfigFileName = "client.yaml" // pre-multi-config single entry
	lastActiveIDFileName = "last_active_id"
)

func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	full := filepath.Join(dir, "Phantom")
	if err := os.MkdirAll(full, 0700); err != nil {
		return "", err
	}
	return full, nil
}

func loadConfigs() ([]SavedConfig, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, configsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		return migrateLegacyConfig(dir)
	}
	var configs []SavedConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		return nil, err
	}
	return configs, nil
}

// migrateLegacyConfig converts the pre-multi-config single client.yaml file
// (from before tiles existed) into a one-entry list, once.
func migrateLegacyConfig(dir string) ([]SavedConfig, error) {
	legacyPath := filepath.Join(dir, legacyConfigFileName)
	legacyData, err := os.ReadFile(legacyPath)
	if err != nil || len(legacyData) == 0 {
		return []SavedConfig{}, nil
	}
	migrated := []SavedConfig{{ID: uuid.NewString(), Yaml: string(legacyData)}}
	if err := saveConfigs(dir, migrated); err != nil {
		return nil, err
	}
	os.Remove(legacyPath)
	return migrated, nil
}

func saveConfigs(dir string, configs []SavedConfig) error {
	data, err := json.Marshal(configs)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, configsFileName), data, 0600)
}

func addConfig(yaml string) (SavedConfig, error) {
	dir, err := configDir()
	if err != nil {
		return SavedConfig{}, err
	}
	configs, err := loadConfigs()
	if err != nil {
		return SavedConfig{}, err
	}
	cfg := SavedConfig{ID: uuid.NewString(), Yaml: yaml}
	if err := saveConfigs(dir, append(configs, cfg)); err != nil {
		return SavedConfig{}, err
	}
	return cfg, nil
}

func updateConfig(id, yaml string) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	configs, err := loadConfigs()
	if err != nil {
		return err
	}
	for i := range configs {
		if configs[i].ID == id {
			// Clear any previously cached geo data - the edited yaml may point at a
			// different server entirely, so the old IP/country would be stale until
			// SetConfigGeo re-resolves it.
			configs[i].Yaml = yaml
			configs[i].IP = ""
			configs[i].Country = ""
			configs[i].CountryCode = ""
		}
	}
	return saveConfigs(dir, configs)
}

// setConfigGeo persists the one-time-resolved IP/country/flag for a saved
// config - called by the frontend right after Add/UpdateConfig, once a Ping
// and a geo-IP lookup have completed.
func setConfigGeo(id, ip, country, countryCode string) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	configs, err := loadConfigs()
	if err != nil {
		return err
	}
	for i := range configs {
		if configs[i].ID == id {
			configs[i].IP = ip
			configs[i].Country = country
			configs[i].CountryCode = countryCode
		}
	}
	return saveConfigs(dir, configs)
}

// setConfigProxyPort persists the independent proxy's bound port for a saved
// config - called the first time it's started (or if its previously
// remembered port turned out to be unavailable and a different one had to be
// used instead), so the next start reuses the same port.
func setConfigProxyPort(id string, port int) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	configs, err := loadConfigs()
	if err != nil {
		return err
	}
	for i := range configs {
		if configs[i].ID == id {
			configs[i].ProxyPort = port
		}
	}
	return saveConfigs(dir, configs)
}

func deleteConfig(id string) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	configs, err := loadConfigs()
	if err != nil {
		return err
	}
	filtered := configs[:0]
	for _, c := range configs {
		if c.ID != id {
			filtered = append(filtered, c)
		}
	}
	return saveConfigs(dir, filtered)
}

func loadLastActiveID() string {
	dir, err := configDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, lastActiveIDFileName))
	if err != nil {
		return ""
	}
	return string(data)
}

func saveLastActiveID(id string) {
	dir, err := configDir()
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, lastActiveIDFileName), []byte(id), 0600)
}
