package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// updateAssetName is the release asset this platform installs.
func updateAssetName() string { return "phantom.exe" }

// applyUpdate downloads downloadURL and swaps it in for the currently
// running exe, then relaunches. Windows allows renaming a running exe's
// file (it's only deleting/overwriting it in place while mapped for
// execution that fails), so the swap is: download to phantom_new.exe next
// to the current exe, rename the running exe out of the way to
// phantom_old.exe, rename phantom_new.exe into the vacated name, start the
// new exe, then exit this process. A leftover phantom_old.exe (unlocked
// once this process exits) is removed by the next launch's cleanupAfterUpdate.
//
// The relaunched exe still carries the requireAdministrator manifest, so
// Windows shows a fresh UAC prompt for it - unavoidable given the app's
// elevation requirement, regardless of how the new process is started.
func applyUpdate(ctx context.Context, downloadURL, sumsURL string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(exePath)
	newPath := filepath.Join(dir, "phantom_new.exe")
	oldPath := filepath.Join(dir, "phantom_old.exe")

	if err := downloadVerified(ctx, downloadURL, sumsURL, updateAssetName(), newPath); err != nil {
		return err
	}

	os.Remove(oldPath) // clean up any previous leftover before reusing the name

	if err := os.Rename(exePath, oldPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("rename current exe out of the way: %w", err)
	}
	if err := os.Rename(newPath, exePath); err != nil {
		os.Rename(oldPath, exePath) // best-effort restore so the app isn't left missing
		return fmt.Errorf("rename new exe into place: %w", err)
	}

	// Free the single-instance mutex before starting the new process - both
	// this (mid-exit) process and the new one would briefly be alive
	// otherwise, and the new one would lose the single-instance check
	// against its own about-to-exit predecessor.
	releaseSingleInstanceLock()

	cmd := exec.Command(exePath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relaunch: %w", err)
	}

	log.Printf("update installed, relaunching as pid %d", cmd.Process.Pid)
	os.Exit(0)
	return nil // unreachable
}

// cleanupAfterUpdate removes a phantom_old.exe left behind by a previous
// self-update - it's locked while that old process was still exiting, but
// is free to delete by the time the newly-relaunched process starts back up.
func cleanupAfterUpdate() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(filepath.Dir(exePath), "phantom_old.exe"))
}
