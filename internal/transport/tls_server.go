package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"

	"golang.org/x/crypto/acme/autocert"

	"phantom/internal/handshake"
	"phantom/internal/logx"
	"phantom/internal/protocol"
)

type TLSServerConfig struct {
	ListenAddr   string
	Domain       string
	ACMEEmail    string
	ACMECacheDir string
	CertFile     string // static cert+key pair instead of ACME - see certReloader
	KeyFile      string
	PSK          []byte
	ServerPriv   []byte // static X25519 private key, for the handshake's ECDH
	ServerPub    []byte
	Decoy        *DecoySite

	// Per-IP auth-handshake throttle (see ratelimit.go); 0 = defaults. Built
	// into a *rateLimiter once, in ListenAndServe.
	HandshakeRatePerSec float64
	HandshakeBurst      float64
	limiter             *rateLimiter
}

// ListenAndServe replaces v1's single hardcoded self-signed certificate
// (CN=www.google.com, identical across every deployment of that code, and
// trivially detected by anything that validates the certificate chain) with a
// real, CA-signed certificate for the operator's own domain - by default
// obtained automatically via ACME (Let's Encrypt), or loaded from a static
// cert_file/key_file pair (e.g. an existing certbot certificate) if cfg sets
// those - see newStaticCertConfig for when that's needed.
//
// The ACME path uses the HTTP-01 challenge type rather than TLS-ALPN-01, so
// the actual VPN listener (cfg.ListenAddr) is free to be any port (e.g.
// :8443) - only a small, separate HTTP responder on port 80 is needed for
// the (infrequent: ~every 60 days) certificate issuance/renewal handshake
// with Let's Encrypt. Port 80 carries no VPN traffic at all.
//
// Connections that don't pass internal/handshake's embedded auth check are
// handed to cfg.Decoy instead of being dropped.
func ListenAndServe(ctx context.Context, cfg *TLSServerConfig, handler func(net.Conn, *protocol.SessionCrypto)) error {
	cfg.limiter = newRateLimiter(cfg.HandshakeRatePerSec, cfg.HandshakeBurst)

	var tlsConfig *tls.Config

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		staticConfig, err := newStaticCertConfig(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return fmt.Errorf("load cert_file/key_file: %w", err)
		}
		tlsConfig = staticConfig
		logx.Infof("[server] using static certificate %s (no ACME HTTP-01 responder - port 80 stays free for anything else already using it)", cfg.CertFile)
	} else {
		certManager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(cfg.ACMECacheDir),
			HostPolicy: autocert.HostWhitelist(cfg.Domain),
			Email:      cfg.ACMEEmail,
		}

		go func() {
			// autocert's HTTP-01 responder; must be reachable on :80 from the
			// internet for Let's Encrypt to validate domain ownership. Runs for
			// the lifetime of the process, not just during issuance, since
			// certificates also need to be renewed periodically. Requires
			// nothing else on this machine to already be listening on :80 - if
			// something is (e.g. a shared box also running its own nginx),
			// this fails silently in the background and every handshake ends
			// up with no certificate to offer; use cert_file/key_file instead.
			srv := &http.Server{Addr: ":80", Handler: certManager.HTTPHandler(nil)}
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logx.Warnf("[server] ACME HTTP-01 responder on :80 failed: %v", err)
			}
		}()

		tlsConfig = certManager.TLSConfig()
	}

	tlsConfig.MinVersion = tls.VersionTLS13

	listener, err := tls.Listen("tcp", cfg.ListenAddr, tlsConfig)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	logx.Infof("[server] listening on %s (domain=%s)", cfg.ListenAddr, cfg.Domain)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := listener.Accept()
		if err != nil {
			logx.Warnf("[server] accept error: %v", err)
			continue
		}

		go handleConnection(conn, cfg, handler)
	}
}

