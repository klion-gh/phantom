package protocol

import (
	"bytes"
	"testing"
)

func TestDeriveSessionKeys(t *testing.T) {
	ecdh := []byte("0123456789abcdef0123456789abcdef")
	psk := []byte("shared-psk-bytes-1234567890abcdef")
	clientPub := []byte("client-ephemeral-pub-1234567890ab")
	serverPub := []byte("server-static-pub-1234567890abcd")

	keys1, err := DeriveSessionKeys(ecdh, psk, clientPub, serverPub)
	if err != nil {
		t.Fatalf("DeriveSessionKeys() error = %v", err)
	}

	keys2, err := DeriveSessionKeys(ecdh, psk, clientPub, serverPub)
	if err != nil {
		t.Fatalf("DeriveSessionKeys() error = %v", err)
	}

	if keys1.InnerKey != keys2.InnerKey {
		t.Error("InnerKey should be deterministic given the same inputs")
	}
	if keys1.AuthKey != keys2.AuthKey {
		t.Error("AuthKey should be deterministic given the same inputs")
	}

	// This is the crux of the forward-secrecy fix: a fresh ECDH shared secret
	// (as produced by a fresh ephemeral key each connection) must change the
	// derived key even when the long-term PSK is unchanged.
	keys3, err := DeriveSessionKeys([]byte("different-ecdh-secret-abcdefghij"), psk, clientPub, serverPub)
	if err != nil {
		t.Fatalf("DeriveSessionKeys() error = %v", err)
	}
	if keys1.InnerKey == keys3.InnerKey {
		t.Error("different ECDH shared secrets must produce different keys even with the same PSK")
	}

	keys4, err := DeriveSessionKeys(ecdh, []byte("different-psk-bytes-abcdefghijkl"), clientPub, serverPub)
	if err != nil {
		t.Fatalf("DeriveSessionKeys() error = %v", err)
	}
	if keys1.InnerKey == keys4.InnerKey {
		t.Error("different PSKs should produce different keys")
	}
}

func TestEncryptDecryptFrame(t *testing.T) {
	keys := testKeys(t)

	header := []byte{0x00, 0x00, 0x01, 0x00, 0x00, 0x0B}
	plaintext := []byte("hello world")

	ciphertext, err := keys.EncryptFrame(header, plaintext)
	if err != nil {
		t.Fatalf("EncryptFrame() error = %v", err)
	}

	if bytes.Equal(ciphertext, plaintext) {
		t.Error("Ciphertext should differ from plaintext")
	}

	decrypted, err := keys.DecryptFrame(header, ciphertext)
	if err != nil {
		t.Fatalf("DecryptFrame() error = %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("DecryptFrame() = %v, want %v", decrypted, plaintext)
	}
}

func TestEncryptFramePadsRandomlyWithinBand(t *testing.T) {
	keys := testKeys(t)
	header := []byte{0x00, 0x00, 0x01, 0x00, 0x00, 0x0B}

	// Encrypted frame body = XChaCha20 nonce (24) + padded plaintext + Poly1305
	// tag (16). A 1-byte and a 200-byte payload both land in the 256 bucket, so
	// both wire sizes fall in the same band and can't be told apart; and the
	// size varies run to run (randomized padding), so it's not a fixed value.
	const aead = 24 + 16
	const bucket = 256
	lo, hi := bucket+aead, bucket+maxPadJitter+aead

	sizes := map[int]bool{}
	for i := 0; i < 128; i++ {
		short, err := keys.EncryptFrame(header, []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		longer, err := keys.EncryptFrame(header, make([]byte, 200))
		if err != nil {
			t.Fatal(err)
		}
		for _, ln := range []int{len(short), len(longer)} {
			if ln < lo || ln > hi {
				t.Fatalf("wire size %d outside the shared band [%d,%d] - magnitude leaked", ln, lo, hi)
			}
		}
		sizes[len(short)] = true
	}
	if len(sizes) < 2 {
		t.Error("wire size is not randomized")
	}
}

func TestEncryptFrameNonceIncrement(t *testing.T) {
	keys := testKeys(t)

	header := []byte{0x00, 0x00, 0x01, 0x00, 0x00, 0x0B}
	plaintext := []byte("hello world")

	ct1, err := keys.EncryptFrame(header, plaintext)
	if err != nil {
		t.Fatalf("EncryptFrame() error = %v", err)
	}

	ct2, err := keys.EncryptFrame(header, plaintext)
	if err != nil {
		t.Fatalf("EncryptFrame() error = %v", err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Error("Same plaintext should produce different ciphertext due to nonce increment")
	}
}

func TestDecryptFrameTampered(t *testing.T) {
	keys := testKeys(t)

	header := []byte{0x00, 0x00, 0x01, 0x00, 0x00, 0x0B}
	plaintext := []byte("hello world")

	ciphertext, err := keys.EncryptFrame(header, plaintext)
	if err != nil {
		t.Fatalf("EncryptFrame() error = %v", err)
	}

	ciphertext[len(ciphertext)-1] ^= 0xFF

	_, err = keys.DecryptFrame(header, ciphertext)
	if err == nil {
		t.Error("DecryptFrame() should fail on tampered ciphertext")
	}
}

func testKeys(t *testing.T) *SessionCrypto {
	t.Helper()
	keys, err := DeriveSessionKeys(
		[]byte("0123456789abcdef0123456789abcdef"),
		[]byte("shared-psk-bytes-1234567890abcdef"),
		[]byte("client-ephemeral-pub-1234567890ab"),
		[]byte("server-static-pub-1234567890abcd"),
	)
	if err != nil {
		t.Fatalf("DeriveSessionKeys() error = %v", err)
	}
	return keys
}
