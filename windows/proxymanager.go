package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"phantom/internal/config"
	"phantom/internal/protocol"
	"phantom/internal/proxy"
	"phantom/internal/transport"
	"phantom/internal/tunnel"
)

// A per-config, independent local SOCKS5 proxy - deliberately separate from
// WinTunnel (the full-tunnel VPN): this dials its own small pool of
// connections to the same server and exposes a plain SOCKS5 listener on
// 127.0.0.1, so a single other app (e.g. Telegram, pointed at its own SOCKS5
// proxy setting) can go through Phantom without needing the full system-wide
// tunnel active - and without conflicting with it if it happens to be active
// too, for this config or any other. Mirrors what cmd/client already does,
// just managed in-process and toggleable instead of a standalone binary.
type runningProxy struct {
	pool   *transport.ConnPool
	server *proxy.SOCKS5Server
	port   int
}

var (
	proxyMu sync.Mutex
	proxies = map[string]*runningProxy{} // configID -> running proxy
)

// startConfigProxy starts (or, if already running, just reports) configID's
// independent SOCKS5 proxy on 127.0.0.1. If requestedPort is non-zero, that
// exact port is used - and failing to bind it is a real error, not silently
// papered over with a different port, since it either came from the user
// explicitly typing a port they want (see the frontend's port field, only
// editable while the proxy is off) or from the one this config used
// successfully last time (see SavedConfig.ProxyPort) and is now unexpectedly
// unavailable, both of which are worth surfacing rather than hiding.
// requestedPort == 0 means "any free port" (OS-assigned). Either way, the
// actual bound port is returned so the caller can remember it via
// setConfigProxyPort.
func startConfigProxy(configID string, configYAML string, requestedPort int) (int, error) {
	proxyMu.Lock()
	if existing, ok := proxies[configID]; ok {
		proxyMu.Unlock()
		return existing.port, nil
	}
	proxyMu.Unlock()

	// Checked before dialing anything - no point spending a real TLS
	// handshake against the server just to then discover the requested port
	// was never available in the first place.
	listener, err := listenAt(requestedPort)
	if err != nil {
		return 0, fmt.Errorf("listen: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	cfg, err := config.ParseClientConfig([]byte(configYAML))
	if err != nil {
		listener.Close()
		return 0, fmt.Errorf("parse config: %w", err)
	}
	psk, err := cfg.GetPSK()
	if err != nil {
		listener.Close()
		return 0, fmt.Errorf("psk: %w", err)
	}
	serverPub, err := cfg.GetServerPublicKey()
	if err != nil {
		listener.Close()
		return 0, fmt.Errorf("server_public_key: %w", err)
	}

	tlsCfg := &transport.TLSClientConfig{
		Domain:      cfg.Domain,
		Fingerprint: cfg.Fingerprint,
		ServerAddr:  cfg.Server,
		PSK:         psk,
		ServerPub:   serverPub,
	}

	poolSize := cfg.PoolSize
	if poolSize <= 0 {
		poolSize = 4
	}

	pool := transport.NewConnPool(poolSize, func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error) {
		return transport.Dial(ctx, tlsCfg)
	})

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 15*time.Second)
	mux, err := pool.Get(dialCtx)
	dialCancel()
	if err != nil {
		listener.Close()
		pool.Close()
		return 0, fmt.Errorf("connect: %w", err)
	}

	session := tunnel.NewSessionFromMux(mux)
	server := proxy.NewSOCKS5Server(listener.Addr().String(), session)
	// See internal/proxy/socks5.go's SetSessionRefresher doc: without this,
	// the one connection dying (a network blip, the server restarting, ...)
	// would break the proxy permanently instead of recovering once
	// transport.ConnPool redials a healthy replacement.
	server.SetSessionRefresher(func() (*tunnel.Session, error) {
		refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer refreshCancel()
		freshMux, err := pool.Get(refreshCtx)
		if err != nil {
			return nil, err
		}
		return tunnel.NewSessionFromMux(freshMux), nil
	})

	rp := &runningProxy{pool: pool, server: server, port: port}

	proxyMu.Lock()
	// Lost a race with a second concurrent start for the same config - keep
	// whichever one is already registered, discard this one.
	if existing, ok := proxies[configID]; ok {
		proxyMu.Unlock()
		server.Stop()
		session.Close()
		pool.Close()
		return existing.port, nil
	}
	proxies[configID] = rp
	proxyMu.Unlock()

	go func() {
		if err := server.Serve(listener); err != nil {
			log.Printf("[proxy %s] serve error: %v", configID, err)
		}
	}()

	log.Printf("[proxy] started for config %s on 127.0.0.1:%d", configID, port)
	return port, nil
}

// listenAt binds 127.0.0.1:port, or an OS-assigned free port if port is 0.
// Deliberately no fallback if the specific requested port is unavailable -
// see startConfigProxy.
func listenAt(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

func stopConfigProxy(configID string) {
	proxyMu.Lock()
	rp, ok := proxies[configID]
	if ok {
		delete(proxies, configID)
	}
	proxyMu.Unlock()
	if !ok {
		return
	}
	rp.server.Stop()
	rp.pool.Close()
	log.Printf("[proxy] stopped for config %s", configID)
}

// configProxyPort reports whether configID currently has a running proxy and,
// if so, which port it's bound to.
func configProxyPort(configID string) (int, bool) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	rp, ok := proxies[configID]
	if !ok {
		return 0, false
	}
	return rp.port, true
}

// anyConfigProxyRunning reports whether at least one independent proxy is
// currently running, for the tray's combined VPN+proxy status line.
func anyConfigProxyRunning() bool {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	return len(proxies) > 0
}

// stopAllConfigProxies tears down every running proxy - called on app shutdown.
func stopAllConfigProxies() {
	proxyMu.Lock()
	all := proxies
	proxies = map[string]*runningProxy{}
	proxyMu.Unlock()

	for _, rp := range all {
		rp.server.Stop()
		rp.pool.Close()
	}
}
