package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// WireVersion identifies the frame/handshake format this build speaks. It is
// mixed into the handshake's auth tag (see internal/handshake), so two peers on
// different versions simply fail to authenticate each other and the connection
// lands on the decoy site - a clean, diagnosable outcome instead of two sides
// disagreeing about how to parse frames.
//
// v2 changed, all at once, three things that could only be fixed by breaking the
// format:
//
//   - FrameOpen's payload (the destination host:port) is encrypted. In v1 only
//     DATA frames were, so the inner encryption layer protected the traffic but
//     published every destination in the clear the moment the outer TLS was
//     terminated or its key compromised.
//   - The AEAD's additional data covers the frame's type and flags, not just its
//     stream id, so the plaintext header fields can't be altered undetected.
//   - Each direction gets its own key (see SetInnerKey), instead of both sides
//     encrypting with one shared key and independent counters.
//
// v2 also makes the ephemeral-ephemeral exchange mandatory rather than
// negotiated, so there is no longer a semi-static fallback with weaker forward
// secrecy for one side to be talked into.
const WireVersion uint16 = 2

// Role is which end of the tunnel a SessionCrypto belongs to. It selects which
// of the two directional keys is used for sending and which for receiving.
type Role int

const (
	RoleClient Role = iota
	RoleServer
)

type SessionCrypto struct {
	// SendKey and RecvKey are the two directional tunnel keys; which is which
	// depends on Role (see SetInnerKey). Previously a single InnerKey served both
	// directions, with each side running its own counter from zero over the same
	// key - safe in practice only because 16 of the 24 nonce bytes are random,
	// and it left no room to ever reject a replayed frame, since a frame from
	// either direction decrypted fine in the other.
	SendKey [32]byte
	RecvKey [32]byte

	AuthKey    [32]byte
	FrameNonce uint64
}

// DeriveSessionKeys derives per-session keys from a freshly computed X25519
// ECDH shared secret (ephemeral client key x server's static key - see
// internal/handshake), mixed with the long-term PSK and both public keys.
//
// The ecdhSharedSecret here is different on every single connection because the
// client generates a fresh ephemeral X25519 keypair each time it connects. A
// compromise of the long-term PSK alone is not sufficient to decrypt a captured
// session - the attacker would also need that connection's ephemeral private
// key, which the client never persists.
//
// The tunnel keys this installs are provisional: the handshake always follows up
// with SetInnerKey once the ephemeral-ephemeral secret is known (mandatory as of
// WireVersion 2). What survives from here unchanged is AuthKey.
func DeriveSessionKeys(ecdhSharedSecret, psk, clientEphemeralPub, serverStaticPub []byte, role Role) (*SessionCrypto, error) {
	sc := &SessionCrypto{}

	material := make([]byte, 0, len(ecdhSharedSecret)+len(psk)+len(clientEphemeralPub)+len(serverStaticPub))
	material = append(material, ecdhSharedSecret...)
	material = append(material, psk...)
	material = append(material, clientEphemeralPub...)
	material = append(material, serverStaticPub...)

	innerKey, err := hkdfExpand(material, []byte("Phantom-inner-encryption"), 32)
	if err != nil {
		return nil, err
	}
	if err := sc.SetInnerKey(innerKey, role); err != nil {
		return nil, err
	}

	authKey, err := hkdfExpand(material, []byte("Phantom-auth-key"), 32)
	if err != nil {
		return nil, err
	}
	copy(sc.AuthKey[:], authKey)

	return sc, nil
}

