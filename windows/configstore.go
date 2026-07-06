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
type SavedConfig struct {
	ID   string `json:"id"`
	Yaml string `json:"yaml"`
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
			configs[i].Yaml = yaml
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
