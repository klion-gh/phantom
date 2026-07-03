package transport

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"

	"phantom/internal/handshake"
	"phantom/internal/protocol"
	"phantom/internal/proxy"
	"phantom/internal/tunnel"
)

// This is a full-stack local test of everything downstream of certificate
// acquisition: real TLS 1.3 (a throwaway self-signed cert stands in for the
// real ACME-issued one - obtaining a real cert needs a real domain with DNS
// pointed at a real public IP, which this test environment doesn't have),
// the disguised handshake, the multiplexer, and a real TCP echo relay end to
// end. It reuses the exact same handleConnection production code path as
// ListenAndServe by constructing the tls.Listener itself instead of going
// through autocert.
func TestFullStackTCPRelay(t *testing.T) {
	serverPriv, serverPub := genX25519Keypair(t)
	psk := []byte("integration-test-psk-0123456789")
	domain := "phantom.test"

	echoAddr := startEchoServer(t)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{generateThrowawayCert(t, domain)},
		MinVersion:   tls.VersionTLS13,
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	cfg := &TLSServerConfig{
		PSK:        psk,
		ServerPriv: serverPriv,
		ServerPub:  serverPub,
		Decoy:      NewDecoySite(""),
	}

	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrCh <- err
			return
		}
		handleConnection(conn, cfg, func(conn net.Conn, crypto *protocol.SessionCrypto) {
			mux := tunnel.NewMultiplexer(conn, crypto, false)
			session := tunnel.NewSessionFromMux(mux)
			defer session.Close()

			direct := proxy.NewDirectOutbound(5 * time.Second)
			stream, err := session.Accept()
			if err != nil {
				serverErrCh <- err
				return
			}
			direct.HandleStream(stream)
			serverErrCh <- nil
		})
	}()

	// Client side: plain tls.Dial (uTLS's ClientHello mimicry is irrelevant to
	// this test) with InsecureSkipVerify since the throwaway cert isn't
	// CA-signed - the real client (transport.Dial) validates a real cert.
	rawConn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		ServerName:         domain,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("client tls dial: %v", err)
	}
	defer rawConn.Close()

	exporter := func(label string, context []byte, length int) ([]byte, error) {
		state := rawConn.ConnectionState()
		return state.ExportKeyingMaterial(label, context, length)
	}

	crypto, err := handshake.ClientHandshake(rawConn, domain, psk, serverPub, exporter)
	if err != nil {
		t.Fatalf("ClientHandshake() error = %v", err)
	}

	clientMux := tunnel.NewMultiplexer(rawConn, crypto, false)
	clientSession := tunnel.NewSessionFromMux(clientMux)
	defer clientSession.Close()

	stream, err := clientSession.Open(echoAddr)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer stream.Close()

	payload := []byte("hello over the full phantom v2 stack")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Errorf("echo mismatch: got %q, want %q", buf, payload)
	}
}

func TestFullStackUDPRelay(t *testing.T) {
	serverPriv, serverPub := genX25519Keypair(t)
	psk := []byte("integration-test-psk-0123456789")
	domain := "phantom.test"

	echoAddr := startUDPEchoServer(t)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{generateThrowawayCert(t, domain)},
		MinVersion:   tls.VersionTLS13,
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	cfg := &TLSServerConfig{
		PSK:        psk,
		ServerPriv: serverPriv,
		ServerPub:  serverPub,
		Decoy:      NewDecoySite(""),
	}

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		handleConnection(conn, cfg, func(conn net.Conn, crypto *protocol.SessionCrypto) {
			mux := tunnel.NewMultiplexer(conn, crypto, false)
			session := tunnel.NewSessionFromMux(mux)
			defer session.Close()

			direct := proxy.NewDirectOutbound(5 * time.Second)
			stream, err := session.Accept()
			if err != nil {
				return
			}
			direct.HandleStream(stream)
		})
	}()

	rawConn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		ServerName:         domain,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("client tls dial: %v", err)
	}
	defer rawConn.Close()

	exporter := func(label string, context []byte, length int) ([]byte, error) {
		state := rawConn.ConnectionState()
		return state.ExportKeyingMaterial(label, context, length)
	}

	crypto, err := handshake.ClientHandshake(rawConn, domain, psk, serverPub, exporter)
	if err != nil {
		t.Fatalf("ClientHandshake() error = %v", err)
	}

	clientMux := tunnel.NewMultiplexer(rawConn, crypto, false)
	clientSession := tunnel.NewSessionFromMux(clientMux)
	defer clientSession.Close()

	stream, err := clientSession.OpenUDP(echoAddr)
	if err != nil {
		t.Fatalf("OpenUDP() error = %v", err)
	}
	defer stream.Close()

	payload := []byte("udp datagram over phantom v2")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	buf := make([]byte, 1500)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Errorf("udp echo mismatch: got %q, want %q", buf[:n], payload)
	}
}

func TestFullStackDecoyFallback(t *testing.T) {
	serverPriv, serverPub := genX25519Keypair(t)
	psk := []byte("integration-test-psk-0123456789")
	domain := "phantom.test"

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{generateThrowawayCert(t, domain)},
		MinVersion:   tls.VersionTLS13,
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	cfg := &TLSServerConfig{
		PSK:        psk,
		ServerPriv: serverPriv,
		ServerPub:  serverPub,
		Decoy:      NewDecoySite(""),
	}

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		handleConnection(conn, cfg, func(net.Conn, *protocol.SessionCrypto) {
			t.Error("handler should not be invoked for an unauthenticated probe")
		})
	}()

	// A plain HTTPS client with no idea about the embedded auth cookie -
	// exactly what an automated censor probe or a curious human would send.
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		ServerName:         domain,
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("client tls dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: " + domain + "\r\n\r\n")); err != nil {
		t.Fatalf("write probe request: %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read decoy response: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected the decoy site to answer 200 OK, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Example Corp")) {
		t.Errorf("expected the built-in decoy page, got: %s", body)
	}
}

func genX25519Keypair(t *testing.T) (priv, pub []byte) {
	t.Helper()
	priv = make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func generateThrowawayCert(t *testing.T, domain string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func startUDPEchoServer(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			conn.WriteTo(buf[:n], addr)
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return conn.LocalAddr().String()
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