// newStaticCertConfig builds a tls.Config that serves certFile/keyFile
// directly - for a server that can't dedicate port 80 to ACME's HTTP-01
// challenge (typically a box already running its own web server on 80/443
// for other things), where the operator instead points this at a
// certificate obtained some other way (e.g. certbot, already renewing
// itself independently for that other web server).
//
// Uses GetCertificate rather than a one-time tls.LoadX509KeyPair so a
// renewal (certbot replaces the files at the same path, roughly every ~60-90
// days) picks up automatically on the next handshake - checked via a cheap
// os.Stat per handshake, only actually re-parsing the PEM files when their
// mtimes change - rather than requiring a manual restart or a renewal hook.
func newStaticCertConfig(certFile, keyFile string) (*tls.Config, error) {
	reloader := &certReloader{certFile: certFile, keyFile: keyFile}
	if _, err := reloader.load(); err != nil {
		return nil, err
	}
	return &tls.Config{GetCertificate: reloader.getCertificate}, nil
}

type certReloader struct {
	certFile, keyFile string

	mu          sync.Mutex
	cert        *tls.Certificate
	certModTime int64
	keyModTime  int64
}

func (r *certReloader) getCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert, err := r.load()
	if err != nil {
		// A transient stat/read failure (e.g. certbot mid-renewal, briefly
		// replacing the files) shouldn't fail every handshake in flight right
		// then - serve the last-known-good certificate instead if there is one.
		r.mu.Lock()
		last := r.cert
		r.mu.Unlock()
		if last != nil {
			return last, nil
		}
		return nil, err
	}
	return cert, nil
}

func (r *certReloader) load() (*tls.Certificate, error) {
	certInfo, err := os.Stat(r.certFile)
	if err != nil {
		return nil, fmt.Errorf("stat cert_file: %w", err)
	}
	keyInfo, err := os.Stat(r.keyFile)
	if err != nil {
		return nil, fmt.Errorf("stat key_file: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cert != nil && certInfo.ModTime().UnixNano() == r.certModTime && keyInfo.ModTime().UnixNano() == r.keyModTime {
		return r.cert, nil
	}

	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return nil, fmt.Errorf("parse cert_file/key_file: %w", err)
	}
	r.cert = &cert
	r.certModTime = certInfo.ModTime().UnixNano()
	r.keyModTime = keyInfo.ModTime().UnixNano()
	logx.Infof("[server] loaded certificate from %s (modified %s)", r.certFile, certInfo.ModTime())
	return r.cert, nil
}

func handleConnection(conn net.Conn, cfg *TLSServerConfig, handler func(net.Conn, *protocol.SessionCrypto)) {
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return
	}

	if err := tlsConn.Handshake(); err != nil {
		logx.Debugf("[server] tls handshake error: %v", err)
		return
	}

	// ACME's own tls-alpn-01 validation probes negotiate the special
	// "acme-tls/1" ALPN protocol; autocert.Manager.GetCertificate answers
	// those by itself with a throwaway challenge certificate and no
	// application data ever follows - nothing else to do for those.
	if tlsConn.ConnectionState().NegotiatedProtocol == "acme-tls/1" {
		return
	}

	// Over its per-IP budget: answer like an ordinary homepage visit without
	// spending an auth attempt on it (see ratelimit.go). Uses the connection's
	// remote IP; behind a reverse proxy this would be the proxy's IP, which is
	// fine - Phantom terminates its own TLS directly, there's no proxy in front.
	if ip, _, splitErr := net.SplitHostPort(conn.RemoteAddr().String()); splitErr == nil && !cfg.limiter.allow(ip) {
		metricRateLimited.Add(1)
		cfg.Decoy.ServeDefault(tlsConn)
		return
	}

	exporter := func(label string, context []byte, length int) ([]byte, error) {
		state := tlsConn.ConnectionState()
		return state.ExportKeyingMaterial(label, context, length)
	}

	result, req, err := handshake.ServerHandshake(tlsConn, cfg.PSK, cfg.ServerPriv, cfg.ServerPub, exporter)
	if err != nil {
		// Didn't even parse as HTTP - not a real prober worth a full decoy
		// response, just close like a real server would for garbage input.
		metricNonHTTP.Add(1)
		logx.Debugf("[server] handshake read error: %v", err)
		return
	}
	if result == nil {
		metricDecoyHits.Add(1)
		cfg.Decoy.Serve(tlsConn, req)
		return
	}

	metricHandshakeOK.Add(1)
	metricActiveNow.Add(1)
	defer metricActiveNow.Add(-1)
	handler(tlsConn, result.Crypto)
}
