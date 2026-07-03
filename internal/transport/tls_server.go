package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"

	"golang.org/x/crypto/acme/autocert"

	"phantom/internal/handshake"
	"phantom/internal/protocol"
)

type TLSServerConfig struct {
	ListenAddr   string
	Domain       string
	ACMEEmail    string
	ACMECacheDir string
	PSK          []byte
	ServerPriv   []byte // static X25519 private key, for the handshake's ECDH
	ServerPub    []byte
	Decoy        *DecoySite
}

// ListenAndServe replaces v1's single hardcoded self-signed certificate
// (CN=www.google.com, identical across every deployment of that code, and
// trivially detected by anything that validates the certificate chain) with a
// real, CA-signed certificate for the operator's own domain, obtained
// automatically via ACME (Let's Encrypt).
//
// Uses the HTTP-01 challenge type rather than TLS-ALPN-01, so the actual VPN
// listener (cfg.ListenAddr) is free to be any port (e.g. :8443) - only a
// small, separate HTTP responder on port 80 is needed for the (infrequent:
// ~every 60 days) certificate issuance/renewal handshake with Let's Encrypt.
// Port 80 carries no VPN traffic at all.
//
// Connections that don't pass internal/handshake's embedded auth check are
// handed to cfg.Decoy instead of being dropped.
func ListenAndServe(ctx context.Context, cfg *TLSServerConfig, handler func(net.Conn, *protocol.SessionCrypto)) error {
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
		// certificates also need to be renewed periodically.
		srv := &http.Server{Addr: ":80", Handler: certManager.HTTPHandler(nil)}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[server] ACME HTTP-01 responder on :80 failed: %v", err)
		}
	}()

	tlsConfig := certManager.TLSConfig()
	tlsConfig.MinVersion = tls.VersionTLS13

	listener, err := tls.Listen("tcp", cfg.ListenAddr, tlsConfig)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	log.Printf("[server] listening on %s (domain=%s)", cfg.ListenAddr, cfg.Domain)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := listener.Accept()
		if err != nil {
			log.Printf("[server] accept error: %v", err)
			continue
		}

		go handleConnection(conn, cfg, handler)
	}
}

func handleConnection(conn net.Conn, cfg *TLSServerConfig, handler func(net.Conn, *protocol.SessionCrypto)) {
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return
	}

	if err := tlsConn.Handshake(); err != nil {
		log.Printf("[server] tls handshake error: %v", err)
		return
	}

	// ACME's own tls-alpn-01 validation probes negotiate the special
	// "acme-tls/1" ALPN protocol; autocert.Manager.GetCertificate answers
	// those by itself with a throwaway challenge certificate and no
	// application data ever follows - nothing else to do for those.
	if tlsConn.ConnectionState().NegotiatedProtocol == "acme-tls/1" {
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
		log.Printf("[server] handshake read error: %v", err)
		return
	}
	if result == nil {
		cfg.Decoy.Serve(tlsConn, req)
		return
	}

	handler(tlsConn, result.Crypto)
}
