package transport

import (
	"context"
	"net"
	"testing"
	"time"

	"phantom/internal/relay"
)

// A real client - uTLS fingerprint, TLS with the server's certificate, the
// disguised handshake, the HTTP/2 carrier - reaches its server through a
// relay that only ever sees the ClientHello and ciphertext.
func TestTunnelThroughRelay(t *testing.T) {
	const domain = "relayed.phantom.test"
	ts := startTunnelServer(t, domain, serverALPN)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := relay.New(&relay.Config{
		Listen:       []string{ln.Addr().String()},
		TargetPort:   8443,
		Routes:       map[string]string{domain: ts.addr},
		AllowPrivate: true,
		MaxConns:     8,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Serve(ctx, ln)

	for _, fp := range []string{"firefox", "chrome131"} {
		t.Run(fp, func(t *testing.T) {
			dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer dialCancel()
			conn, crypto, err := Dial(dialCtx, &TLSClientConfig{
				Domain: domain, Fingerprint: fp, ServerAddr: ln.Addr().String(),
				PSK: ts.psk, ServerPub: ts.pub, rootCAs: ts.roots,
			})
			if err != nil {
				t.Fatalf("dial through the relay: %v", err)
			}
			echoThrough(t, conn, crypto, 2, 1<<20)
		})
	}
}

// Through a relay with a prefix the client presents msk.<domain>: the server
// holding a certificate for it is reached under that name, and one that
// doesn't yet is still reached, under <domain>.
func TestTunnelThroughRelayWithPrefix(t *testing.T) {
	const domain = "prefixed.phantom.test"
	for _, tc := range []struct {
		name      string
		certNames []string
	}{
		{"server has the relay name", []string{"msk." + domain}},
		{"server without it falls back", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := startTunnelServer(t, domain, serverALPN, tc.certNames...)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			r := relay.New(&relay.Config{
				Listen:       []string{ln.Addr().String()},
				TargetPort:   8443,
				SNIPrefix:    "msk",
				Routes:       map[string]string{domain: ts.addr},
				AllowPrivate: true,
				MaxConns:     8,
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go r.Serve(ctx, ln)

			dialCtx, dialCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer dialCancel()
			conn, crypto, err := Dial(dialCtx, &TLSClientConfig{
				Domain: domain, RelayDomain: "msk." + domain, ServerAddr: ln.Addr().String(),
				PSK: ts.psk, ServerPub: ts.pub, rootCAs: ts.roots,
			})
			if err != nil {
				t.Fatalf("dial through the relay: %v", err)
			}
			echoThrough(t, conn, crypto, 1, 256<<10)
		})
	}
}
