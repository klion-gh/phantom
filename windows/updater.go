package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
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

// Set by checkAndSelfUpdate once it finds something newer, read by
// applyPendingUpdate when the user actually clicks the update button - the
// app no longer installs an update on its own the moment it's found.
var (
	pendingUpdateMu   sync.Mutex
	pendingUpdateTag  string
	pendingUpdateURL  string
	pendingUpdateSums string // SHA256SUMS asset for the same release
)

// sumsAssetName is the checksum manifest the release workflow publishes next to
// the binaries. The updater refuses to install an exe it can't check against it.
const sumsAssetName = "SHA256SUMS"

// checkAndSelfUpdate runs once shortly after startup (called as its own
// goroutine from App.startup, so it never blocks the window from showing).
// If GitHub has a newer release with a phantom.exe asset, it just remembers
// it and emits "update:available" so the frontend can show the update
// button - actually downloading/installing it is applyPendingUpdate's job,
// triggered only by the user clicking that button. Any failure along the
// way (offline, rate-limited, no matching asset) is logged and otherwise
// ignored; the app keeps running on its current version rather than
// treating "can't check" as fatal.
func checkAndSelfUpdate(ctx context.Context) {
	cleanupOldExe()

	tag, downloadURL, sumsURL, ok := checkForUpdate()
	if !ok {
		return
	}

	log.Printf("update available: %s (current %s)", tag, AppVersion)
	pendingUpdateMu.Lock()
	pendingUpdateTag = tag
	pendingUpdateURL = downloadURL
	pendingUpdateSums = sumsURL
	pendingUpdateMu.Unlock()
	runtime.EventsEmit(ctx, "update:available", tag)
}

// applyPendingUpdate performs the actual download+swap+relaunch for
// whatever update checkAndSelfUpdate most recently found. Returns an error
// message on failure, or "" if there was nothing pending to apply - on
// success this doesn't return at all, since selfUpdate relaunches the new
// exe and calls os.Exit itself.
func applyPendingUpdate(ctx context.Context) string {
	pendingUpdateMu.Lock()
	tag, downloadURL, sumsURL := pendingUpdateTag, pendingUpdateURL, pendingUpdateSums
	pendingUpdateMu.Unlock()

	if downloadURL == "" {
		return "no update available"
	}

	log.Printf("applying update %s (current %s)", tag, AppVersion)
	runtime.EventsEmit(ctx, "update:downloading", tag)

	if err := selfUpdate(downloadURL, sumsURL); err != nil {
		log.Printf("self-update to %s failed: %v", tag, err)
		runtime.EventsEmit(ctx, "update:failed", err.Error())
		return err.Error()
	}
	return "" // unreachable - selfUpdate exits the process on success
}

// checkForUpdate asks GitHub for the latest release and returns its tag, the
// phantom.exe asset's download URL and the SHA256SUMS asset's URL, or ok=false if
// already current or the check couldn't be completed for any reason.
func checkForUpdate() (tag string, downloadURL string, sumsURL string, ok bool) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, githubReleasesAPI, nil)
	if err != nil {
		return "", "", "", false
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("update check failed: %v", err)
		return "", "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("update check: unexpected status %d", resp.StatusCode)
		return "", "", "", false
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		log.Printf("update check: decode error: %v", err)
		return "", "", "", false
	}

	if !isNewerVersion(release.TagName, AppVersion) {
		return "", "", "", false
	}

	var exeURL string
	for _, asset := range release.Assets {
		switch asset.Name {
		case "phantom.exe":
			exeURL = asset.BrowserDownloadURL
		case sumsAssetName:
			sumsURL = asset.BrowserDownloadURL
		}
	}
	if exeURL == "" {
		log.Printf("update check: release %s has no phantom.exe asset", release.TagName)
		return "", "", "", false
	}
	if sumsURL == "" {
		// Refused rather than installed unchecked. Every release the workflow
		// builds publishes SHA256SUMS, so a release without one is not a release
		// this updater should be swapping an elevated executable for.
		log.Printf("update check: release %s has no %s - refusing to update unverified",
			release.TagName, sumsAssetName)
		return "", "", "", false
	}
	return release.TagName, exeURL, sumsURL, true
}

// fetchExpectedSum downloads the release's SHA256SUMS and returns the digest
// listed for name.
//
// This binds the downloaded exe to what the release actually published. Worth
// being clear about what it is and isn't: the manifest travels over the same
// channel as the binary, so it defends against a truncated or corrupted transfer
// and against being served the wrong asset - not against a release that is itself
// malicious, and not against an adversary who can rewrite both. Real authenticity
// for a Windows executable means Authenticode signing, which this project doesn't
// have; until it does, this is the floor rather than the ceiling.
func fetchExpectedSum(sumsURL, name string) (string, error) {
	if sumsURL == "" {
		return "", fmt.Errorf("release publishes no %s - refusing to install an unverified executable", sumsAssetName)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(sumsURL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", sumsAssetName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: unexpected status %d", sumsAssetName, resp.StatusCode)
	}

	// Cap the read: this is parsed before anything is verified.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", sumsAssetName, err)
	}

	sum, err := sumFor(string(body), name)
	if err != nil {
		return "", err
	}
	return sum, nil
}

// sumFor parses sha256sum(1) output and returns the digest recorded for name.
// The leading '*' some implementations write for binary mode is tolerated.
func sumFor(manifest, name string) (string, error) {
	for _, line := range strings.Split(manifest, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			sum := strings.ToLower(fields[0])
			if len(sum) != sha256.Size*2 {
				return "", fmt.Errorf("%s lists a malformed digest for %s", sumsAssetName, name)
			}
			return sum, nil
		}
	}
	return "", fmt.Errorf("%s does not list %s", sumsAssetName, name)
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
func selfUpdate(downloadURL, sumsURL string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(exePath)
	newPath := filepath.Join(dir, "phantom_new.exe")
	oldPath := filepath.Join(dir, "phantom_old.exe")

	// Fetch the expected digest before downloading, so a release whose manifest
	// can't be read costs nothing and changes nothing on disk.
	wantSum, err := fetchExpectedSum(sumsURL, "phantom.exe")
	if err != nil {
		return err
	}

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
	// Hash while writing rather than re-reading afterwards: what gets hashed is
	// then exactly the bytes that were written.
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, hasher), resp.Body); err != nil {
		out.Close()
		os.Remove(newPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	out.Close()

	if got := hex.EncodeToString(hasher.Sum(nil)); got != wantSum {
		os.Remove(newPath)
		return fmt.Errorf("downloaded phantom.exe does not match the release checksum "+
			"(expected %s, got %s) - not installing it", wantSum, got)
	}
	log.Printf("update: checksum verified")

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
