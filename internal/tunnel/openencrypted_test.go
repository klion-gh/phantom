package tunnel

import (
	"bytes"
	"net"
	"testing"
	"time"

	"phantom/internal/protocol"
)

// TestOpenFrameTargetIsNotOnTheWire is the regression test for the plaintext
// destination: FrameOpen's payload is the host:port a stream is heading to, and
// in WireVersion 1 it was written to the tunnel unencrypted while DATA frames
// around it were sealed. Anything that terminated the outer TLS - a middlebox
// with its root installed, a compromised certificate key - recovered a complete
// browsing history without reading a byte of content.
//
// Reads the raw bytes a multiplexer puts on its connection and asserts the
// hostname doesn't appear in them.
func TestOpenFrameTargetIsNotOnTheWire(t *testing.T) {
	const secretHost = "very-distinctive-destination.example"
	const target = secretHost + ":443"

	client, raw := net.Pipe()
	defer client.Close()
	defer raw.Close()

	crypto, err := protocol.DeriveSessionKeys(
		[]byte("0123456789abcdef0123456789abcdef"),
		[]byte("shared-psk-bytes-1234567890abcdef"),
		[]byte("client-ephemeral-pub-1234567890ab"),
		[]byte("server-static-pub-1234567890abcd"),
		protocol.RoleClient,
	)
	if err != nil {
		t.Fatal(err)
	}

	m := NewMultiplexer(client, crypto)
	defer m.Close()

	// Collect whatever the multiplexer writes while opening a stream.
	captured := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 8192)
		n, err := raw.Read(buf)
		if err != nil {
			captured <- nil
			return
		}
		captured <- buf[:n]
	}()

	go func() {
		s, err := m.Open(target)
		if err == nil && s != nil {
			s.Write([]byte("payload"))
		}
	}()

	var onWire []byte
	select {
	case onWire = <-captured:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was written to the connection")
	}
	if onWire == nil {
		t.Fatal("failed to read the frame off the connection")
	}

	if bytes.Contains(onWire, []byte(secretHost)) {
		t.Fatalf("destination %q appears in cleartext on the wire (%d bytes captured)", secretHost, len(onWire))
	}
	if bytes.Contains(onWire, []byte(target)) {
		t.Fatalf("destination %q appears in cleartext on the wire", target)
	}

	// Sanity: it really was an OPEN frame that got written, so the assertion above
	// isn't passing merely because nothing was sent.
	if len(onWire) < protocol.FrameHeaderSize {
		t.Fatalf("captured only %d bytes, expected at least a frame header", len(onWire))
	}
	if got := protocol.FrameType(onWire[0]); got != protocol.FrameOpen {
		t.Fatalf("first frame on the wire was type %d, expected FrameOpen", got)
	}

	// And the payload must be longer than the plaintext target: nonce + padding +
	// tag, i.e. genuinely sealed rather than merely obfuscated.
	payloadLen := int(onWire[4])<<8 | int(onWire[5])
	if payloadLen <= len(target) {
		t.Fatalf("OPEN payload is %d bytes for a %d-byte target - does not look sealed",
			payloadLen, len(target))
	}
}

// TestOpenFrameRoundTripsTarget confirms the other half: encrypted or not, the
// accepting end must recover the exact target.
func TestOpenFrameRoundTripsTarget(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	const target = "example.com:8443"
	if _, err := m1.Open(target); err != nil {
		t.Fatal(err)
	}

	accepted := make(chan *Stream, 1)
	go func() {
		s, err := m2.Accept()
		if err == nil {
			accepted <- s
		}
	}()

	select {
	case s := <-accepted:
		if s.Target() != target {
			t.Fatalf("target came through as %q, want %q", s.Target(), target)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream was never accepted - the encrypted OPEN did not round-trip")
	}
}

// TestUDPFlagSurvivesEncryptedOpen pins that flags still work now that they're
// part of the AEAD's additional data rather than ignored.
func TestUDPFlagSurvivesEncryptedOpen(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	if _, err := m1.OpenUDP("1.1.1.1:53"); err != nil {
		t.Fatal(err)
	}

	accepted := make(chan *Stream, 1)
	go func() {
		s, err := m2.Accept()
		if err == nil {
			accepted <- s
		}
	}()

	select {
	case s := <-accepted:
		if !s.IsUDP() {
			t.Fatal("UDP flag was lost across the encrypted OPEN")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UDP stream was never accepted")
	}
}
