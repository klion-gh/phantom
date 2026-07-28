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
	languageFileName = "language"
	themeFileName    = "theme"
	accentFileName   = "accent"
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

// loadTheme returns "dark" or "light". Dark is the default and the original look:
// adding a theme switch shouldn't change how the app appears to anyone who never
// touches it.
func loadTheme() string { return loadSetting(themeFileName, "dark", "dark", "light") }

func saveTheme(theme string) { saveSetting(themeFileName, theme, "dark", "dark", "light") }

// loadAccent returns which gradient the "this is on" outlines use. "pink" is the
// original, for the same reason.
func loadAccent() string {
	return loadSetting(accentFileName, "pink", "pink", "green", "blue", "red")
}

func saveAccent(accent string) {
	saveSetting(accentFileName, accent, "pink", "pink", "green", "blue", "red")
}
