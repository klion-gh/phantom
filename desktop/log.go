package main

import (
	"log"
	"os"
	"path/filepath"

	"phantom/internal/logfile"
)

// viewerTailBytes is how much of the log the in-app viewer shows: the newest
// part, which is what's being looked at, without handing a WebView textarea a
// whole day of diagnostics to lay out. "Скопировать" copies the full log.
const viewerTailBytes = 512 << 10

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

// initLog redirects the standard `log` package (used here and by every
// internal/* package) into hourly segment files that keep only the last day
// (see internal/logfile), mirroring the Android app's FileLog.
//
// Before this it was a single phantom.log appended to forever - with the
// diagnostics added since, that grew without bound. That file is deleted
// here on first start of a build that no longer writes it.
func initLog() {
	dir, err := logDir()
	if err != nil {
		return
	}
	os.Remove(filepath.Join(filepath.Dir(dir), "phantom.log"))
	w, err := logfile.Open(dir)
	if err != nil {
		return
	}
	// Milliseconds: stall, DNS-timeout and reconnect lines are only useful
	// if they can be lined up against each other.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(w)
}

// readLog is what the in-app viewer shows - see viewerTailBytes.
func readLog() string {
	dir, err := logDir()
	if err != nil {
		return "(no log directory)"
	}
	s, err := logfile.ReadTail(dir, viewerTailBytes)
	if err != nil {
		return "(no log yet)"
	}
	return s
}

// readFullLog is every retained line - what "Скопировать" puts on the
// clipboard, so a bug report carries the whole last day, not just the tail.
func readFullLog() string {
	dir, err := logDir()
	if err != nil {
		return "(no log directory)"
	}
	s, err := logfile.ReadAll(dir)
	if err != nil {
		return "(no log yet)"
	}
	return s
}
