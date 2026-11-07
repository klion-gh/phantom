//go:build !windows

package mobile

import (
	"encoding/json"
	"sync"

	"phantom/internal/config"
	"phantom/internal/pingcheck"
)

// The app's "use a relay" setting, held here so every path into the core -
// Start, Ping, the auto-selector's probes - goes through the same relay
// without each call site carrying it. The configs themselves stay as
// imported; the relay is added on the way in. See internal/relay.
var (
	relayMu     sync.Mutex
	relayAddr   string
	relayPrefix string
)

// SetRelay sets the relay every config connects through - and the label of
// the servers' relay-facing names through it ("msk", or "" for none) - or
// turns it off with addr "". The app calls it at startup and whenever the
// setting changes; a tunnel already up keeps its path until it is rebuilt.
func SetRelay(addr, prefix string) error {
	normalized := ""
	if addr != "" {
		var err error
		if normalized, err = config.NormalizeRelayAddr(addr); err != nil {
			return err
		}
	}
	pfx, err := config.NormalizeRelayPrefix(prefix)
	if err != nil {
		return err
	}
	relayMu.Lock()
	relayAddr, relayPrefix = normalized, pfx
	relayMu.Unlock()
	return nil
}

// NormalizeRelayPrefix checks what the user typed as a relay prefix and
// returns it as one lower-case DNS label ("" for none).
func NormalizeRelayPrefix(prefix string) (string, error) {
	return config.NormalizeRelayPrefix(prefix)
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
	addr, prefix := relayAddr, relayPrefix
	relayMu.Unlock()
	if addr == "" {
		return configYAML
	}
	out, err := config.WithRelay(configYAML, addr, prefix)
	if err != nil {
		return configYAML
	}
	return out
}

// PingRelay times a TCP connect to a relay, around the VPN: a JSON blob
// {"ip":"1.2.3.4","latency_ms":12}, the same shape as Ping's.
func PingRelay(addr string) (string, error) {
	result, err := pingcheck.PingRelay(addr, pingOptions())
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		IP        string `json:"ip"`
		LatencyMs int64  `json:"latency_ms"`
	}{IP: result.IP, LatencyMs: result.LatencyMs})
	if err != nil {
		return "", err
	}
	return string(data), nil
}