// DeriveInnerKeyEE derives the base tunnel key for an ephemeral-ephemeral
// handshake, mixing the ephemeral-ephemeral ECDH secret (ee = clientEph x
// serverEph) in addition to the ephemeral-static one (es = clientEph x
// serverStatic) that DeriveSessionKeys used. This closes the semi-static
// forward-secrecy gap: with es alone, a future compromise of the server's
// long-term private key plus recorded traffic would decrypt past sessions;
// adding ee means an attacker would also need one of the two ephemeral private
// keys, which neither side ever persists.
//
// Pass the result to SetInnerKey, which splits it per direction. AuthKey stays
// whatever DeriveSessionKeys produced from es, so authentication is unaffected.
func DeriveInnerKeyEE(es, ee, psk, clientPub, serverStaticPub, serverEphPub []byte) ([]byte, error) {
	material := make([]byte, 0, len(es)+len(ee)+len(psk)+len(clientPub)+len(serverStaticPub)+len(serverEphPub))
	material = append(material, es...)
	material = append(material, ee...)
	material = append(material, psk...)
	material = append(material, clientPub...)
	material = append(material, serverStaticPub...)
	material = append(material, serverEphPub...)

	return hkdfExpand(material, []byte("Phantom-inner-encryption-ee"), 32)
}

// SetInnerKey installs base as the tunnel key, splitting it into two independent
// directional keys and assigning them according to role.
//
// One key per direction means a frame the client sent can never be decrypted by
// another client-side reader (no reflection), the two sides' nonce counters live
// in separate keyspaces so their reuse can't collide at all, and a future
// receive-side replay check becomes possible - none of which held when both
// directions shared one key.
func (sc *SessionCrypto) SetInnerKey(base []byte, role Role) error {
	c2s, err := hkdfExpand(base, []byte("Phantom-inner-c2s"), 32)
	if err != nil {
		return err
	}
	s2c, err := hkdfExpand(base, []byte("Phantom-inner-s2c"), 32)
	if err != nil {
		return err
	}

	switch role {
	case RoleClient:
		copy(sc.SendKey[:], c2s)
		copy(sc.RecvKey[:], s2c)
	case RoleServer:
		copy(sc.SendKey[:], s2c)
		copy(sc.RecvKey[:], c2s)
	default:
		return fmt.Errorf("unknown role %d", role)
	}
	return nil
}

func hkdfExpand(secret, info []byte, length int) ([]byte, error) {
	hkdfReader := hkdf.New(sha256.New, secret, nil, info)
	out := make([]byte, length)
	if _, err := io.ReadFull(hkdfReader, out); err != nil {
		return nil, err
	}
	return out, nil
}

// EncryptFrame seals a frame body with this side's sending key. header is the
// additional data the ciphertext is bound to - build it with FrameAAD so the
// frame's type and flags are covered, not just its stream id.
//
// Plaintext is padded to hide its real length (see PadPlaintext) transparently to
// every caller.
func (sc *SessionCrypto) EncryptFrame(header, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(sc.SendKey[:])
	if err != nil {
		return nil, err
	}

	padded, err := PadPlaintext(plaintext)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, aead.NonceSize())
	binary.BigEndian.PutUint64(nonce[:8], sc.FrameNonce)
	sc.FrameNonce++
	if _, err := rand.Read(nonce[8:]); err != nil {
		return nil, err
	}

	ciphertext := aead.Seal(nil, nonce, padded, header)
	body := append(nonce, ciphertext...)
	if len(body) > MaxFramePayload {
		// Unreachable given PadPlaintext's own limit; a guard rather than a
		// silently truncated length field if that ever stops holding.
		return nil, fmt.Errorf("sealed frame body %d exceeds maximum %d", len(body), MaxFramePayload)
	}
	return body, nil
}

// DecryptFrame opens a frame body with this side's receiving key.
func (sc *SessionCrypto) DecryptFrame(header, data []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(sc.RecvKey[:])
	if err != nil {
		return nil, err
	}

	nonceSize := aead.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce := data[:nonceSize]
	ciphertext := data[nonceSize:]

	padded, err := aead.Open(nil, nonce, ciphertext, header)
	if err != nil {
		return nil, err
	}

	return UnpadPlaintext(padded)
}
