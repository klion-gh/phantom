//go:build !windows

package mobile

import (
	"sync"

	"phantom/internal/config"
)

// The app's "use a relay" setting, held here so every path into the core -
// Start, Ping, the auto-selector's probes - goes through the same relay
// without each call site carrying it. The configs themselves stay as
// imported; the relay is added on the way in. See internal/relay.
var (
	relayMu   sync.Mutex
	relayAddr string
)

// SetRelay sets the relay every config connects through, or turns it off
// with "". The app calls it at startup and whenever the setting changes;
// a tunnel already up keeps its path until it is rebuilt.
func SetRelay(addr string) error {
	normalized := ""
	if addr != "" {
		var err error
		if normalized, err = config.NormalizeRelayAddr(addr); err != nil {
			return err
		}
	}
	relayMu.Lock()
	relayAddr = normalized
	relayMu.Unlock()
	return nil
}

// NormalizeRelayAddr checks what the user typed as a relay address and
// returns it as address:port (port 443 when none was given).
func NormalizeRelayAddr(addr string) (string, error) {
	return config.NormalizeRelayAddr(addr)
}

// withRelay is configYAML as the core should use it: with the relay added
// while one is set. A config it can't be added to goes through unchanged -
// connecting directly beats not connecting.
func withRelay(configYAML string) string {
	relayMu.Lock()
	addr := relayAddr
	relayMu.Unlock()
	if addr == "" {
		return configYAML
	}
	out, err := config.WithRelay(configYAML, addr)
	if err != nil {
		return configYAML
	}
	return out
}
