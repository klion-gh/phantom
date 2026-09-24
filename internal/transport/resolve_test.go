package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"testing"
	"time"

	"phantom/internal/protocol"
)

type fakeResolver struct {
	ips []net.IP
	err error
}

func (f fakeResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return f.ips, f.err
}

// The failure from the field: mid-reconnect on Android nothing resolves the
// server's own hostname ("lookup <server>: no such host"), so the tunnel
// couldn't come back. A host dialed successfully once must still be reachable
// when the resolver then has no answer, through the address it was reached on.
func TestDialFallsBackToLastGoodAddressWhenLookupFails(t *testing.T) {
	const host = "resolve-fallback.phantom.test"
	serverPriv, serverPub := genX25519Keypair(t)
	psk := []byte("resolve-fallback-psk-0123456789")

	cert := generateThrowawayCert(t, host)
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
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleConnection(conn, &TLSServerConfig{
				PSK: psk, ServerPriv: serverPriv, ServerPub: serverPub, Decoy: NewDecoySite(""),
			}, func(c net.Conn, _ *protocol.SessionCrypto) { c.Close() })
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())

	dial := func(resolver IPLookuper) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, _, err := Dial(ctx, &TLSClientConfig{
			Domain:      host,
			Fingerprint: "firefox",
			ServerAddr:  net.JoinHostPort(host, port),
			PSK:         psk,
			ServerPub:   serverPub,
			Resolver:    resolver,
			rootCAs:     roots,
		})
		if err == nil {
			conn.Close()
		}
		return err
	}

	if err := dial(fakeResolver{ips: []net.IP{net.ParseIP("127.0.0.1")}}); err != nil {
		t.Fatalf("first dial, with a working resolver: %v", err)
	}
	if err := dial(fakeResolver{err: errors.New("no such host")}); err != nil {
		t.Fatalf("with the resolver down, the last good address should still have been used: %v", err)
	}
}

// A host never reached before has no fallback: the lookup error is reported.
func TestDialReportsLookupFailureWithoutAFallback(t *testing.T) {
	_, err := resolveServer(context.Background(), fakeResolver{err: errors.New("no such host")}, "never-seen.phantom.test:443")
	if err == nil {
		t.Fatal("expected the lookup failure to surface")
	}
}
