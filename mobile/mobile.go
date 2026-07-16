//go:build !windows

// Package mobile is the shared entry point for mobile VPN clients (Android now,
// iOS later via the same gomobile-bound source). It owns nothing platform
// specific except the raw TUN file descriptor handed to it by the OS - the
// actual packet routing lives in internal/netstack, shared with the Windows
// client (windows/wintun.go), which feeds the same netstack.Tunnel from a
// Wintun device instead of a raw fd.
//
// The exported API only uses gomobile-safe types (string, int, error) so it
// can be bound with `gomobile bind` for both Android (.aar) and iOS
// (.xcframework).
//
// Excluded from GOOS=windows: gvisor's fdbased link endpoint (pkg/rawfile)
// is linux-only and was never meant to build there anyway (the Windows
// client feeds netstack from a Wintun device via channel.Endpoint instead -
// see windows/wintun.go) - but with no build constraint of its own, this
// package still got *type-checked* whenever a Windows-side tool loaded the
// whole module (`go list ./...`/`go build ./...`), tripping over that
// unrelated failure. That's harmless for a plain `go build ./windows/...`
// (which only resolves windows/'s own dependency graph), but Wails' binding
// generator loads the full module graph to analyze the bound App struct -
// and silently produced no bindings at all once that hit this error.
package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"phantom/internal/config"
	"phantom/internal/netstack"
	"phantom/internal/pingcheck"
	"phantom/internal/protocol"
	"phantom/internal/proxy"
	"phantom/internal/transport"
	"phantom/internal/tunnel"

	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
)

// Protector exempts a raw socket fd from the platform's VPN routing, e.g.
// Android's VpnService.protect(). Implemented on the Kotlin/Swift side and
// passed into Start.
type Protector interface {
	Protect(fd int) bool
}

// Tunnel is a running Phantom VPN tunnel. Obtain one via Start.
type Tunnel struct {
	pool   *transport.ConnPool
	cancel context.CancelFunc
	inner  *netstack.Tunnel
}

// Start parses configYAML (the raw contents of a client.yaml file - the
// mobile app just imports/pastes it as text, no separate YAML parser needed
// on the Kotlin/Swift side), connects to the Phantom server, and begins
// routing all IP traffic arriving on tunFD (an already-established OS TUN
// device, e.g. the fd from Android's VpnService.Builder.establish()) through
// the tunnel. mtu should match the MTU the TUN device was configured with
// (1500 if unsure). protector must be able to exempt a raw socket fd from
// the VPN's own routing (VpnService.protect on Android) - without it, this
// process's own connection to the Phantom server gets captured by the
// tunnel it is trying to establish and never completes.
func Start(configYAML string, tunFD int, mtu int, protector Protector) (*Tunnel, error) {
	if mtu <= 0 {
		mtu = 1500
	}

	cfg, err := config.ParseClientConfig([]byte(configYAML))
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	psk, err := cfg.GetPSK()
	if err != nil {
		return nil, fmt.Errorf("psk: %w", err)
	}
	serverPub, err := cfg.GetServerPublicKey()
	if err != nil {
		return nil, fmt.Errorf("server_public_key: %w", err)
	}

	tlsCfg := &transport.TLSClientConfig{
		Domain:      cfg.Domain,
		Fingerprint: cfg.Fingerprint,
		ServerAddr:  cfg.Server,
		PSK:         psk,
		ServerPub:   serverPub,
	}
	if protector != nil {
		tlsCfg.ProtectFD = protector.Protect
	}

	poolSize := cfg.PoolSize
	if poolSize <= 0 {
		poolSize = 4
	}

	ctx, cancel := context.WithCancel(context.Background())

	pool := transport.NewConnPool(poolSize, 12*1024, func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error) {
		return transport.Dial(ctx, tlsCfg)
	})

	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	mux, err := pool.Get(dialCtx)
	dialCancel()
	if err != nil {
		pool.Close()
		cancel()
		return nil, fmt.Errorf("connect: %w", err)
	}

	linkEP, err := fdbased.New(&fdbased.Options{
		FDs: []int{tunFD},
		MTU: uint32(mtu),
	})
	if err != nil {
		pool.Close()
		cancel()
		return nil, fmt.Errorf("link endpoint: %w", err)
	}

	session := tunnel.NewSessionFromMux(mux)
	inner, err := netstack.New(session, linkEP, mtu)
	if err != nil {
		session.Close()
		pool.Close()
		cancel()
		return nil, err
	}
	// Lets the tunnel recover on its own from the one connection to the
	// Phantom server dying (a brief Wi-Fi blip, a server-side hiccup) by
	// pulling a fresh one out of the pool - which, thanks to ConnPool's own
	// self-healing (see connpool.go's monitorConn), is often *already*
	// sitting there healthy by the time this is called. See
	// netstack.Tunnel.SetSessionRefresher for why this matters.
	inner.SetSessionRefresher(func() (*tunnel.Session, error) {
		refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer refreshCancel()
		freshMux, err := pool.Get(refreshCtx)
		if err != nil {
			return nil, err
		}
		return tunnel.NewSessionFromMux(freshMux), nil
	})

	return &Tunnel{pool: pool, cancel: cancel, inner: inner}, nil
}

