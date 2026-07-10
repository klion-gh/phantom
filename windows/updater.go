package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const githubReleasesAPI = "https://api.github.com/repos/klion-gh/phantom/releases/latest"

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// checkAndSelfUpdate runs once shortly after startup (called as its own
// goroutine from App.startup, so it never blocks the window from showing).
// If GitHub has a newer release with a phantom.exe asset, it downloads it,
// swaps it in for the running exe, and relaunches - see selfUpdate. Any
// failure along the way (offline, rate-limited, no matching asset) is
// logged and otherwise ignored; the app keeps running on its current
// version rather than treating "can't update" as fatal.
func checkAndSelfUpdate(ctx context.Context) {
	cleanupOldExe()

	tag, downloadURL, ok := checkForUpdate()
	if !ok {
		return
	}

	log.Printf("update available: %s (current %s) - downloading", tag, AppVersion)
	runtime.EventsEmit(ctx, "update:downloading", tag)

	if err := selfUpdate(downloadURL); err != nil {
		log.Printf("self-update to %s failed: %v", tag, err)
		runtime.EventsEmit(ctx, "update:failed", err.Error())
		return
	}
	// selfUpdate relaunches the new exe and calls os.Exit itself on success -
	// nothing runs after it returns nil in practice.
}

// checkForUpdate asks GitHub for the latest release and returns its tag and
// the phantom.exe asset's download URL, or ok=false if already current or
// the check couldn't be completed for any reason.
func checkForUpdate() (tag string, downloadURL string, ok bool) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, githubReleasesAPI, nil)
	if err != nil {
		return "", "", false
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("update check failed: %v", err)
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("update check: unexpected status %d", resp.StatusCode)
		return "", "", false
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		log.Printf("update check: decode error: %v", err)
		return "", "", false
	}

	if !isNewerVersion(release.TagName, AppVersion) {
		return "", "", false
	}

	for _, asset := range release.Assets {
		if asset.Name == "phantom.exe" {
			return release.TagName, asset.BrowserDownloadURL, true
		}
	}
	log.Printf("update check: release %s has no phantom.exe asset", release.TagName)
	return "", "", false
}

// isNewerVersion compares two "vX.Y.Z"/"X.Y.Z" version strings numerically
// component by component - a plain string comparison would wrongly treat
// "1.9.0" as newer than "1.10.0".
func isNewerVersion(latest, current string) bool {
	l, c := parseVersion(latest), parseVersion(current)
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseVersion(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 3)
	var out [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(strings.TrimSpace(parts[i]))
		out[i] = n
	}
	return out
}

// selfUpdate downloads downloadURL and swaps it in for the currently
// running exe, then relaunches. Windows allows renaming a running exe's
// file (it's only deleting/overwriting it in place while mapped for
// execution that fails), so the swap is: download to phantom_new.exe next
// to the current exe, rename the running exe out of the way to
// phantom_old.exe, rename phantom_new.exe into the vacated name, start the
// new exe, then exit this process. A leftover phantom_old.exe (unlocked
// once this process exits) is removed by the next launch's cleanupOldExe.
//
// The relaunched exe still carries the requireAdministrator manifest, so
// Windows shows a fresh UAC prompt for it - unavoidable given the app's
// elevation requirement, regardless of how the new process is started.
func selfUpdate(downloadURL string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(exePath)
	newPath := filepath.Join(dir, "phantom_new.exe")
	oldPath := filepath.Join(dir, "phantom_old.exe")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: unexpected status %d", resp.StatusCode)
	}

	out, err := os.Create(newPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(newPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	out.Close()

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

// cleanupOldExe removes a phantom_old.exe left behind by a previous
// self-update - it's locked while that old process was still exiting, but
// is free to delete by the time the newly-relaunched process starts back up.
func cleanupOldExe() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(filepath.Dir(exePath), "phantom_old.exe"))
}
