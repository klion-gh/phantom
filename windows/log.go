package main

import (
	"log"
	"os"
	"path/filepath"
)

// logFilePath puts phantom.log next to the running executable rather than
// under a per-user config directory, so it's easy to find without hunting
// through AppData while testing.
func logFilePath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exePath), "phantom.log"), nil
}

// initLog redirects the standard `log` package (used both here and by every
// internal/* package - the multiplexer, direct.go, etc. all call log.Printf
// directly) into a file, mirroring the Android app's FileLog: a real crash or
// connection failure needs to be diagnosable without a terminal attached.
func initLog() {
	path, err := logFilePath()
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	log.SetOutput(f)
}

func readLog() string {
	path, err := logFilePath()
	if err != nil {
		return "(no log directory)"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "(no log yet)"
	}
	return string(data)
}
