package main

import (
	"os"
	"path/filepath"
	"strings"
)

const languageFileName = "language"

// loadLanguage returns the persisted UI language ("ru" or "en"), defaulting to
// "ru" - the app's original language - when nothing has been saved yet, so
// existing users see no change until they pick English explicitly.
func loadLanguage() string {
	dir, err := configDir()
	if err != nil {
		return "ru"
	}
	data, err := os.ReadFile(filepath.Join(dir, languageFileName))
	if err != nil {
		return "ru"
	}
	if strings.TrimSpace(string(data)) == "en" {
		return "en"
	}
	return "ru"
}

// saveLanguage persists the UI language. Anything other than "en" is stored as
// "ru" so the on-disk value is always one of the two supported languages.
func saveLanguage(lang string) {
	if lang != "en" {
		lang = "ru"
	}
	dir, err := configDir()
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, languageFileName), []byte(lang), 0600)
}
