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
	// eeCapCookie is a second cookie the client sends (with a random value)
	// purely to signal it supports the ephemeral-ephemeral upgrade (§5.1). Its
	// presence - not its value - is the signal; an old server ignores it and
	// stays on the static-only key, a new server responds with its ephemeral
	// public key. Named/shaped like an ordinary companion cookie (session +
	// csrf is a very common pair) so it adds no distinctive fingerprint, and
	// its random value means it isn't a constant tell either.
	eeCapCookie = "csrf"
	// eeServerCookie is the cookie the server sets in its 101 response carrying
	// its per-connection ephemeral public key (base64url), when the client
	// signaled ee support. Servers setting a cookie in a response is entirely
	// ordinary; an old client ignores it.
	eeServerCookie = "sid"
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
	clientPriv, clientPub, err := genEphemeral()
	if err != nil {
		return nil, err
	}

	// es = client ephemeral x server static. Authenticates the client (only
	// someone who knows the PSK and completed this ECDH lands on the same
	// AuthKey) and, unchanged from before, is the basis for the tunnel key when
	// the server doesn't do the ephemeral-ephemeral upgrade.
	es, err := curve25519.X25519(clientPriv, serverStaticPub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}

	binding, err := exporter("phantom-handshake", nil, 32)
	if err != nil {
		return nil, fmt.Errorf("tls exporter: %w", err)
	}

	crypto, err := protocol.DeriveSessionKeys(es, psk, clientPub, serverStaticPub)
	if err != nil {
		return nil, err
	}

	tag := computeAuthTag(crypto.AuthKey[:], clientPub, binding)

	cookieValue := base64.RawURLEncoding.EncodeToString(append(append([]byte{}, clientPub...), tag...))

	// Random companion-cookie value; presence signals ephemeral-ephemeral
	// support (see eeCapCookie).
	capTok := make([]byte, 16)
	if _, err := rand.Read(capTok); err != nil {
		return nil, err
	}
	capValue := base64.RawURLEncoding.EncodeToString(capTok)

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
			"Cookie: %s=%s; %s=%s\r\n"+
			"\r\n",
		path, domain, wsKeyB64, cookieName, cookieValue, eeCapCookie, capValue,
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

	// If the server included its ephemeral public key, upgrade the tunnel key to
	// ephemeral-ephemeral (full forward secrecy). If not, it's an older server -
	// keep the static-only key DeriveSessionKeys already produced.
	if serverEphPub := readServerEphPub(resp); serverEphPub != nil {
		ee, err := curve25519.X25519(clientPriv, serverEphPub)
		if err != nil {
			return nil, fmt.Errorf("ee ecdh: %w", err)
		}
		innerKey, err := protocol.DeriveInnerKeyEE(es, ee, psk, clientPub, serverStaticPub, serverEphPub)
		if err != nil {
			return nil, err
		}
		crypto.InnerKey = innerKey
	}

	return crypto, nil
}

// readServerEphPub extracts the server's ephemeral public key from the 101
// response's Set-Cookie (see eeServerCookie), or nil if absent/malformed (an
// older server, or a client that didn't ask for the upgrade).
func readServerEphPub(resp *http.Response) []byte {
	for _, c := range resp.Cookies() {
		if c.Name != eeServerCookie {
			continue
		}
		pub, err := base64.RawURLEncoding.DecodeString(c.Value)
		if err != nil || len(pub) != 32 {
			return nil
		}
		return pub
	}
	return nil
}

func genEphemeral() (priv, pub []byte, err error) {
	priv = make([]byte, 32)
	if _, err = rand.Read(priv); err != nil {
		return nil, nil, err
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err = curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
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

	// If the client signaled ephemeral-ephemeral support (see eeCapCookie),
	// generate a per-connection ephemeral keypair, upgrade the tunnel key to
	// mix in the ephemeral-ephemeral secret, and hand the client our ephemeral
	// public key in a Set-Cookie header so it can derive the same key. A client
	// that didn't ask (older client) gets the unchanged static-only key and no
	// extra header. crypto.AuthKey is untouched either way.
	var extraHeader string
	if _, capErr := req.Cookie(eeCapCookie); capErr == nil {
		serverEphPriv, serverEphPub, genErr := genEphemeral()
		if genErr != nil {
			return nil, nil, fmt.Errorf("server ephemeral: %w", genErr)
		}
		ee, eeErr := curve25519.X25519(serverEphPriv, clientPub)
		if eeErr != nil {
			return nil, nil, fmt.Errorf("ee ecdh: %w", eeErr)
		}
		innerKey, deriveErr := protocol.DeriveInnerKeyEE(ecdhSecret, ee, psk, clientPub, serverPub, serverEphPub)
		if deriveErr != nil {
			return nil, nil, deriveErr
		}
		crypto.InnerKey = innerKey
		extraHeader = fmt.Sprintf("Set-Cookie: %s=%s; Path=/; HttpOnly\r\n",
			eeServerCookie, base64.RawURLEncoding.EncodeToString(serverEphPub))
	}

	wsKey := req.Header.Get("Sec-WebSocket-Key")
	accept := computeWebSocketAccept(wsKey)

	resp := fmt.Sprintf(
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n"+
			"%s"+
			"\r\n",
		accept, extraHeader,
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
