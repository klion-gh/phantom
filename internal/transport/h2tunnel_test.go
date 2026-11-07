package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"phantom/internal/handshake"
	"phantom/internal/protocol"
	"phantom/internal/proxy"
	"phantom/internal/tunnel"
)

type testServer struct {
	addr   string
	roots  *x509.CertPool
	pub    []byte
	psk    []byte
	tunnel chan struct{} // closed when an authenticated tunnel starts
}

// startTunnelServer runs handleConnection - the production path - behind a
// listener offering alpn, relaying authenticated streams to their targets.
func startTunnelServer(t *testing.T, domain string, alpn []string, extraNames ...string) *testServer {
	t.Helper()
	priv, pub := genX25519Keypair(t)
	psk := []byte("h2-tunnel-test-psk-0123456789ab")
	cert := generateThrowawayCert(t, domain, extraNames...)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: alpn,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ts := &testServer{addr: ln.Addr().String(), roots: roots, pub: pub, psk: psk, tunnel: make(chan struct{})}
	var once sync.Once
	cfg := &TLSServerConfig{PSK: psk, ServerPriv: priv, ServerPub: pub, Decoy: NewDecoySite("")}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleConnection(c, cfg, func(conn net.Conn, crypto *protocol.SessionCrypto) {
				once.Do(func() { close(ts.tunnel) })
				session := tunnel.NewSessionFromMux(tunnel.NewMultiplexer(conn, crypto))
				defer session.Close()
				direct := proxy.NewDirectOutbound(5 * time.Second)
				direct.AllowLocalTargets(true)
				session.HandleIncoming(context.Background(), direct.HandleStream)
			})
		}
	}()
	return ts
}

func (ts *testServer) dial(t *testing.T, domain string, psk []byte) (net.Conn, *protocol.SessionCrypto, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return Dial(ctx, &TLSClientConfig{Domain: domain, ServerAddr: ts.addr, PSK: psk, ServerPub: ts.pub, rootCAs: ts.roots})
}

// echoThrough sends size random bytes on each of n parallel streams to an
// echo server through the tunnel and checks every byte comes back.
func echoThrough(t *testing.T, conn net.Conn, crypto *protocol.SessionCrypto, n, size int) {
	t.Helper()
	echo := startEchoServer(t)
	session := tunnel.NewSessionFromMux(tunnel.NewMultiplexer(conn, crypto))
	defer session.Close()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := session.Open(echo)
			if err != nil {
				t.Error(err)
				return
			}
			defer s.Close()
			payload := make([]byte, size)
			rand.Read(payload)
			go s.Write(payload)
			got := make([]byte, size)
			if _, err := io.ReadFull(s, got); err != nil {
				t.Errorf("read back: %v", err)
				return
			}
			if !bytes.Equal(got, payload) {
				t.Error("echo mismatch")
			}
		}()
	}
	wg.Wait()
}

// 2.3 client, 2.3 server: the tunnel runs inside an HTTP/2 stream, and
// carries several megabytes on parallel streams both ways - flow control on
// both sides included.
func TestTunnelOverH2(t *testing.T) {
	ts := startTunnelServer(t, "h2.phantom.test", serverALPN)
	conn, crypto, err := ts.dial(t, "h2.phantom.test", ts.psk)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := conn.(*h2ClientConn); !ok {
		t.Fatalf("tunnel is a %T, want the HTTP/2 carrier", conn)
	}
	echoThrough(t, conn, crypto, 3, 3<<20)
}

// 2.3 client, server before 2.3 (offers only http/1.1): the client falls
// back to the WebSocket upgrade.
func TestNewClientOldServerUsesUpgrade(t *testing.T) {
	ts := startTunnelServer(t, "h1.phantom.test", []string{"http/1.1"})
	conn, crypto, err := ts.dial(t, "h1.phantom.test", ts.psk)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := conn.(*h2ClientConn); ok {
		t.Fatal("used HTTP/2 against a server that offered only http/1.1")
	}
	echoThrough(t, conn, crypto, 2, 1<<20)
}

// Wrong keys over HTTP/2 are refused like any other probe: no tunnel, and
// the client reports an auth failure rather than hanging.
func TestH2WrongKeysAreRefused(t *testing.T) {
	ts := startTunnelServer(t, "bad.phantom.test", serverALPN)
	_, _, err := ts.dial(t, "bad.phantom.test", []byte("not-the-psk-0123456789abcdefghij"))
	if !errors.Is(err, handshake.ErrAuthFailed) {
		t.Fatalf("got %v, want ErrAuthFailed", err)
	}
	select {
	case <-ts.tunnel:
		t.Fatal("a tunnel started for a client with the wrong keys")
	default:
	}
}

// A POST without valid auth - what a curious prober might send - gets the
// site, not a tunnel and not an error.
func TestH2UnauthenticatedPostGetsDecoy(t *testing.T) {
	ts := startTunnelServer(t, "probe.phantom.test", serverALPN)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: ts.roots, ServerName: "probe.phantom.test"},
		ForceAttemptHTTP2: true,
	}}
	resp, err := client.Post("https://"+ts.addr+"/ws", "application/octet-stream", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("got %s %d, want the decoy's 404 over HTTP/2", resp.Proto, resp.StatusCode)
	}
	select {
	case <-ts.tunnel:
		t.Fatal("an unauthenticated POST started a tunnel")
	default:
	}
}

// Every selectable fingerprint completes the handshake against a 2.3 server
// - over HTTP/2 if its ClientHello offers h2, by upgrade if not.
func TestEveryFingerprintAgainstH2Server(t *testing.T) {
	for _, fp := range []string{"auto", "firefox", "edge", "360", "qq", "chrome133", "chrome131", "safari16"} {
		t.Run(fp, func(t *testing.T) {
			domain := fp + ".h2fp.phantom.test"
			ts := startTunnelServer(t, domain, serverALPN)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, crypto, err := Dial(ctx, &TLSClientConfig{Domain: domain, Fingerprint: fp, ServerAddr: ts.addr,
				PSK: ts.psk, ServerPub: ts.pub, rootCAs: ts.roots})
			if err != nil {
				t.Fatalf("fingerprint %s: %v", fp, err)
			}
			echoThrough(t, conn, crypto, 1, 64<<10)
		})
	}
}
