// Package pingcheck previews a saved client.yaml's reachability/latency
// without building a tunnel - shared by the Android bridge (mobile.Ping) and
// the Windows app (windows/ping.go), since the logic has no platform-specific
// part (no TUN/gVisor involved, just a dial+handshake+timing).
package pingcheck

import (
	"context"
	"fmt"
	"net"
	"time"

	"phantom/internal/config"
	"phantom/internal/transport"
)

// Result is what a UI needs to show for a saved config's server: its
// resolved IP (for display, and as the input to a geo-IP lookup) and the
// round-trip cost of one real disguised handshake.
type Result struct {
	IP        string
	LatencyMs int64
}

// Ping resolves configYAML's server address and performs one full disguised
// handshake (TCP connect + uTLS ClientHello + the WS-upgrade auth exchange -
// the same cost a real connect incurs), timing it, then closes the
// connection without building a tunnel. Safe to call repeatedly/periodically.
func Ping(configYAML string) (Result, error) {
	cfg, err := config.ParseClientConfig([]byte(configYAML))
	if err != nil {
		return Result{}, fmt.Errorf("parse config: %w", err)
	}
	psk, err := cfg.GetPSK()
	if err != nil {
		return Result{}, fmt.Errorf("psk: %w", err)
	}
	serverPub, err := cfg.GetServerPublicKey()
	if err != nil {
		return Result{}, fmt.Errorf("server_public_key: %w", err)
	}

	host, _, err := net.SplitHostPort(cfg.Server)
	if err != nil {
		return Result{}, fmt.Errorf("invalid server address %q: %w", cfg.Server, err)
	}

	// "ip4" (rather than a dual-stack lookup) skips the AAAA query, which on
	// some networks stalls for several seconds before falling back to A.
	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	ips, err := net.DefaultResolver.LookupIP(resolveCtx, "ip4", host)
	resolveCancel()
	if err != nil || len(ips) == 0 {
		return Result{}, fmt.Errorf("resolve server address %q: %w", host, err)
	}
	ip := ips[0].String()

	tlsCfg := &transport.TLSClientConfig{
		Domain:      cfg.Domain,
		Fingerprint: cfg.Fingerprint,
		ServerAddr:  cfg.Server,
		PSK:         psk,
		ServerPub:   serverPub,
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer dialCancel()

	start := time.Now()
	conn, _, err := transport.Dial(dialCtx, tlsCfg)
	if err != nil {
		return Result{}, fmt.Errorf("handshake: %w", err)
	}
	latency := time.Since(start)
	conn.Close()

	return Result{IP: ip, LatencyMs: latency.Milliseconds()}, nil
}
