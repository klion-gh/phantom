package tunnel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"phantom/internal/protocol"
)

// A guard on the tunnel's stop path: the server stops reading (a blackholed
// connection looks exactly like this from the client), so the multiplexer's
// write loop blocks inside a TLS Write holding TLS's write lock - and Close
// must still return promptly rather than wait on that lock to send
// close_notify. Both crypto/tls and uTLS skip close_notify when a Write is in
// flight, which is what makes this pass; the test is here so it keeps passing
// if either ever changes, since netstack.Tunnel.Stop now closes the session
// first precisely because the stop path must not block on a silent server.
func TestCloseReturnsWhileAWriteIsStuckOnAStalledConnection(t *testing.T) {
	cert := selfSignedCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	serverSide := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.(*tls.Conn).Handshake() // then never reads again: a server gone silent
		serverSide <- c
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := tls.Client(raw, &tls.Config{InsecureSkipVerify: true})
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		select {
		case c := <-serverSide:
			c.Close()
		default:
		}
	}()

	crypto, err := protocol.DeriveSessionKeys(
		[]byte("0123456789abcdef0123456789abcdef"), []byte("shared-psk-bytes-1234567890abcdef"),
		[]byte("client-ephemeral-pub-1234567890ab"), []byte("server-static-pub-1234567890abcd"),
		protocol.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMultiplexer(client, crypto)

	stream, err := m.Open("example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	// Keep writing until the socket is full and the write loop is stuck in a
	// TLS Write that will never complete.
	go func() {
		chunk := make([]byte, 60000)
		for {
			if _, err := stream.Write(chunk); err != nil {
				return
			}
		}
	}()
	waitUntilWritesStall(t, m)

	closed := make(chan struct{})
	go func() {
		m.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close hung behind a write stuck on the stalled connection")
	}
}

// waitUntilWritesStall waits until the connection has stopped accepting data -
// the write loop is then blocked mid-Write, which is the state that matters.
func waitUntilWritesStall(t *testing.T, m *Multiplexer) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	last := int64(-1)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		sent := m.Activity().BytesTx
		if sent > 0 && sent == last {
			return
		}
		last = sent
	}
	t.Fatal("writes never stalled - the socket buffer never filled")
}

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "stall.test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
