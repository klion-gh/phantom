package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

type SessionCrypto struct {
	InnerKey   [32]byte
	AuthKey    [32]byte
	FrameNonce uint64
}

// DeriveSessionKeys derives per-session keys from a freshly computed X25519
// ECDH shared secret (ephemeral client key x server's static key - see
// internal/handshake), mixed with the long-term PSK and both public keys.
//
// Unlike v1 (which called this with the ECDH inputs always empty, making the
// key 100% static/derived from the PSK alone with no forward secrecy), the
// ecdhSharedSecret here is different on every single connection because the
// client generates a fresh ephemeral X25519 keypair each time it connects.
// A compromise of the long-term PSK alone is no longer sufficient to decrypt
// a captured session - the attacker would also need that connection's
// ephemeral private key, which the client never persists.
func DeriveSessionKeys(ecdhSharedSecret, psk, clientEphemeralPub, serverStaticPub []byte) (*SessionCrypto, error) {
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
	copy(sc.InnerKey[:], innerKey)

	authKey, err := hkdfExpand(material, []byte("Phantom-auth-key"), 32)
	if err != nil {
		return nil, err
	}
	copy(sc.AuthKey[:], authKey)

	return sc, nil
}

func hkdfExpand(secret, info []byte, length int) ([]byte, error) {
	hkdfReader := hkdf.New(sha256.New, secret, nil, info)
	out := make([]byte, length)
	if _, err := io.ReadFull(hkdfReader, out); err != nil {
		return nil, err
	}
	return out, nil
}

// EncryptFrame pads plaintext to a fixed bucket size (see PadPlaintext) before
// sealing it, so the wire size of a DATA frame never reveals the real payload
// size - this is applied here, transparently to every caller (in particular
// internal/tunnel/multiplexer.go, ported unchanged from v1, has no idea
// padding happens at all).
func (sc *SessionCrypto) EncryptFrame(header, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(sc.InnerKey[:])
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
	return append(nonce, ciphertext...), nil
}

func (sc *SessionCrypto) DecryptFrame(header, data []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(sc.InnerKey[:])
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
