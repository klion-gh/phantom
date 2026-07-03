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

func TestEncryptFrameHidesPlaintextLength(t *testing.T) {
	keys := testKeys(t)
	header := []byte{0x00, 0x00, 0x01, 0x00, 0x00, 0x0B}

	short, err := keys.EncryptFrame(header, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	longer, err := keys.EncryptFrame(header, make([]byte, 200))
	if err != nil {
		t.Fatal(err)
	}

	if len(short) != len(longer) {
		t.Errorf("wire sizes should match for payloads in the same padding bucket: got %d and %d", len(short), len(longer))
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
