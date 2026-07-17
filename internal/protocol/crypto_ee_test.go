package protocol

import (
	"bytes"
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func genKeypair(t *testing.T) (priv, pub []byte) {
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

// TestDeriveInnerKeyEE verifies the ephemeral-ephemeral tunnel key is (a) the
// same whether derived from the client's or the server's side of the ee ECDH,
// and (b) genuinely different from the static-only InnerKey - i.e. mixing ee in
// actually changes the key, so the forward-secrecy upgrade isn't a no-op.
func TestDeriveInnerKeyEE(t *testing.T) {
	clientPriv, clientPub := genKeypair(t)
	serverStaticPriv, serverStaticPub := genKeypair(t)
	serverEphPriv, serverEphPub := genKeypair(t)
	psk := []byte("0123456789abcdef0123456789abcdef")

	es, err := curve25519.X25519(clientPriv, serverStaticPub)
	if err != nil {
		t.Fatal(err)
	}
	// The same es the server computes the other way.
	esServer, err := curve25519.X25519(serverStaticPriv, clientPub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(es, esServer) {
		t.Fatal("es mismatch between client and server derivation")
	}

	eeClient, err := curve25519.X25519(clientPriv, serverEphPub)
	if err != nil {
		t.Fatal(err)
	}
	eeServer, err := curve25519.X25519(serverEphPriv, clientPub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(eeClient, eeServer) {
		t.Fatal("ee mismatch between client and server derivation")
	}

	keyFromClient, err := DeriveInnerKeyEE(es, eeClient, psk, clientPub, serverStaticPub, serverEphPub)
	if err != nil {
		t.Fatal(err)
	}
	keyFromServer, err := DeriveInnerKeyEE(esServer, eeServer, psk, clientPub, serverStaticPub, serverEphPub)
	if err != nil {
		t.Fatal(err)
	}
	if keyFromClient != keyFromServer {
		t.Error("client and server derived different ephemeral-ephemeral InnerKeys")
	}

	staticCrypto, err := DeriveSessionKeys(es, psk, clientPub, serverStaticPub)
	if err != nil {
		t.Fatal(err)
	}
	if keyFromClient == staticCrypto.InnerKey {
		t.Error("ephemeral-ephemeral InnerKey equals the static-only InnerKey - ee not mixed in")
	}

	// The auth key is derived from es only and must be identical regardless of
	// the ephemeral-ephemeral step (that's what keeps auth interop-compatible).
	staticCrypto2, err := DeriveSessionKeys(es, psk, clientPub, serverStaticPub)
	if err != nil {
		t.Fatal(err)
	}
	if staticCrypto.AuthKey != staticCrypto2.AuthKey {
		t.Error("AuthKey derivation is not deterministic")
	}
}
