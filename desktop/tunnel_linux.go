package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"phantom/internal/daemonapi"
)

// The window's side of the Linux tunnel, which lives in phantomd - see
// internal/daemonapi for why. The service keeps the tunnel and its routing
// engine; this file only asks it to.

var (
	daemonMu     sync.Mutex
	daemonClient *daemonapi.Client
)

// withDaemon runs fn against phantomd, connecting first if needed. With start
// set, a service that isn't running is started (see startDaemon) - only
// Connect does that; everything else just reports ErrNotRunning, which for a
// status poll or a routing edit simply means "no tunnel".
//
// A connection broken under it (the service was restarted, e.g. by a package
// upgrade) is redialled once, so one stale handle never surfaces as an error.
func withDaemon(start bool, fn func(*daemonapi.Client) error) error {
	for attempt := 0; attempt < 2; attempt++ {
		c, err := daemonConn(start)
		if err != nil {
			return err
		}
		err = fn(c)
		if err != nil && isBrokenConn(err) {
			dropDaemonConn(c)
			continue
		}
		return err
	}
	return errors.New("phantomd connection keeps breaking")
}

func isBrokenConn(err error) bool {
	return errors.Is(err, rpc.ErrShutdown) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func daemonConn(start bool) (*daemonapi.Client, error) {
	daemonMu.Lock()
	defer daemonMu.Unlock()
	if daemonClient != nil {
		return daemonClient, nil
	}

	c, err := daemonapi.Dial()
	if err != nil && start && errors.Is(err, daemonapi.ErrNotRunning) {
		if startErr := startDaemon(); startErr != nil {
			return nil, startErr
		}
		c, err = waitForDaemon(daemonStartTimeout)
	}
	if err != nil {
		return nil, err
	}

	st, err := c.Status()
	if err != nil {
		c.Close()
		return nil, err
	}
	if st.Version != daemonapi.Version {
		c.Close()
		return nil, fmt.Errorf("phantomd speaks protocol v%d but this window expects v%d - "+
			"reinstall Phantom so both come from the same release", st.Version, daemonapi.Version)
	}
	daemonClient = c
	return c, nil
}

func dropDaemonConn(c *daemonapi.Client) {
	daemonMu.Lock()
	defer daemonMu.Unlock()
	if daemonClient == c {
		daemonClient.Close()
		daemonClient = nil
	}
}

// Long enough to type a password into the polkit prompt startDaemon may
// bring up.
const daemonStartTimeout = 90 * time.Second

// startDaemon gets phantomd running: through systemd where the package
// installed its unit (normally it's already running - enabled at install -
// and this only matters if someone stopped it), otherwise by running the
// binary through pkexec. Either may show the desktop's polkit password
// prompt; that's the one moment this app needs administrator rights.
func startDaemon() error {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		out, err := exec.Command("systemctl", "start", "phantomd.service").CombinedOutput()
		if err == nil {
			log.Printf("started phantomd.service")
			return nil
		}
		log.Printf("systemctl start phantomd.service: %v: %s - trying pkexec", err, out)
	}

	bin, err := daemonBinary()
	if err != nil {
		return err
	}
	cmd := exec.Command("pkexec", bin)
	// Its own session: the service outlives this window, and must not get the
	// hangup a terminal-launched window's exit would send its process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start phantomd through pkexec: %w", err)
	}
	log.Printf("started %s through pkexec (pid %d)", bin, cmd.Process.Pid)
	go cmd.Wait() // reap it whenever it does exit
	return nil
}

// daemonBinary finds phantomd: next to this executable (a portable unpack),
// or where the packages and install.sh put it.
func daemonBinary() (string, error) {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "phantomd"))
	}
	candidates = append(candidates, "/usr/lib/phantom/phantomd", "/usr/libexec/phantom/phantomd", "/opt/phantom/phantomd")
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("phantomd is not installed - reinstall Phantom")
}

func waitForDaemon(timeout time.Duration) (*daemonapi.Client, error) {
	deadline := time.Now().Add(timeout)
	for {
		c, err := daemonapi.Dial()
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("phantomd did not start within %s: %w", timeout, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// --- activeTunnel ------------------------------------------------------------

// daemonTunnel stands for "the tunnel phantomd is running". It holds no state
// of its own: there is one tunnel per machine, and the service is the
// authority on it.
type daemonTunnel struct{}

func (daemonTunnel) Stop() {
	if err := withDaemon(false, func(c *daemonapi.Client) error { return c.Disconnect() }); err != nil {
		log.Printf("disconnect: %v", err)
	}
}

func (daemonTunnel) status() daemonapi.StatusReply {
	var st daemonapi.StatusReply
	withDaemon(false, func(c *daemonapi.Client) error {
		var err error
		st, err = c.Status()
		return err
	})
	return st
}

func (t daemonTunnel) IsAlive() bool {
	st := t.status()
	return st.Connected && st.Alive
}

func (t daemonTunnel) Stats() string {
	if st := t.status(); st.Stats != "" {
		return st.Stats
	}
	return "{}"
}

func (daemonTunnel) ResetMisroutedFlows() {
	withDaemon(false, func(c *daemonapi.Client) error { return c.ResetMisroutedFlows() })
}

// startTunnel asks phantomd to connect, starting the service if it isn't
// running. onNetworkChanged is unused here: the service moves the tunnel to a
// new network itself, without a reconnect - see cmd/phantomd's watchNetwork.
func startTunnel(configID, configYAML string, onNetworkChanged func()) (activeTunnel, error) {
	err := withDaemon(true, func(c *daemonapi.Client) error {
		return c.Connect(daemonapi.ConnectArgs{
			ConfigID: configID,
			YAML:     configYAML,
			Routing:  currentRouting(),
		})
	})
	if err != nil {
		return nil, err
	}
	return daemonTunnel{}, nil
}

// adoptRunningTunnel picks up a tunnel phantomd kept running while no window
// was open. It never starts the service: no service, no tunnel.
func adoptRunningTunnel() (activeTunnel, string) {
	st := daemonTunnel{}.status()
	if !st.Connected {
		return nil, ""
	}
	return daemonTunnel{}, st.ConfigID
}

func currentRouting() daemonapi.RoutingArgs {
	return daemonapi.RoutingArgs{
		Smart: loadSmartEnabled() && !loadAutoEnabled(),
		Sites: loadSmartSites(),
	}
}
