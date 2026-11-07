package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	utls "github.com/refraction-networking/utls"

	"phantom/internal/handshake"
	"phantom/internal/logx"
	"phantom/internal/protocol"
)

type TLSClientConfig struct {
	Domain      string // real domain the server has a CA-signed cert for - used as SNI and Host header
	Fingerprint string
	ServerAddr  string   // single endpoint; used by Dial and as the fallback for NewFailoverDialer when ServerAddrs is empty
	ServerAddrs []string // optional multi-endpoint failover list (see NewFailoverDialer); all share Domain/PSK/ServerPub
	PSK         []byte
	ServerPub   []byte // server's static X25519 public key

	// RelayDomain, if set, is the name presented instead of Domain - the
	// server's relay-facing name (see config.ClientConfig.RelayPrefix). A
	// server without a certificate for it is retried with Domain.
	RelayDomain string

	// ProtectFD, if set, is called with the raw socket fd right after it's
	// created and before connect(). On Android, once a VpnService routes
	// 0.0.0.0/0 through its TUN, the app's own outbound socket to the real
	// server gets captured by that same route unless the socket is
	// explicitly "protected" to bypass the VPN. Unused on desktop clients.
	ProtectFD func(fd int) bool

	// Resolver, if set, resolves a server given by hostname - see resolve.go
	// for why a platform whose own VPN captures the system resolver wants a
	// protected one here. Nil uses the system resolver. Either way a failed
	// lookup falls back to the last address that host was dialed on.
	Resolver IPLookuper

	// WrapConn, if set, wraps the raw TCP connection before TLS runs over
	// it. Only tools/flowlab uses it: it records the size and timing of every
	// read and write - what a DPI box on the path sees of the tunnel. Nil in
	// every client.
	WrapConn func(net.Conn) net.Conn

	// rootCAs overrides the system trust store. Tests only (unexported): it
	// lets them run the real Dial path - fingerprint, handshake pacing, TLS
	// 1.3, the disguised handshake - against a throwaway certificate.
	rootCAs *x509.CertPool
}

const defaultTimeout = 10 * time.Second

// Dial connects to cfg.ServerAddr (a single endpoint). Callers with a failover
// list use NewFailoverDialer instead; this stays the single-endpoint primitive
// (pingcheck uses it directly to time one specific address).
func Dial(ctx context.Context, cfg *TLSClientConfig) (net.Conn, *protocol.SessionCrypto, error) {
	return dialAddr(ctx, cfg, cfg.ServerAddr)
}

// NewFailoverDialer returns a pool dial function that tries cfg's endpoints
// (cfg.ServerAddrs, or [cfg.ServerAddr] if that's empty) until one connects,
// remembering the last one that worked and trying it first next time - so a
// dead primary IP/port costs a single failed-connect timeout once, not on every
// pooled redial afterward. This is what makes blocking one of the operator's
// IPs/ports no longer take the whole tunnel down (see config.ServerList).
func NewFailoverDialer(cfg *TLSClientConfig) func(context.Context) (net.Conn, *protocol.SessionCrypto, error) {
	addrs := cfg.ServerAddrs
	if len(addrs) == 0 {
		addrs = []string{cfg.ServerAddr}
	}
	return newFailoverDialer(addrs, func(ctx context.Context, addr string) (net.Conn, *protocol.SessionCrypto, error) {
		return dialAddr(ctx, cfg, addr)
	})
}

// dialOneFn dials a single address - the real one is dialAddr bound to a cfg;
// tests substitute a fake to exercise the failover ordering deterministically.
type dialOneFn func(ctx context.Context, addr string) (net.Conn, *protocol.SessionCrypto, error)

// newFailoverDialer is the ordering/last-good core of NewFailoverDialer, split
// out so it can be tested without a real server. It tries addrs starting from
// the last one that worked, rotating through the rest, and returns the first
// success (updating the preferred index) or the last error.
func newFailoverDialer(addrs []string, dialOne dialOneFn) func(context.Context) (net.Conn, *protocol.SessionCrypto, error) {
	var mu sync.Mutex
	preferred := 0

	return func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error) {
		mu.Lock()
		start := preferred
		mu.Unlock()

		var lastErr error
		for i := 0; i < len(addrs); i++ {
			idx := (start + i) % len(addrs)
			conn, crypto, err := dialOne(ctx, addrs[idx])
			if err == nil {
				mu.Lock()
				preferred = idx
				mu.Unlock()
				return conn, crypto, nil
			}
			lastErr = err
		}
		return nil, nil, lastErr
	}
}

// dialAddr establishes the outer TLS 1.3 connection to addr (SNI = cfg.Domain,
// ClientHello mimicked via uTLS to look like a real browser - since cfg.Domain
// has a real, CA-signed certificate now, this connection is byte-for-byte
// indistinguishable from a real browser visiting a real site, unlike v1's
// self-signed-cert masquerade), then performs the disguised handshake
// (internal/handshake) to authenticate and derive per-session keys.
func dialAddr(ctx context.Context, cfg *TLSClientConfig, addr string) (net.Conn, *protocol.SessionCrypto, error) {
	if cfg.RelayDomain == "" {
		return dialName(ctx, cfg, addr)
	}
	relayed := *cfg
	relayed.Domain, relayed.RelayDomain = cfg.RelayDomain, ""
	conn, crypto, err := dialName(ctx, &relayed, addr)
	if err == nil || errors.Is(err, errTCPDial) || ctx.Err() != nil {
		return conn, crypto, err
	}
	// The relay reached the server but the relay-facing name didn't work -
	// a server with no certificate for it yet. Its own domain does.
	logx.Infof("[client] %s through the relay failed (%v), trying %s", cfg.RelayDomain, err, cfg.Domain)
	return dialName(ctx, cfg, addr)
}

