package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	utls "github.com/refraction-networking/utls"

	"phantom/internal/handshake"
	"phantom/internal/protocol"
)

type TLSClientConfig struct {
	Domain      string // real domain the server has a CA-signed cert for - used as SNI and Host header
	Fingerprint string
	ServerAddr  string   // single endpoint; used by Dial and as the fallback for NewFailoverDialer when ServerAddrs is empty
	ServerAddrs []string // optional multi-endpoint failover list (see NewFailoverDialer); all share Domain/PSK/ServerPub
	PSK         []byte
	ServerPub   []byte // server's static X25519 public key

	// ProtectFD, if set, is called with the raw socket fd right after it's
	// created and before connect(). On Android, once a VpnService routes
	// 0.0.0.0/0 through its TUN, the app's own outbound socket to the real
	// server gets captured by that same route unless the socket is
	// explicitly "protected" to bypass the VPN. Unused on desktop clients.
	ProtectFD func(fd int) bool
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

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("tcp dial: %w", err)
	}

	utlsCfg := &utls.Config{
		ServerName: cfg.Domain,
		MinVersion: tls.VersionTLS13,
	}

	clientHelloID, err := getFingerprint(cfg.Fingerprint)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	uconn := utls.UClient(conn, utlsCfg, clientHelloID)
	if err := uconn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("tls handshake: %w", err)
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

	crypto, err := handshake.ClientHandshake(uconn, cfg.Domain, cfg.PSK, cfg.ServerPub, exporter)
	if err != nil {
		uconn.Close()
		return nil, nil, fmt.Errorf("handshake: %w", err)
	}

	return uconn, crypto, nil
}

// getFingerprint maps a config's fingerprint name to a uTLS ClientHelloID.
//
// chrome131/chrome133 carry a real X25519MLKEM768 post-quantum hybrid key
// share, matching current real Chrome (~57%+ of real browser connections
// have one as of early 2026) - chrome120 (kept only for explicit opt-in/
// backward compat) predates Chrome's PQ rollout and is now the more
// anomalous-looking ClientHello of the two, not the safer default it used to
// be. firefox120/safari16 have no PQ-carrying capture available in the
// pinned uTLS version, so they stay as-is.
func getFingerprint(name string) (utls.ClientHelloID, error) {
	switch name {
	case "chrome131":
		return utls.HelloChrome_131, nil
	case "chrome133", "chrome":
		return utls.HelloChrome_133, nil
	case "chrome120":
		return utls.HelloChrome_120, nil
	case "firefox120", "firefox130", "firefox":
		return utls.HelloFirefox_120, nil
	case "safari16", "safari18", "safari":
		return utls.HelloSafari_16_0, nil
	default:
		return utls.HelloChrome_133, fmt.Errorf("unknown fingerprint %q, using chrome133", name)
	}
}
