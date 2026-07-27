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

	keys1, err := DeriveSessionKeys(ecdh, psk, clientPub, serverPub, RoleClient)
	if err != nil {
		t.Fatalf("DeriveSessionKeys() error = %v", err)
	}

	keys2, err := DeriveSessionKeys(ecdh, psk, clientPub, serverPub, RoleClient)
	if err != nil {
		t.Fatalf("DeriveSessionKeys() error = %v", err)
	}

	if keys1.SendKey != keys2.SendKey || keys1.RecvKey != keys2.RecvKey {
		t.Error("tunnel keys should be deterministic given the same inputs")
	}
	if keys1.AuthKey != keys2.AuthKey {
		t.Error("AuthKey should be deterministic given the same inputs")
	}

	// This is the crux of the forward-secrecy fix: a fresh ECDH shared secret
	// (as produced by a fresh ephemeral key each connection) must change the
	// derived key even when the long-term PSK is unchanged.
	keys3, err := DeriveSessionKeys([]byte("different-ecdh-secret-abcdefghij"), psk, clientPub, serverPub, RoleClient)
	if err != nil {
		t.Fatalf("DeriveSessionKeys() error = %v", err)
	}
	if keys1.SendKey == keys3.SendKey {
		t.Error("different ECDH shared secrets must produce different keys even with the same PSK")
	}

	keys4, err := DeriveSessionKeys(ecdh, []byte("different-psk-bytes-abcdefghijkl"), clientPub, serverPub, RoleClient)
	if err != nil {
		t.Fatalf("DeriveSessionKeys() error = %v", err)
	}
	if keys1.SendKey == keys4.SendKey {
		t.Error("different PSKs should produce different keys")
	}
}

// TestDirectionalKeysMirror pins the directional key split: the two ends must
// agree crosswise (what one sends, the other receives) and the two directions
// must use genuinely different keys, so a frame can never be decrypted by a
// reader on the same side that sent it.
func TestDirectionalKeysMirror(t *testing.T) {
	client, server := testPair(t)

	if client.SendKey != server.RecvKey {
		t.Error("client's send key must equal the server's receive key")
	}
	if server.SendKey != client.RecvKey {
		t.Error("server's send key must equal the client's receive key")
	}
	if client.SendKey == client.RecvKey {
		t.Error("the two directions share one key - reflection is possible and per-direction replay checks impossible")
	}
}

// TestFrameFromOneSideIsNotReadableBySameSide is the practical consequence: a
// frame the client sealed must not open with the client's own receiving key.
// Before the split both directions shared one key, so it would have.
func TestFrameFromOneSideIsNotReadableBySameSide(t *testing.T) {
	client, server := testPair(t)
	aad := FrameAAD(FrameData, 0, 7)

	sealed, err := client.EncryptFrame(aad, []byte("secret payload"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.DecryptFrame(aad, sealed); err == nil {
		t.Fatal("a client-sent frame opened with the client's own receive key - directions are not separated")
	}
	if _, err := server.DecryptFrame(aad, sealed); err != nil {
		t.Fatalf("the server could not open a client-sent frame: %v", err)
	}
}

// TestAADCoversTypeAndFlags checks the widened additional data: relabelling a
// frame's type or flipping its flags must break authentication, not pass
// silently as it did when only the stream id was covered.
func TestAADCoversTypeAndFlags(t *testing.T) {
	client, server := testPair(t)

	sealed, err := client.EncryptFrame(FrameAAD(FrameOpen, 0, 7), []byte("example.com:443"))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		aad  []byte
	}{
		{"type relabelled OPEN->DATA", FrameAAD(FrameData, 0, 7)},
		{"FlagUDP set", FrameAAD(FrameOpen, FlagUDP, 7)},
		{"stream id changed", FrameAAD(FrameOpen, 0, 8)},
	} {
		if _, err := server.DecryptFrame(tc.aad, sealed); err == nil {
			t.Errorf("%s: decryption succeeded, header field is not authenticated", tc.name)
		}
	}

	if _, err := server.DecryptFrame(FrameAAD(FrameOpen, 0, 7), sealed); err != nil {
		t.Fatalf("unmodified header failed to verify: %v", err)
	}
}

func TestEncryptDecryptFrame(t *testing.T) {
	client, server := testPair(t)

	header := FrameAAD(FrameData, 0, 1)
	plaintext := []byte("hello world")

	ciphertext, err := client.EncryptFrame(header, plaintext)
	if err != nil {
		t.Fatalf("EncryptFrame() error = %v", err)
	}

	if bytes.Equal(ciphertext, plaintext) {
		t.Error("Ciphertext should differ from plaintext")
	}

	decrypted, err := server.DecryptFrame(header, ciphertext)
	if err != nil {
		t.Fatalf("DecryptFrame() error = %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("DecryptFrame() = %v, want %v", decrypted, plaintext)
	}
}

func TestEncryptFramePadsRandomlyWithinBand(t *testing.T) {
	keys := testKeys(t)
	header := FrameAAD(FrameData, 0, 1)

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

	header := FrameAAD(FrameData, 0, 1)
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
	client, server := testPair(t)

	header := FrameAAD(FrameData, 0, 1)
	plaintext := []byte("hello world")

	ciphertext, err := client.EncryptFrame(header, plaintext)
	if err != nil {
		t.Fatalf("EncryptFrame() error = %v", err)
	}

	ciphertext[len(ciphertext)-1] ^= 0xFF

	_, err = server.DecryptFrame(header, ciphertext)
	if err == nil {
		t.Error("DecryptFrame() should fail on tampered ciphertext")
	}
}

// testKeys is one end of a session, for tests that only seal.
func testKeys(t *testing.T) *SessionCrypto {
	t.Helper()
	client, _ := testPair(t)
	return client
}

// testPair builds both ends of one session. Sealing and opening now need
// opposite roles, since each direction has its own key.
func testPair(t *testing.T) (client, server *SessionCrypto) {
	t.Helper()
	const (
		ecdh      = "0123456789abcdef0123456789abcdef"
		psk       = "shared-psk-bytes-1234567890abcdef"
		clientPub = "client-ephemeral-pub-1234567890ab"
		serverPub = "server-static-pub-1234567890abcd"
	)
	client, err := DeriveSessionKeys([]byte(ecdh), []byte(psk), []byte(clientPub), []byte(serverPub), RoleClient)
	if err != nil {
		t.Fatalf("DeriveSessionKeys(client) error = %v", err)
	}
	server, err = DeriveSessionKeys([]byte(ecdh), []byte(psk), []byte(clientPub), []byte(serverPub), RoleServer)
	if err != nil {
		t.Fatalf("DeriveSessionKeys(server) error = %v", err)
	}
	return client, server
}
