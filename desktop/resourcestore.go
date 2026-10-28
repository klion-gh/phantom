package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// PingResource is a user-visible "is this reachable" tile - separate from
// SavedConfig, purely diagnostic (no server/psk/etc, just a name + URL).
// Reachability itself is checked by the frontend directly via fetch(),
// which goes through the OS's real network stack and is therefore subject
// to whatever the system routing table currently says - a resource that's
// normally blocked and starts responding once the Phantom tunnel connects
// (or one that stays unreachable even connected) is the whole point of the
// feature, so no Go-side ping method is needed here, just storage.
type PingResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

const resourcesFileName = "resources.json"

var defaultResources = []PingResource{
	{Name: "YouTube", URL: "https://www.youtube.com"},
	{Name: "Discord", URL: "https://discord.com"},
	{Name: "ChatGPT", URL: "https://chatgpt.com"},
	{Name: "Claude", URL: "https://claude.ai"},
}

func loadResources() ([]PingResource, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, resourcesFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// First run: seed the built-in defaults (with real IDs) so the panel
		// isn't empty and the user can delete/customize from there on.
		seeded := make([]PingResource, len(defaultResources))
		for i, r := range defaultResources {
			seeded[i] = PingResource{ID: uuid.NewString(), Name: r.Name, URL: r.URL}
		}
		if err := saveResources(dir, seeded); err != nil {
			return nil, err
		}
		return seeded, nil
	}
	var resources []PingResource
	if err := json.Unmarshal(data, &resources); err != nil {
		return nil, err
	}
	return resources, nil
}

func saveResources(dir string, resources []PingResource) error {
	data, err := json.Marshal(resources)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, resourcesFileName), data, 0600)
}

func addResource(name, url string) (PingResource, error) {
	dir, err := configDir()
	if err != nil {
		return PingResource{}, err
	}
	resources, err := loadResources()
	if err != nil {
		return PingResource{}, err
	}
	r := PingResource{ID: uuid.NewString(), Name: name, URL: url}
	if err := saveResources(dir, append(resources, r)); err != nil {
		return PingResource{}, err
	}
	return r, nil
}

func deleteResource(id string) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	resources, err := loadResources()
	if err != nil {
		return err
	}
	filtered := resources[:0]
	for _, r := range resources {
		if r.ID != id {
			filtered = append(filtered, r)
		}
	}
	return saveResources(dir, filtered)
}
