package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Small UI preferences, one file per key under the app's config directory. Not
// worth a config format: each is a single short token, and keeping them as plain
// files means a corrupt or hand-edited one degrades to its default rather than
// taking the others with it.
const (
	languageFileName          = "language"
	paletteFileName           = "palette"
	backgroundFileName        = "background"
	showProxySettingsFileName = "show_proxy_settings"
)

// loadSetting returns the persisted value of name, falling back to def when the
// file is missing, unreadable, or holds something not in allowed. Validating on
// read as well as write means a value that stopped being supported (or was
// edited by hand) can't reach the UI.
func loadSetting(name, def string, allowed ...string) string {
	dir, err := configDir()
	if err != nil {
		return def
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return def
	}
	value := strings.TrimSpace(string(data))
	for _, ok := range allowed {
		if value == ok {
			return value
		}
	}
	return def
}

// saveSetting persists value under name, storing def instead if value isn't one
// of allowed - so whatever ends up on disk is always something loadSetting will
// accept back.
func saveSetting(name, value, def string, allowed ...string) {
	valid := false
	for _, ok := range allowed {
		if value == ok {
			valid = true
			break
		}
	}
	if !valid {
		value = def
	}
	dir, err := configDir()
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name), []byte(value), 0600)
}

// loadLanguage returns the persisted UI language, defaulting to "ru" - the app's
// original language - so existing users see no change until they pick English.
func loadLanguage() string { return loadSetting(languageFileName, "ru", "ru", "en") }

func saveLanguage(lang string) { saveSetting(languageFileName, lang, "ru", "ru", "en") }

// The six interchangeable palettes and eight animated-backdrop variants - see
// style.css's [data-palette=...] blocks and background.js's BACKGROUNDS list,
// which this must stay in sync with. "midnight"/"orbs" are the originals and
// stay the defaults.
var (
	palettes    = []string{"midnight", "emerald", "sunset", "ocean", "graphite", "sakura"}
	backgrounds = []string{"orbs", "aurora", "stars", "mesh", "meteors", "waves", "embers", "plain"}
)

func loadPalette() string { return loadSetting(paletteFileName, "midnight", palettes...) }

func savePalette(palette string) { saveSetting(paletteFileName, palette, "midnight", palettes...) }

func loadBackground() string { return loadSetting(backgroundFileName, "orbs", backgrounds...) }

func saveBackground(background string) {
	saveSetting(backgroundFileName, background, "orbs", backgrounds...)
}

// Whether the per-config proxy button and port field are shown at all -
// stored as "1"/"0" like the other single-token settings; on ("1") by default
// since that was the only behaviour before this setting existed.
func loadShowProxySettings() bool {
	return loadSetting(showProxySettingsFileName, "1", "1", "0") == "1"
}

func saveShowProxySettings(show bool) {
	value := "0"
	if show {
		value = "1"
	}
	saveSetting(showProxySettingsFileName, value, "1", "1", "0")
}