// Stop tears down the tunnel: the netstack, the Phantom session/pool, and
// any in-flight splices.
func (t *Tunnel) Stop() {
	if t.cancel != nil {
		t.cancel()
	}
	if t.inner != nil {
		t.inner.Stop()
	}
	if t.pool != nil {
		t.pool.Close()
	}
}

// Stats returns a small JSON blob {"uptime_seconds":N,"bytes_up":N,"bytes_down":N}
// for the UI to poll. A plain string keeps this gomobile-safe.
func (t *Tunnel) Stats() string {
	if t.inner == nil {
		return "{}"
	}
	return t.inner.Stats()
}

// IsAlive reports whether the underlying Phantom session is still connected.
func (t *Tunnel) IsAlive() bool {
	return t.inner != nil && t.inner.IsAlive()
}

// Ping resolves configYAML's server address and performs one full disguised
// handshake (TCP connect + uTLS ClientHello + the WS-upgrade auth exchange -
// the same cost a real Start incurs), timing it, then closes the connection
// without building a tunnel. Meant for a UI to show "IP/latency" for a saved
// config without actually connecting - safe to call repeatedly/periodically.
// Returns a JSON blob {"ip":"1.2.3.4","latency_ms":42} (a plain string keeps
// this gomobile-safe, same pattern as Stats). The actual work lives in
// internal/pingcheck, shared with the Windows app.
func Ping(configYAML string) (string, error) {
	result, err := pingcheck.Ping(configYAML)
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

// ProxyHandle is a running independent local SOCKS5 proxy - see StartProxy.
type ProxyHandle struct {
	pool   *transport.ConnPool
	server *proxy.SOCKS5Server
	port   int
}

// StartProxy dials configYAML's server and starts a local SOCKS5 proxy on
// 127.0.0.1, entirely independent of Start's full-tunnel VPN. Point another
// app's own SOCKS5 proxy setting (e.g. Telegram's) at 127.0.0.1:<Port()> to
// route just that one app through Phantom, without needing the full-tunnel
// VPN active at all (and without conflicting with it if it happens to be
// active too - this dials its own separate pool of connections to the same
// server).
//
// requestedPort, if non-zero, is the exact port to bind - typically the one
// the UI's own editable port field shows (only editable while off) and/or
// remembered from a previous successful start, so whatever's pointed at it
// doesn't need reconfiguring every time. Failing to bind it is a real error,
// not silently replaced with a different port, so the caller can surface it
// and let the user pick another one. requestedPort == 0 means "any free
// port" (OS-assigned).
//
// protector (nilable) exempts this proxy's own connections from the
// full-tunnel VPN's routing, exactly like Start's protector param - without
// it, turning the full VPN on for *any* config (not just this proxy's own)
// would capture and break this proxy's connections, since they're not part
// of that tunnel's own dial.
func StartProxy(configYAML string, requestedPort int, protector Protector) (*ProxyHandle, error) {
	// Checked before dialing anything - no point spending a real TLS
	// handshake against the server just to then discover the requested port
	// was never available in the first place.
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", requestedPort))
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	cfg, err := config.ParseClientConfig([]byte(configYAML))
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("parse config: %w", err)
	}
	psk, err := cfg.GetPSK()
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("psk: %w", err)
	}
	serverPub, err := cfg.GetServerPublicKey()
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("server_public_key: %w", err)
	}

	tlsCfg := &transport.TLSClientConfig{
		Domain:      cfg.Domain,
		Fingerprint: cfg.Fingerprint,
		ServerAddr:  cfg.Server,
		PSK:         psk,
		ServerPub:   serverPub,
	}
	if protector != nil {
		tlsCfg.ProtectFD = protector.Protect
	}

	poolSize := cfg.PoolSize
	if poolSize <= 0 {
		poolSize = 4
	}

	pool := transport.NewConnPool(poolSize, 12*1024, func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error) {
		return transport.Dial(ctx, tlsCfg)
	})

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 15*time.Second)
	mux, err := pool.Get(dialCtx)
	dialCancel()
	if err != nil {
		listener.Close()
		pool.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}

	session := tunnel.NewSessionFromMux(mux)
	server := proxy.NewSOCKS5Server(listener.Addr().String(), session)
	// See internal/proxy/socks5.go's SetSessionRefresher doc: without this,
	// the one connection dying (the VPN briefly capturing it while toggled
	// on, a network blip, ...) would break the proxy permanently instead of
	// recovering once transport.ConnPool redials a healthy replacement.
	server.SetSessionRefresher(func() (*tunnel.Session, error) {
		refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer refreshCancel()
		freshMux, err := pool.Get(refreshCtx)
		if err != nil {
			return nil, err
		}
		return tunnel.NewSessionFromMux(freshMux), nil
	})

	go server.Serve(listener) // errors already logged inside socks5.go

	return &ProxyHandle{pool: pool, server: server, port: port}, nil
}

// Port returns the local port the proxy is listening on - point another
// app's SOCKS5 proxy setting at 127.0.0.1:<Port()>.
func (p *ProxyHandle) Port() int {
	return p.port
}

// Stop tears down the proxy's listener and its connection pool.
func (p *ProxyHandle) Stop() {
	p.server.Stop()
	p.pool.Close()
}
