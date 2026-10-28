package main

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"log"
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

// ensureWintunDLL writes the embedded wintun.dll next to the running executable,
// and verifies that whatever is already there is byte-for-byte the DLL that was
// embedded. Must run before the first tun.CreateTUN call.
//
// The verification is the point. This process runs elevated (the manifest is
// requireAdministrator) and the DLL is loaded from the executable's own directory
// via LOAD_LIBRARY_SEARCH_APPLICATION_DIR. Phantom ships as a single .exe that
// people put wherever is convenient - Downloads, the Desktop, a folder under the
// user profile - all of which are writable *without* administrator rights. Any
// process running as the user could therefore drop its own wintun.dll next to the
// exe, and the previous code, which returned as soon as os.Stat found a file
// there, would load it with full administrator privileges on the next launch.
// That is a local privilege escalation, and a cheap one.
//
// A mismatch is refused rather than silently overwritten-and-continued only if
// the overwrite fails: the alternative is loading a DLL this build did not ship.
func ensureWintunDLL() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	dllPath := filepath.Join(filepath.Dir(exePath), "wintun.dll")

	want := sha256.Sum256(wintunDLL)

	if existing, readErr := os.ReadFile(dllPath); readErr == nil {
		if sha256.Sum256(existing) == want {
			return nil // exactly what we ship - nothing to do
		}
		log.Printf("wintun.dll next to the executable does not match the embedded copy - replacing it")
		if err := os.Remove(dllPath); err != nil {
			return fmt.Errorf(
				"wintun.dll at %s is not the copy this build embeds and could not be replaced (%w) - "+
					"refusing to continue rather than load it", dllPath, err)
		}
	}

	if err := os.WriteFile(dllPath, wintunDLL, 0644); err != nil {
		return fmt.Errorf("write wintun.dll: %w", err)
	}

	// Re-read rather than trust the write: this is the file about to be loaded
	// into an elevated process.
	written, err := os.ReadFile(dllPath)
	if err != nil {
		return fmt.Errorf("verify wintun.dll after writing: %w", err)
	}
	if sha256.Sum256(written) != want {
		return fmt.Errorf("wintun.dll at %s does not match the embedded copy after writing", dllPath)
	}
	return nil
}
