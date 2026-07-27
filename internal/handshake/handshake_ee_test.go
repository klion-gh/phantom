package handshake

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"

	"golang.org/x/crypto/curve25519"

	"phantom/internal/protocol"
)

// This file used to test the ephemeral-ephemeral upgrade's *interop* with older
// peers in both directions: a current client falling back to the static-only key
// against an old server, and a current server staying static-only for an old
// client. WireVersion 2 removed that fallback deliberately - the ee exchange is
// mandatory and the version is bound into the auth tag - so the same scenarios
// now have to fail, cleanly and in the right way. That is what these test.

// v1ClientRequest writes a handshake request shaped exactly like a pre-v2 client's:
// authenticated the old way, with the wire version absent from the auth tag.
func v1ClientRequest(t *testing.T, rw readWriter, domain string, psk, serverStaticPub []byte, exporter ExportKeyingMaterial) {
	t.Helper()

	clientPriv, clientPub, err := genEphemeral()
	if err != nil {
		t.Errorf("v1 client keygen: %v", err)
		return
	}
	es, err := curve25519.X25519(clientPriv, serverStaticPub)
	if err != nil {
		t.Errorf("v1 client ecdh: %v", err)
		return
	}
	binding, err := exporter("phantom-handshake", nil, 32)
	if err != nil {
		t.Errorf("v1 client exporter: %v", err)
		return
	}
	crypto, err := protocol.DeriveSessionKeys(es, psk, clientPub, serverStaticPub, protocol.RoleClient)
	if err != nil {
		t.Errorf("v1 client derive: %v", err)
		return
	}

	// The v1 tag: HMAC over clientPub+binding, with no wire version mixed in.
	mac := hmac.New(sha256.New, crypto.AuthKey[:])
	mac.Write(clientPub)
	mac.Write(binding)
	tag := mac.Sum(nil)[:authTagSize]

	cookieValue := base64.RawURLEncoding.EncodeToString(append(append([]byte{}, clientPub...), tag...))
	capTok := make([]byte, 16)
	if _, err := rand.Read(capTok); err != nil {
		t.Errorf("v1 client rand: %v", err)
		return
	}
	wsKey := make([]byte, 16)
	if _, err := rand.Read(wsKey); err != nil {
		t.Errorf("v1 client rand: %v", err)
		return
	}

	req := fmt.Sprintf(
		"GET /ws HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Connection: Upgrade\r\n"+
			"Upgrade: websocket\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"Sec-WebSocket-Key: %s\r\n"+
			"Cookie: %s=%s; %s=%s\r\n"+
			"\r\n",
		domain, base64.StdEncoding.EncodeToString(wsKey),
		cookieName, cookieValue,
		eeCapCookie, base64.RawURLEncoding.EncodeToString(capTok),
	)
	if _, err := rw.Write([]byte(req)); err != nil {
		t.Errorf("v1 client write: %v", err)
	}
}

// TestV1ClientIsRejectedToDecoy is the version gate from the server's side: a
// client whose auth tag doesn't include the wire version must not authenticate,
// and must fall through to the decoy site rather than get an error or a reset -
// so to anyone probing, a version-mismatched client is indistinguishable from
// any other stranger, and an operator mid-upgrade sees a normal website.
func TestV1ClientIsRejectedToDecoy(t *testing.T) {
	serverPriv, serverPub := genServerKeypair(t)
	psk := []byte("0123456789abcdef0123456789abcdef")

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	go v1ClientRequest(t, clientConn, "example.com", psk, serverPub, fakeExporter(0x42))

	result, req, err := ServerHandshake(serverConn, psk, serverPriv, serverPub, fakeExporter(0x42))
	if err != nil {
		t.Fatalf("ServerHandshake() unexpected error = %v", err)
	}
	if result != nil {
		t.Fatal("a v1-shaped client authenticated against a v2 server - the version gate is not holding")
	}
	if req == nil {
		t.Fatal("expected the parsed request back for decoy fallback")
	}
	if req.Header.Get("Upgrade") != "websocket" {
		t.Errorf("expected the websocket-shaped request for the decoy, got Upgrade=%q", req.Header.Get("Upgrade"))
	}
}

// v1ServerResponse mimics a pre-v2 server that authenticates but sends no
// ephemeral key, i.e. one that would have left the tunnel on the semi-static key.
func v1ServerResponse(t *testing.T, rw readWriter) {
	t.Helper()
	br := bufio.NewReader(rw)
	req, err := http.ReadRequest(br)
	if err != nil {
		t.Errorf("v1 server read: %v", err)
		return
	}
	resp := fmt.Sprintf(
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n"+
			"\r\n",
		computeWebSocketAccept(req.Header.Get("Sec-WebSocket-Key")),
	)
	if _, err := rw.Write([]byte(resp)); err != nil {
		t.Errorf("v1 server write: %v", err)
	}
}

// TestClientRefusesServerWithoutEphemeralKey is the gate from the client's side.
// A server that answers 101 but sends no ephemeral key would have left the tunnel
// on the weaker semi-static key; the client must refuse outright instead of
// quietly downgrading, which is exactly what the old fallback did.
func TestClientRefusesServerWithoutEphemeralKey(t *testing.T) {
	_, serverPub := genServerKeypair(t)
	psk := []byte("0123456789abcdef0123456789abcdef")

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	go v1ServerResponse(t, serverConn)

	crypto, err := ClientHandshake(clientConn, "example.com", psk, serverPub, fakeExporter(0x42))
	if err == nil {
		t.Fatal("client accepted a server that sent no ephemeral key - it silently downgraded forward secrecy")
	}
	if crypto != nil {
		t.Fatal("client returned crypto state despite the failure")
	}
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("expected ErrAuthFailed so the caller treats it like any other auth failure, got %v", err)
	}
}

// TestEphemeralEphemeralIsAlwaysUsed checks the positive case: a normal v2
// handshake must always end up on an ee-derived key, with no path left that
// produces the static-only one.
func TestEphemeralEphemeralIsAlwaysUsed(t *testing.T) {
	serverPriv, serverPub := genServerKeypair(t)
	psk := []byte("0123456789abcdef0123456789abcdef")

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	type clientResult struct {
		crypto *protocol.SessionCrypto
		err    error
	}
	done := make(chan clientResult, 1)
	go func() {
		c, err := ClientHandshake(clientConn, "example.com", psk, serverPub, fakeExporter(0x42))
		done <- clientResult{c, err}
	}()

	result, _, err := ServerHandshake(serverConn, psk, serverPriv, serverPub, fakeExporter(0x42))
	if err != nil {
		t.Fatalf("ServerHandshake() error = %v", err)
	}
	if result == nil {
		t.Fatal("ServerHandshake() did not authenticate a current client")
	}

	res := <-done
	if res.err != nil {
		t.Fatalf("ClientHandshake() error = %v", res.err)
	}

	// Both ends must agree crosswise on the directional keys.
	if res.crypto.SendKey != result.Crypto.RecvKey {
		t.Error("client's send key does not match the server's receive key")
	}
	if result.Crypto.SendKey != res.crypto.RecvKey {
		t.Error("server's send key does not match the client's receive key")
	}

	// Each direction must genuinely differ - that's the directional split, and
	// jointly with the two negative tests above it pins that the session can only
	// have come through the mandatory ee path.
	if res.crypto.SendKey == res.crypto.RecvKey {
		t.Error("both directions share one key")
	}
}
