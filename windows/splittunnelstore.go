package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// ExcludedApp is one entry in the split-tunneling list: an executable whose
// own connections should bypass the Phantom tunnel and go out directly, even
// while connected. Matched by exe basename (see isExcludedProcess in
// splittunnel.go) rather than full path, since that's what the user picks via
// a file-open dialog and what's most intuitive ("exclude chrome.exe"
// regardless of which profile/shortcut launched it).
type ExcludedApp struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	ExePath string `json:"exePath"`
}

const excludedAppsFileName = "split_tunnel.json"

func loadExcludedApps() ([]ExcludedApp, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, excludedAppsFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return []ExcludedApp{}, nil
		}
		return nil, err
	}
	var apps []ExcludedApp
	if err := json.Unmarshal(data, &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

func saveExcludedApps(dir string, apps []ExcludedApp) error {
	data, err := json.Marshal(apps)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, excludedAppsFileName), data, 0600)
}

func addExcludedApp(name, exePath string) (ExcludedApp, error) {
	dir, err := configDir()
	if err != nil {
		return ExcludedApp{}, err
	}
	apps, err := loadExcludedApps()
	if err != nil {
		return ExcludedApp{}, err
	}
	a := ExcludedApp{ID: uuid.NewString(), Name: name, ExePath: exePath}
	if err := saveExcludedApps(dir, append(apps, a)); err != nil {
		return ExcludedApp{}, err
	}
	return a, nil
}

func deleteExcludedApp(id string) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	apps, err := loadExcludedApps()
	if err != nil {
		return err
	}
	filtered := apps[:0]
	for _, a := range apps {
		if a.ID != id {
			filtered = append(filtered, a)
		}
	}
	return saveExcludedApps(dir, filtered)
}
