package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"
)

// testServer is the "real server" behind the relay: a TLS echo server with
// its own certificate for domain, reporting the SNI each connection asked for.
func testServer(t *testing.T, domain string) (port int, pool *x509.CertPool, snis <-chan string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)

	seen := make(chan string, 8)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			seen <- h.ServerName
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, pool, seen
}

// startRelay runs a relay on loopback whose lookups resolve every name to
// 127.0.0.1, with private targets allowed so it may reach the test server.
func startRelay(t *testing.T, cfg *Config) string {
	t.Helper()
	cfg.AllowPrivate = true
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = []string{ln.Addr().String()}
	r := New(cfg)
	r.lookup = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }

	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.Serve(ctx, ln)
	return ln.Addr().String()
}

func TestRelayForwardsBySNI(t *testing.T) {
	port, pool, snis := testServer(t, "vpn.test")
	relayAddr := startRelay(t, &Config{TargetPort: port, MaxConns: 8})

	c, err := tls.Dial("tcp", relayAddr, &tls.Config{ServerName: "vpn.test", RootCAs: pool})
	if err != nil {
		t.Fatalf("TLS through the relay: %v", err)
	}
	defer c.Close()
	if got := <-snis; got != "vpn.test" {
		t.Fatalf("server saw SNI %q", got)
	}

	// Enough to cross several TLS records each way.
	msg := make([]byte, 200<<10)
	rand.Read(msg)
	go c.Write(msg)
	got := make([]byte, len(msg))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("echo through the relay: %v", err)
	}
	for i := range msg {
		if got[i] != msg[i] {
			t.Fatalf("echo differs at byte %d", i)
		}
	}
}

func TestRelayRoutes(t *testing.T) {
	port, pool, snis := testServer(t, "vpn.test")
	relayAddr := startRelay(t, &Config{
		TargetPort: 1, // nothing listens there: only the route can reach the server
		Routes:     map[string]string{"vpn.test": "127.0.0.1:" + strconv.Itoa(port)},
		MaxConns:   8,
	})
	c, err := tls.Dial("tcp", relayAddr, &tls.Config{ServerName: "vpn.test", RootCAs: pool})
	if err != nil {
		t.Fatalf("TLS through a route: %v", err)
	}
	c.Close()
	<-snis
}

func TestRelayRefuses(t *testing.T) {
	port, pool, _ := testServer(t, "vpn.test")
	relayAddr := startRelay(t, &Config{TargetPort: port, Allow: []string{"other.test", "*.allowed.test"}, MaxConns: 8})

	if _, err := tls.Dial("tcp", relayAddr, &tls.Config{ServerName: "vpn.test", RootCAs: pool}); err == nil {
		t.Fatal("a domain outside allow was forwarded")
	}
	// No SNI at all (a client dialing by IP) is never forwarded.
	if _, err := tls.Dial("tcp", relayAddr, &tls.Config{InsecureSkipVerify: true}); err == nil {
		t.Fatal("a ClientHello without SNI was forwarded")
	}
}

func TestAllowAndPublic(t *testing.T) {
	r := New(&Config{Allow: []string{"vpn.example", "*.example.org"}, MaxConns: 1})
	for sni, want := range map[string]bool{
		"vpn.example": true, "a.vpn.example": false,
		"x.example.org": true, "example.org": false, "evil-example.org": false,
	} {
		if got := r.allowed(sni); got != want {
			t.Errorf("allowed(%q) = %v, want %v", sni, got, want)
		}
	}
	for ip, want := range map[string]bool{
		"34.88.191.124": true, "127.0.0.1": false, "10.1.2.3": false, "192.168.0.1": false,
		"169.254.169.254": false, "::1": false, "0.0.0.0": false, "2a00:1450::1": true,
	} {
		if got := isPublicIP(net.ParseIP(ip)); got != want {
			t.Errorf("isPublicIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestRelayWontLoopOrGoPrivate(t *testing.T) {
	r := New(&Config{TargetPort: 8443, MaxConns: 1})
	r.lookup = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.5"), net.ParseIP("127.0.0.1")}, nil
	}
	if _, err := r.target(context.Background(), "vpn.test"); err == nil {
		t.Fatal("a domain resolving only to private addresses got a target")
	}

	// Private allowed, but the target is this machine on the relay's own port.
	r = New(&Config{Listen: []string{":8443"}, TargetPort: 8443, AllowPrivate: true, MaxConns: 1})
	r.lookup = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	if _, err := r.target(context.Background(), "vpn.test"); err == nil {
		t.Fatal("a target that is the relay itself was accepted")
	}
	// The same machine on another port is a server next to the relay: fine.
	r = New(&Config{Listen: []string{":443"}, TargetPort: 8443, AllowPrivate: true, MaxConns: 1})
	r.lookup = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	if got, err := r.target(context.Background(), "vpn.test"); err != nil || got != "127.0.0.1:8443" {
		t.Fatalf("server beside the relay: %q, %v", got, err)
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig([]byte("allow: [\"vpn.example\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Listen) != 2 || cfg.TargetPort != 8443 || cfg.MaxConns != 4096 {
		t.Fatalf("defaults: %+v", cfg)
	}
	if _, err := ParseConfig([]byte("routes: {a.example: \"nope\"}\n")); err == nil {
		t.Fatal("a route without a port was accepted")
	}
}
