package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
)

// How this copy of Phantom was installed, as recorded by the installer at
// installMethodFile: "deb", "rpm", "arch" or "tar" (scripts/linux/install.sh).
// An update goes in the same way, so the package manager's view of the system
// stays true. Anything unrecorded is treated as "tar", which works anywhere.
const installMethodFile = "/usr/lib/phantom/install-method"

func installMethod() string {
	data, err := os.ReadFile(installMethodFile)
	if err != nil {
		return "tar"
	}
	switch m := strings.TrimSpace(string(data)); m {
	case "deb", "rpm", "arch", "tar":
		return m
	}
	return "tar"
}

// updateAssetName is the release asset this install updates from - see the
// release workflow's linux-client job for where each is built.
func updateAssetName() string {
	base := "phantom-linux-" + goruntime.GOARCH
	switch installMethod() {
	case "deb":
		return base + ".deb"
	case "rpm":
		return base + ".rpm"
	case "arch":
		return base + ".pkg.tar.zst"
	}
	return base + ".tar.gz"
}

// applyUpdate downloads and verifies the update, installs it with the system's
// own tool through pkexec (the desktop's password prompt), then starts the new
// window and exits. The package's scripts restart phantomd, which drops the
// tunnel; the new window starts disconnected, as after any restart.
func applyUpdate(ctx context.Context, downloadURL, sumsURL string) error {
	name := updateAssetName()
	dir, err := os.MkdirTemp("", "phantom-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	pkg := filepath.Join(dir, name)
	if err := downloadVerified(ctx, downloadURL, sumsURL, name, pkg); err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch installMethod() {
	case "deb":
		cmd = exec.Command("pkexec", "dpkg", "-i", pkg)
	case "rpm":
		cmd = exec.Command("pkexec", "rpm", "-U", "--replacepkgs", pkg)
	case "arch":
		cmd = exec.Command("pkexec", "pacman", "-U", "--noconfirm", pkg)
	default:
		if err := extractTarGz(pkg, dir); err != nil {
			return fmt.Errorf("unpack update: %w", err)
		}
		// Through sh, not executed directly: /tmp is often mounted noexec.
		cmd = exec.Command("pkexec", "/bin/sh", filepath.Join(dir, "phantom", "install.sh"))
	}
	log.Printf("update: installing with %s", strings.Join(cmd.Args, " "))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("install failed: %v: %s", err, lastLines(string(out), 6))
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	// The new window must start after this one is gone: while it lives, the
	// single-instance lock would hand the launch straight back to it.
	relaunch := exec.Command("/bin/sh", "-c", `sleep 1; exec "$0"`, exe)
	relaunch.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := relaunch.Start(); err != nil {
		return fmt.Errorf("relaunch: %w", err)
	}
	log.Printf("update installed, relaunching")
	os.Exit(0)
	return nil // unreachable
}

// cleanupAfterUpdate: nothing is left behind on Linux - the download lives in
// a temporary directory applyUpdate removes itself.
func cleanupAfterUpdate() {}

// extractTarGz unpacks archive into dir, refusing any entry that would land
// outside it.
func extractTarGz(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	root := filepath.Clean(dir) + string(os.PathSeparator)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dir, hdr.Name)
		if !strings.HasPrefix(target, root) {
			return fmt.Errorf("archive entry %q escapes the target directory", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0755)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
