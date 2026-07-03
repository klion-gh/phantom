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
package mobile

import (
	"context"
	"fmt"
	"net"
	"time"

	"phantom/internal/config"
	"phantom/internal/netstack"
	"phantom/internal/protocol"
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
