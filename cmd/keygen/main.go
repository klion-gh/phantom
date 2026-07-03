package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// Generates the server's long-term X25519 keypair (used for a real ECDH key
// exchange with each client's fresh ephemeral key - see internal/handshake)
// plus a random PSK (a second, symmetric factor mixed into key derivation
// alongside the ECDH secret). Unlike v1's keygen, which computed a real X25519
// keypair but then fed the "public key" into HKDF as a flat, never-exchanged
// symmetric PSK, every value printed here is used exactly as its name says.
func main() {
	serverPriv := make([]byte, 32)
	if _, err := rand.Read(serverPriv); err != nil {
		panic(err)
	}
	serverPriv[0] &= 248
	serverPriv[31] &= 127
	serverPriv[31] |= 64

	serverPub, err := curve25519.X25519(serverPriv, curve25519.Basepoint)
	if err != nil {
		panic(err)
	}

	psk := make([]byte, 32)
	if _, err := rand.Read(psk); err != nil {
		panic(err)
	}

	fmt.Println("=== Phantom Key Material ===")
	fmt.Println()
	fmt.Printf("Server Private Key: %s\n", hex.EncodeToString(serverPriv))
	fmt.Printf("Server Public Key:  %s\n", hex.EncodeToString(serverPub))
	fmt.Printf("PSK:                %s\n", hex.EncodeToString(psk))
	fmt.Println()
	fmt.Println("=== server.yaml ===")
	fmt.Printf("private_key: \"%s\"\n", hex.EncodeToString(serverPriv))
	fmt.Printf("psk: \"%s\"\n", hex.EncodeToString(psk))
	fmt.Println()
	fmt.Println("=== client.yaml ===")
	fmt.Printf("server_public_key: \"%s\"\n", hex.EncodeToString(serverPub))
	fmt.Printf("psk: \"%s\"\n", hex.EncodeToString(psk))
	fmt.Println()
	fmt.Println("Server Private Key stays on the server only. Server Public Key and PSK")
	fmt.Println("both go to every client - PSK must be identical on client and server.")
}
