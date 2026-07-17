package handshake

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"testing"

	"golang.org/x/crypto/curve25519"

	"phantom/internal/protocol"
)

// oldServerHandshake mimics a pre-ephemeral-ephemeral server: it authenticates
// exactly as before and replies 101 with NO Set-Cookie, so it can stand in for
// an older deployment when checking that a current client falls back to the
// static-only key. Returns the static InnerKey it derived.
func oldServerHandshake(t *testing.T, rw readWriter, psk, serverPriv, serverPub []byte) []byte {
	t.Helper()
	br := bufio.NewReader(rw)
	req, err := http.ReadRequest(br)
	if err != nil {
		t.Errorf("old server read request: %v", err)
		return nil
	}
	cookie, err := req.Cookie(cookieName)
	if err != nil {
		t.Errorf("old server: no session cookie")
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) != 32+authTagSize {
		t.Errorf("old server: bad cookie")
		return nil
	}
	clientPub := raw[:32]
	es, err := curve25519.X25519(serverPriv, clientPub)
	if err != nil {
		t.Errorf("old server ecdh: %v", err)
		return nil
	}
	crypto, err := protocol.DeriveSessionKeys(es, psk, clientPub, serverPub)
	if err != nil {
		t.Errorf("old server derive: %v", err)
		return nil
	}
	accept := computeWebSocketAccept(req.Header.Get("Sec-WebSocket-Key"))
	resp := fmt.Sprintf(
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n"+
			"\r\n",
		accept,
	)
	if _, err := rw.Write([]byte(resp)); err != nil {
		t.Errorf("old server write: %v", err)
		return nil
	}
	return crypto.InnerKey[:]
}

// oldClientHandshake mimics a pre-ephemeral-ephemeral client: it sends only the
// session cookie (no capability cookie) and never reads a server ephemeral key,
// so it stands in for an older client. Returns the static InnerKey it derived.
func oldClientHandshake(t *testing.T, rw readWriter, domain string, psk, serverStaticPub []byte, exporter ExportKeyingMaterial) []byte {
	t.Helper()
	clientPriv, clientPub, err := genEphemeral()
	if err != nil {
		t.Errorf("old client eph: %v", err)
		return nil
	}
	es, err := curve25519.X25519(clientPriv, serverStaticPub)
	if err != nil {
		t.Errorf("old client ecdh: %v", err)
		return nil
	}
	binding, err := exporter("phantom-handshake", nil, 32)
	if err != nil {
		t.Errorf("old client exporter: %v", err)
		return nil
	}
	crypto, err := protocol.DeriveSessionKeys(es, psk, clientPub, serverStaticPub)
	if err != nil {
		t.Errorf("old client derive: %v", err)
		return nil
	}
	tag := computeAuthTag(crypto.AuthKey[:], clientPub, binding)
	cookieValue := base64.RawURLEncoding.EncodeToString(append(append([]byte{}, clientPub...), tag...))
	req := fmt.Sprintf(
		"GET /ws HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Connection: Upgrade\r\n"+
			"Upgrade: websocket\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
			"Cookie: %s=%s\r\n"+
			"\r\n",
		domain, cookieName, cookieValue,
	)
	if _, err := rw.Write([]byte(req)); err != nil {
		t.Errorf("old client write: %v", err)
		return nil
	}
	br := bufio.NewReader(rw)
	if _, err := http.ReadResponse(br, nil); err != nil {
		t.Errorf("old client read response: %v", err)
		return nil
	}
	return crypto.InnerKey[:]
}

// TestHandshakeEE_NewClientOldServer: a current client talking to a server that
// never sends an ephemeral key must fall back to the static-only InnerKey and
// still match.
func TestHandshakeEE_NewClientOldServer(t *testing.T) {
	serverPriv, serverPub := genServerKeypair(t)
	psk := []byte("0123456789abcdef0123456789abcdef")

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	serverKey := make(chan []byte, 1)
	go func() {
		serverKey <- oldServerHandshake(t, serverConn, psk, serverPriv, serverPub)
	}()

	crypto, err := ClientHandshake(clientConn, "example.com", psk, serverPub, fakeExporter(0x42))
	if err != nil {
		t.Fatalf("ClientHandshake() error = %v", err)
	}
	if got, want := crypto.InnerKey[:], <-serverKey; !bytes.Equal(got, want) {
		t.Error("new client did not fall back to the static InnerKey against an old server")
	}
}

// TestHandshakeEE_OldClientNewServer: an old client (no capability cookie) must
// authenticate against a current server, which must stay on the static-only key
// and match.
func TestHandshakeEE_OldClientNewServer(t *testing.T) {
	serverPriv, serverPub := genServerKeypair(t)
	psk := []byte("0123456789abcdef0123456789abcdef")

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	clientKey := make(chan []byte, 1)
	go func() {
		clientKey <- oldClientHandshake(t, clientConn, "example.com", psk, serverPub, fakeExporter(0x42))
	}()

	result, req, err := ServerHandshake(serverConn, psk, serverPriv, serverPub, fakeExporter(0x42))
	if err != nil {
		t.Fatalf("ServerHandshake() error = %v", err)
	}
	if result == nil {
		t.Fatalf("current server failed to authenticate an old client (req=%v)", req)
	}
	if got, want := result.Crypto.InnerKey[:], <-clientKey; !bytes.Equal(got, want) {
		t.Error("current server did not stay on the static InnerKey for an old client")
	}
}
