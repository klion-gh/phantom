package main

import (
	"os"
	"path/filepath"
)

// logDir is under the XDG state directory (~/.local/state/phantom/logs): the
// executable sits in /usr/bin, where a user process can't write. phantomd logs
// to the journal - see `journalctl -u phantomd`.
func logDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "phantom", "logs"), nil
}
