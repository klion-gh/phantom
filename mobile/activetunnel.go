//go:build !windows

package mobile

import (
	"context"
	"errors"
	"sync"
	"time"

	"phantom/internal/config"
	"phantom/internal/routing"
)

// The tunnel up right now and the server it goes to, so the auto-selector can
// check the config carrying traffic through it instead of opening a fresh
// handshake to the same server every round - see routing.SetCurrentCheck.
var (
	activeMu     sync.Mutex
	activeTun    *Tunnel
	activeDomain string
)

func setActiveTunnel(t *Tunnel, configYAML string) {
	activeMu.Lock()
	activeTun, activeDomain = t, configDomain(configYAML)
	activeMu.Unlock()
}

func clearActiveTunnel(t *Tunnel) {
	activeMu.Lock()
	if activeTun == t {
		activeTun, activeDomain = nil, ""
	}
	activeMu.Unlock()
}

func configDomain(configYAML string) string {
	cfg, err := config.ParseClientConfig([]byte(configYAML))
	if err != nil {
		return ""
	}
	return cfg.Domain
}

// inTunnelTimeout bounds one check through the tunnel.
const inTunnelTimeout = 8 * time.Second

// checkCurrentInTunnel is the selector's CurrentCheckFunc: a request through
// the tunnel up to c's server, if that is the server the tunnel goes to.
func checkCurrentInTunnel(c routing.Candidate) (bool, error) {
	domain := configDomain(c.YAML)
	activeMu.Lock()
	t, d := activeTun, activeDomain
	activeMu.Unlock()
	if t == nil || t.inner == nil || domain == "" || d != domain {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), inTunnelTimeout)
	defer cancel()
	if _, err := t.inner.CheckInternet(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return false, nil
		}
		return true, err
	}
	return true, nil
}