// errTCPDial marks a failure before TLS began: no point trying another name.
var errTCPDial = errors.New("tcp dial")

func dialName(ctx context.Context, cfg *TLSClientConfig, addr string) (net.Conn, *protocol.SessionCrypto, error) {
	dialer := &net.Dialer{Timeout: defaultTimeout}
	if cfg.ProtectFD != nil {
		dialer.Control = func(network, address string, c syscall.RawConn) error {
			var ctrlErr error
			if err := c.Control(func(fd uintptr) {
				if !cfg.ProtectFD(int(fd)) {
					ctrlErr = fmt.Errorf("failed to protect socket fd %d", fd)
				}
			}); err != nil {
				return err
			}
			return ctrlErr
		}
	}

	// Resolved here rather than by the dialer, so the lookup can go through
	// cfg.Resolver and fall back to the last good address - see resolve.go.
	targets, err := resolveServer(ctx, cfg.Resolver, addr)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errTCPDial, err)
	}
	var conn net.Conn
	for _, target := range targets {
		conn, err = dialer.DialContext(ctx, "tcp", target)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errTCPDial, err)
	}
	if host, _, splitErr := net.SplitHostPort(addr); splitErr == nil && net.ParseIP(host) == nil {
		if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
			rememberGoodIP(host, tcpAddr.IP.String())
		}
	}
	if cfg.WrapConn != nil {
		conn = cfg.WrapConn(conn)
	}

	utlsCfg := &utls.Config{
		ServerName: cfg.Domain,
		MinVersion: tls.VersionTLS13,
		RootCAs:    cfg.rootCAs,
	}

	clientHelloID, err := getFingerprint(cfg.Fingerprint)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	// Paces handshakes to one SNI - see waitHandshakeTurn. After the TCP
	// connect, right before the ClientHello: it's the ClientHellos' spacing
	// that's counted, and TCP connect times vary too much to pace by.
	if err := waitHandshakeTurn(ctx, cfg.Domain); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("waiting to handshake: %w", err)
	}

	uconn := utls.UClient(conn, utlsCfg, clientHelloID)
	if err := uconn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("tls handshake: %w", err)
	}

	// HandshakeContext only bounds the TLS handshake itself - once it returns,
	// ctx's deadline stops being enforced on this conn at all, since nothing
	// downstream watches ctx.Done(). The disguised handshake below
	// (handshake.ClientHandshake) does a plain blocking Write then
	// http.ReadResponse with no deadline of its own, so a peer that completes
	// the outer TLS handshake but then never answers the upgrade request (a
	// stalled server, or a middlebox that silently drops packets afterward)
	// left this call - and every caller waiting on it, including a UI button -
	// blocked forever. A bounded deadline here, capped to whatever's left on
	// ctx, is what makes Connect() a reliable "up or definitively failed"
	// call the way its callers already assume it is.
	deadline := time.Now().Add(defaultTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := uconn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("set handshake deadline: %w", err)
	}

	// Mimicking a real Chrome ClientHello (via clientHelloID above) includes a
	// renegotiation_info extension, which makes uTLS set config.Renegotiation
	// to a non-Never value to match - and the underlying TLS stack refuses to
	// export keying material at all whenever renegotiation is enabled, since
	// it can't be considered channel-bound across a possible renegotiation.
	// This is read fresh from the config on every ConnectionState() call (not
	// latched at handshake time), and the ClientHello bytes are already on
	// the wire by this point, so resetting it now doesn't touch the
	// fingerprint - it only re-enables ExportKeyingMaterial for the exporter
	// below (internal/handshake's replay-binding).
	utlsCfg.Renegotiation = utls.RenegotiateNever

	exporter := func(label string, context []byte, length int) ([]byte, error) {
		state := uconn.ConnectionState()
		return state.ExportKeyingMaterial(label, context, length)
	}

	// The carrier follows ALPN: a server offering h2 (2.3 on) gets the
	// handshake and the tunnel inside an HTTP/2 stream (h2tunnel.go), one
	// offering only http/1.1 the WebSocket upgrade it has always understood.
	var tunnelConn net.Conn = uconn
	var crypto *protocol.SessionCrypto
	if uconn.ConnectionState().NegotiatedProtocol == "h2" {
		tunnelConn, crypto, err = clientH2(uconn, cfg.Domain, cfg.PSK, cfg.ServerPub, exporter)
	} else {
		crypto, err = handshake.ClientHandshake(uconn, cfg.Domain, cfg.PSK, cfg.ServerPub, exporter)
	}
	if err != nil {
		uconn.Close()
		return nil, nil, fmt.Errorf("handshake: %w", err)
	}

	// Clear the handshake deadline - the tunnel that follows manages its own
	// read/write timing (see internal/tunnel), and leaving this one in place
	// would tear the connection down mid-session the moment it's reached.
	if err := uconn.SetDeadline(time.Time{}); err != nil {
		tunnelConn.Close()
		return nil, nil, fmt.Errorf("clear handshake deadline: %w", err)
	}

	return tunnelConn, crypto, nil
}
