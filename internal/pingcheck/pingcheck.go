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

// Options controls how a ping reaches the network.
type Options struct {
	// ProtectFD, if set, is applied to every socket the ping opens - the DNS
	// lookup and the handshake alike - before it connects, to take it out of
	// the platform's VPN routing (VpnService.protect on Android, a bind to the
	// physical interface on Windows). Nil means "the tunnel isn't up, use the
	// default route", which is already direct.
	//
	// This exists because a config's ping is a statement about the path from
	// this device to that server. Measured from inside a running tunnel it
	// instead reports device -> current server -> that server: a dead config
	// could look reachable through a live one, and every latency would carry
	// the current server's round trip on top.
	ProtectFD func(fd int) bool
}

// Ping resolves configYAML's server address and performs one full disguised
// handshake (TCP connect + uTLS ClientHello + the WS-upgrade auth exchange -
// the same cost a real connect incurs), timing it, then closes the
// connection without building a tunnel. Safe to call repeatedly/periodically.
func Ping(configYAML string) (Result, error) {
	return PingWith(configYAML, Options{})
}

// PingWith is Ping with control over the network path - see Options.
func PingWith(configYAML string, opts Options) (Result, error) {
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

	tlsCfg := &transport.TLSClientConfig{
		Domain:      cfg.Domain,
		Fingerprint: cfg.Fingerprint,
		PSK:         psk,
		ServerPub:   serverPub,
		ProtectFD:   opts.ProtectFD,
	}
	resolver := resolverFor(opts.ProtectFD)

	// Try each configured endpoint in order and report the first that responds -
	// so the tile shows "up" (and that reachable endpoint's IP/latency) as long
	// as any endpoint is reachable, matching what the failover dialer does for
	// the real tunnel rather than only ever previewing the primary.
	var lastErr error
	for _, endpoint := range cfg.ServerList() {
		host, port, splitErr := net.SplitHostPort(endpoint)
		if splitErr != nil {
			lastErr = fmt.Errorf("invalid server address %q: %w", endpoint, splitErr)
			continue
		}

		// "ip4" (rather than a dual-stack lookup) skips the AAAA query, which on
		// some networks stalls for several seconds before falling back to A.
		resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		ips, resolveErr := resolver.LookupIP(resolveCtx, "ip4", host)
		resolveCancel()
		if resolveErr != nil || len(ips) == 0 {
			lastErr = fmt.Errorf("resolve server address %q: %w", host, resolveErr)
			continue
		}

		// Dial the address just resolved rather than the hostname: handing the
		// dialer a hostname has it do its own lookup through the system
		// resolver, which is the one path ProtectFD can't reach. It also means
		// the IP shown on the tile is the one that was actually timed. SNI stays
		// cfg.Domain, so the real certificate still validates.
		tlsCfg.ServerAddr = net.JoinHostPort(ips[0].String(), port)
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 8*time.Second)
		start := time.Now()
		conn, _, dialErr := transport.Dial(dialCtx, tlsCfg)
		dialCancel()
		if dialErr != nil {
			lastErr = fmt.Errorf("handshake: %w", dialErr)
			continue
		}
		latency := time.Since(start)
		conn.Close()
		return Result{IP: ips[0].String(), LatencyMs: latency.Milliseconds()}, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no endpoints configured")
	}
	return Result{}, lastErr
}

// resolverFor returns the resolver a ping should use: the system one when
// nothing needs protecting, otherwise one whose sockets go through protect to
// a public server (transport.NewProtectedResolver).
func resolverFor(protect func(fd int) bool) transport.IPLookuper {
	if protect == nil {
		return net.DefaultResolver
	}
	return transport.NewProtectedResolver(protect)
}
