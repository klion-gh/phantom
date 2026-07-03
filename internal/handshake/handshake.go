// Package handshake replaces v1's bare "AUTH frame sent as the first bytes
// after the TLS handshake" (a distinctive, fixed-size, high-entropy signature
// no real browser produces). Instead, right after the real outer TLS 1.3
// handshake completes, the client sends something that parses as an entirely
// ordinary HTTP/1.1 WebSocket upgrade request - a very common, unremarkable
// shape in real HTTPS traffic (this is the same disguise VLESS+WS uses). The
// actual authentication and key exchange is smuggled inside a cookie value.
//
// Authentication is a fresh X25519 ECDH between the client's per-connection
// ephemeral keypair and the server's long-term static keypair (the same
// mechanism XTLS "Reality" uses for forward secrecy - see PROTOCOL.md), mixed
// with a long-term PSK via HKDF-SHA256, and bound to the specific outer TLS
// connection via TLS 1.3 exported keying material (RFC 5705) so a captured
// cookie value cannot be replayed on a different connection.
//
// A connection that fails or skips this check is not dropped - the caller is
// expected to hand it to internal/transport's decoy site instead, so a
// prober sees a normal website with a normal certificate, never a timeout or
// reset (see PROTOCOL.md's discussion of Trojan's fallback design).
package handshake

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/crypto/curve25519"

	"phantom/internal/protocol"
)

const (
	wsMagicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	cookieName  = "session"
	authTagSize = 16
	// A handful of plausible-looking paths a real small site might expose a
	// websocket endpoint on; the client picks one per connection so repeated
	// connections don't all hit the exact same literal path.
)

var decoyPaths = []string{"/ws", "/socket", "/api/live", "/updates"}

// ExportKeyingMaterial matches the shape of (*tls.ConnectionState).ExportKeyingMaterial
// and uTLS's equivalent, decoupling this package from which TLS stack is in use.
type ExportKeyingMaterial func(label string, context []byte, length int) ([]byte, error)

var ErrAuthFailed = errors.New("handshake: auth failed or not present")

// ClientHandshake performs the disguised handshake over an already-established
// outer TLS connection (conn) and returns the derived SessionCrypto for the
// tunnel that follows. domain is used as the Host header.
func ClientHandshake(rw readWriter, domain string, psk, serverStaticPub []byte, exporter ExportKeyingMaterial) (*protocol.SessionCrypto, error) {
	clientPriv := make([]byte, 32)
	if _, err := rand.Read(clientPriv); err != nil {
		return nil, err
	}
	clientPriv[0] &= 248
	clientPriv[31] &= 127
	clientPriv[31] |= 64

	clientPub, err := curve25519.X25519(clientPriv, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}

	ecdhSecret, err := curve25519.X25519(clientPriv, serverStaticPub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}

	binding, err := exporter("phantom-handshake", nil, 32)
	if err != nil {
		return nil, fmt.Errorf("tls exporter: %w", err)
	}

	crypto, err := protocol.DeriveSessionKeys(ecdhSecret, psk, clientPub, serverStaticPub)
	if err != nil {
		return nil, err
	}

	tag := computeAuthTag(crypto.AuthKey[:], clientPub, binding)

	cookieValue := base64.RawURLEncoding.EncodeToString(append(append([]byte{}, clientPub...), tag...))

	wsKey := make([]byte, 16)
	if _, err := rand.Read(wsKey); err != nil {
		return nil, err
	}
	wsKeyB64 := base64.StdEncoding.EncodeToString(wsKey)

	path := decoyPaths[int(clientPriv[0])%len(decoyPaths)]

	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Connection: Upgrade\r\n"+
			"Upgrade: websocket\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"Sec-WebSocket-Key: %s\r\n"+
			"Cookie: %s=%s\r\n"+
			"\r\n",
		path, domain, wsKeyB64, cookieName, cookieValue,
	)

	if _, err := rw.Write([]byte(req)); err != nil {
		return nil, fmt.Errorf("write handshake request: %w", err)
	}

	br := bufio.NewReader(rw)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, fmt.Errorf("read handshake response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("%w: server returned %d instead of 101", ErrAuthFailed, resp.StatusCode)
	}

	expectedAccept := computeWebSocketAccept(wsKeyB64)
	if resp.Header.Get("Sec-WebSocket-Accept") != expectedAccept {
		return nil, fmt.Errorf("%w: unexpected Sec-WebSocket-Accept", ErrAuthFailed)
	}

	return crypto, nil
}

// ServerResult is returned by ServerHandshake on a successful, authenticated
// handshake (the 101 response has already been written to the connection).
type ServerResult struct {
	Crypto *protocol.SessionCrypto
}

// ServerHandshake reads and validates the disguised handshake request.
//
//   - On success: writes the 101 Switching Protocols response itself and
//     returns a non-nil *ServerResult. The connection is now the tunnel.
//   - On a validly-parsed HTTP request whose embedded auth is missing/wrong:
//     returns (nil, req, nil) - the caller should serve the decoy site using
//     req, since nothing has been written to the connection yet.
//   - If the input can't even be parsed as an HTTP request: returns
//     (nil, nil, err) - the caller can still fall back to a generic decoy
//     response, or just close the connection.
func ServerHandshake(rw readWriter, psk, serverPriv, serverPub []byte, exporter ExportKeyingMaterial) (*ServerResult, *http.Request, error) {
	br := bufio.NewReader(rw)
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil, nil, fmt.Errorf("read handshake request: %w", err)
	}

	cookie, err := req.Cookie(cookieName)
	if err != nil {
		return nil, req, nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) != 32+authTagSize {
		return nil, req, nil
	}
	clientPub := raw[:32]
	tag := raw[32:]

	ecdhSecret, err := curve25519.X25519(serverPriv, clientPub)
	if err != nil {
		return nil, req, nil
	}

	crypto, err := protocol.DeriveSessionKeys(ecdhSecret, psk, clientPub, serverPub)
	if err != nil {
		return nil, req, nil
	}

	binding, err := exporter("phantom-handshake", nil, 32)
	if err != nil {
		return nil, req, nil
	}

	expectedTag := computeAuthTag(crypto.AuthKey[:], clientPub, binding)
	if !hmac.Equal(tag, expectedTag) {
		return nil, req, nil
	}

	wsKey := req.Header.Get("Sec-WebSocket-Key")
	accept := computeWebSocketAccept(wsKey)

	resp := fmt.Sprintf(
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n"+
			"\r\n",
		accept,
	)
	if _, err := rw.Write([]byte(resp)); err != nil {
		return nil, nil, fmt.Errorf("write handshake response: %w", err)
	}

	return &ServerResult{Crypto: crypto}, nil, nil
}

func computeAuthTag(authKey, clientPub, binding []byte) []byte {
	// The HKDF-derived AuthKey already mixes in the ECDH secret and PSK,
	// so a tag over clientPub+binding proves the sender both completed the
	// real ECDH (implicitly, by having derived a matching AuthKey - only
	// someone who did the ECDH with the right keys lands on the same key)
	// and is bound to this specific TLS connection (via binding), so a
	// captured cookie can't be replayed on a different connection.
	mac := hmac.New(sha256.New, authKey)
	mac.Write(clientPub)
	mac.Write(binding)
	full := mac.Sum(nil)
	return full[:authTagSize]
}

func computeWebSocketAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(wsMagicGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// readWriter is the minimal surface handshake needs from the underlying TLS
// connection - satisfied by both *tls.Conn and uTLS's *utls.UConn.
type readWriter interface {
	io.Reader
	io.Writer
}
