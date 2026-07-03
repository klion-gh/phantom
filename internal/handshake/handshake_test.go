package handshake

import (
	"bytes"
	"crypto/rand"
	"net"
	"testing"

	"golang.org/x/crypto/curve25519"
)

// fakeExporter simulates the TLS exporter keying material both sides would
// derive from the same real TLS connection - in these tests, both ends just
// share the same fixed value, standing in for "same underlying TLS session".
func fakeExporter(seed byte) ExportKeyingMaterial {
	return func(label string, context []byte, length int) ([]byte, error) {
		out := make([]byte, length)
		for i := range out {
			out[i] = seed
		}
		return out, nil
	}
}

func genServerKeypair(t *testing.T) (priv, pub []byte) {
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

func TestHandshakeRoundTrip(t *testing.T) {
	serverPriv, serverPub := genServerKeypair(t)
	psk := []byte("0123456789abcdef0123456789abcdef")

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	type clientResult struct {
		crypto []byte
		err    error
	}
	done := make(chan clientResult, 1)

	go func() {
		crypto, err := ClientHandshake(clientConn, "example.com", psk, serverPub, fakeExporter(0x42))
		if err != nil {
			done <- clientResult{err: err}
			return
		}
		done <- clientResult{crypto: crypto.InnerKey[:]}
	}()

	result, req, err := ServerHandshake(serverConn, psk, serverPriv, serverPub, fakeExporter(0x42))
	if err != nil {
		t.Fatalf("ServerHandshake() error = %v", err)
	}
	if req != nil {
		t.Fatalf("ServerHandshake() should have authenticated, got fallthrough request for %s", req.URL.Path)
	}
	if result == nil {
		t.Fatal("ServerHandshake() returned nil result on success")
	}

	clientRes := <-done
	if clientRes.err != nil {
		t.Fatalf("ClientHandshake() error = %v", clientRes.err)
	}

	if !bytes.Equal(result.Crypto.InnerKey[:], clientRes.crypto) {
		t.Error("client and server derived different InnerKeys for the same handshake")
	}
}

func TestHandshakeWrongPSKFallsThroughToDecoy(t *testing.T) {
	serverPriv, serverPub := genServerKeypair(t)
	clientPSK := []byte("client-psk-0123456789abcdefabcd")
	serverPSK := []byte("different-server-psk-0123456789")

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	go func() {
		// Client thinks the handshake succeeds or fails based on the response;
		// we only care that the server treats it as unauthenticated below.
		_, _ = ClientHandshake(clientConn, "example.com", clientPSK, serverPub, fakeExporter(0x11))
	}()

	result, req, err := ServerHandshake(serverConn, serverPSK, serverPriv, serverPub, fakeExporter(0x11))
	if err != nil {
		t.Fatalf("ServerHandshake() unexpected error = %v", err)
	}
	if result != nil {
		t.Fatal("ServerHandshake() should not authenticate with mismatched PSK")
	}
	if req == nil {
		t.Fatal("ServerHandshake() should return the parsed request for decoy fallback")
	}
	if req.Header.Get("Upgrade") != "websocket" {
		t.Errorf("expected a plausible websocket-upgrade-shaped request, got Upgrade=%q", req.Header.Get("Upgrade"))
	}
}

func TestHandshakeMismatchedTLSBindingFails(t *testing.T) {
	serverPriv, serverPub := genServerKeypair(t)
	psk := []byte("0123456789abcdef0123456789abcdef")

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	// Different exporter values simulate a captured cookie being replayed on
	// a different TLS connection - must not authenticate.
	go func() {
		_, _ = ClientHandshake(clientConn, "example.com", psk, serverPub, fakeExporter(0xAA))
	}()

	result, req, err := ServerHandshake(serverConn, psk, serverPriv, serverPub, fakeExporter(0xBB))
	if err != nil {
		t.Fatalf("ServerHandshake() unexpected error = %v", err)
	}
	if result != nil {
		t.Fatal("ServerHandshake() should not authenticate when the TLS binding differs")
	}
	if req == nil {
		t.Fatal("expected fallthrough request for decoy handling")
	}
}

func TestNonHandshakeRequestFallsThroughCleanly(t *testing.T) {
	serverPriv, serverPub := genServerKeypair(t)
	psk := []byte("0123456789abcdef0123456789abcdef")

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	go func() {
		clientConn.Write([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"))
	}()

	result, req, err := ServerHandshake(serverConn, psk, serverPriv, serverPub, fakeExporter(0x01))
	if err != nil {
		t.Fatalf("ServerHandshake() unexpected error = %v", err)
	}
	if result != nil {
		t.Fatal("a plain GET / with no auth cookie must not authenticate")
	}
	if req == nil || req.URL.Path != "/" {
		t.Fatalf("expected fallthrough request for '/', got %#v", req)
	}
}
