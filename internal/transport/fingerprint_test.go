package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	"phantom/internal/protocol"
)

// Every fingerprint a config can name must complete the whole real client
// path against a server shaped like ours: TLS 1.3 only, http/1.1 as the only
// ALPN, the renegotiation reset that keying-material export depends on, and
// the disguised handshake on top. Older profiles (Edge 106, QQ) are exactly
// the ones that could quietly fail one of those - OkHttp did (TLS 1.2 only)
// and was dropped because of it.
func TestEveryFingerprintCompletesTheFullHandshake(t *testing.T) {
	for _, fp := range []string{"auto", "", "firefox", "edge", "360", "qq", "chrome133", "chrome131", "safari16"} {
		name := fp
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			// A domain per case, so the handshake pacing (one SNI) doesn't
			// serialise the cases behind each other.
			domain := name + ".phantom.test"
			serverPriv, serverPub := genX25519Keypair(t)
			psk := []byte("fingerprint-test-psk-0123456789")

			cert := generateThrowawayCert(t, domain)
			leaf, err := x509.ParseCertificate(cert.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			roots.AddCert(leaf)

			listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS13,
				NextProtos:   []string{"http/1.1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()

			authed := make(chan struct{})
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				handleConnection(conn, &TLSServerConfig{
					PSK: psk, ServerPriv: serverPriv, ServerPub: serverPub, Decoy: NewDecoySite(""),
				}, func(net.Conn, *protocol.SessionCrypto) { close(authed) })
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, crypto, err := Dial(ctx, &TLSClientConfig{
				Domain:      domain,
				Fingerprint: fp,
				ServerAddr:  listener.Addr().String(),
				PSK:         psk,
				ServerPub:   serverPub,
				rootCAs:     roots,
			})
			if err != nil {
				t.Fatalf("fingerprint %q could not complete the handshake: %v", fp, err)
			}
			defer conn.Close()
			if crypto == nil {
				t.Fatal("no session keys")
			}
			select {
			case <-authed:
			case <-time.After(5 * time.Second):
				t.Fatal("server never authenticated the client")
			}
		})
	}
}

// Unknown names still connect (on Firefox) but say so, rather than failing
// the connection over a typo in a config.
func TestUnknownFingerprintFallsBackWithError(t *testing.T) {
	if _, err := getFingerprint("netscape4"); err == nil {
		t.Fatal("expected an error naming the unknown fingerprint")
	}
}

// The pacing guarantee: handshakes to one SNI are never closer together than
// the spacing, however many ask at once; a different SNI isn't held up.
func TestHandshakeGateSpacesOneSNIButNotOthers(t *testing.T) {
	g := &gate{next: map[string]time.Time{}}
	const spacing = 80 * time.Millisecond
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := g.wait(ctx, "a.example", spacing); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 3*spacing {
		t.Fatalf("4 handshakes to one SNI took %v, expected at least %v", elapsed, 3*spacing)
	}

	other := time.Now()
	if err := g.wait(ctx, "b.example", spacing); err != nil {
		t.Fatal(err)
	}
	if time.Since(other) > spacing/2 {
		t.Fatal("a different SNI was held up by another one's spacing")
	}
}

// A caller that gives up (its context ends) stops waiting instead of hanging.
func TestHandshakeGateHonoursContext(t *testing.T) {
	g := &gate{next: map[string]time.Time{}}
	g.wait(context.Background(), "a.example", time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := g.wait(ctx, "a.example", time.Hour); err == nil {
		t.Fatal("expected the wait to end with the context")
	}
}
