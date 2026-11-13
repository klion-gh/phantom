// Package pingcheck previews a saved client.yaml's reachability/latency
// without building a tunnel - shared by the Android bridge (mobile.Ping) and
// the Windows app (desktop/app.go), since the logic has no platform-specific
// part (no TUN/gVisor involved, just a dial+handshake+timing).
package pingcheck

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"phantom/internal/config"
	"phantom/internal/netcheck"
	"phantom/internal/protocol"
	"phantom/internal/transport"
	"phantom/internal/tunnel"
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
	return probe(configYAML, opts, nil)
}

// Internet is PingWith that goes one step further: once the handshake is
// done it fetches a connectivity-check page through that server (see
// internal/netcheck). A server can answer the handshake and still pass no
// traffic - its own uplink down, a filter between it and the internet - and
// that's exactly what choosing a config to fall back to must not land on.
// LatencyMs covers the handshake and the fetch.
func Internet(configYAML string, opts Options) (Result, error) {
	return probe(configYAML, opts, func(conn net.Conn, crypto *protocol.SessionCrypto) error {
		mux := tunnel.NewMultiplexer(conn, crypto)
		defer mux.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, err := netcheck.Through(ctx, func(addr string) (io.ReadWriteCloser, error) {
			return mux.Open(addr)
		})
		return err
	})
}

// Direct reports whether this device reaches the internet at all around the
// VPN - see netcheck.Direct. opts.ProtectFD takes the sockets out of a
// running tunnel, as for a ping.
func Direct(opts Options) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d := &net.Dialer{Timeout: 5 * time.Second, Control: transport.ProtectControl(opts.ProtectFD)}
	return netcheck.Direct(ctx, d)
}

// probe dials configYAML's endpoints in order and, on the first handshake
// that succeeds, runs then on the connection (nil: just the handshake). The
// connection is closed afterwards.
func probe(configYAML string, opts Options, then func(net.Conn, *protocol.SessionCrypto) error) (Result, error) {
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
		RelayDomain: cfg.RelayDomain(),
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
		conn, crypto, dialErr := transport.Dial(dialCtx, tlsCfg)
		dialCancel()
		if dialErr != nil {
			lastErr = fmt.Errorf("handshake: %w", dialErr)
			continue
		}
		if then != nil {
			if err := then(conn, crypto); err != nil {
				conn.Close()
				lastErr = fmt.Errorf("through the server: %w", err)
				continue
			}
		}
		latency := time.Since(start)
		conn.Close()
		ip := ips[0].String()
		if cfg.Relay != "" {
			// What was timed is the relay path, but the tile is about the
			// server: its IP, and the country looked up from it.
			ip = serverIP(resolver, cfg.DirectServerList())
		}
		return Result{IP: ip, LatencyMs: latency.Milliseconds()}, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no endpoints configured")
	}
	return Result{}, lastErr
}

// serverIP resolves the first of a config's own endpoints without dialing it -
// through a relay the server is never contacted directly, so this is only
// where it is. "" when it doesn't resolve.
func serverIP(resolver transport.IPLookuper, endpoints []string) string {
	if len(endpoints) == 0 {
		return ""
	}
	host, _, err := net.SplitHostPort(endpoints[0])
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := resolver.LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0].String()
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

// PingRelay times a TCP connect to a relay (cmd/relay) - its IP and how long
// the connect took. A relay holds no keys, so unlike a server there is no
// handshake to make with it; reaching its port is what "up" means for one.
func PingRelay(addr string, opts Options) (Result, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return Result{}, fmt.Errorf("invalid relay address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		ips, err := resolverFor(opts.ProtectFD).LookupIP(resolveCtx, "ip4", host)
		resolveCancel()
		if err != nil || len(ips) == 0 {
			return Result{}, fmt.Errorf("resolve relay address %q: %w", host, err)
		}
		ip = ips[0]
	}
	d := net.Dialer{Timeout: 8 * time.Second, Control: transport.ProtectControl(opts.ProtectFD)}
	start := time.Now()
	conn, err := d.Dial("tcp", net.JoinHostPort(ip.String(), port))
	if err != nil {
		return Result{}, err
	}
	latency := time.Since(start)
	conn.Close()
	return Result{IP: ip.String(), LatencyMs: latency.Milliseconds()}, nil
}
