package main

import (
	"os"
	"path/filepath"
)

// logDir keeps the log next to the running executable rather than under a
// per-user config directory, so it's easy to find without hunting through
// AppData - in a logs/ folder now that it's hourly segment files rather than
// one phantom.log.
func logDir() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exePath), "logs"), nil
}
