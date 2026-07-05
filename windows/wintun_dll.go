package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// wintun.dll (from https://www.wintun.net/, the driver WireGuard-for-Windows
// itself uses, pre-signed by the WireGuard project) can't be loaded from
// embedded bytes directly - golang.zx2c4.com/wintun's LoadLibraryEx call
// searches the running exe's own directory (LOAD_LIBRARY_SEARCH_APPLICATION_DIR)
// and System32, not arbitrary memory. Embedding it and writing it out next to
// the exe on first run keeps the distributed artifact to a single .exe.
//
//go:embed wintun-amd64.dll
var wintunDLL []byte

// ensureWintunDLL writes the embedded wintun.dll next to the running
// executable if it isn't already there. Must run before the first
// tun.CreateTUN call.
func ensureWintunDLL() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	dllPath := filepath.Join(filepath.Dir(exePath), "wintun.dll")

	if _, err := os.Stat(dllPath); err == nil {
		return nil // already present (e.g. a previous run extracted it)
	}

	return os.WriteFile(dllPath, wintunDLL, 0644)
}
