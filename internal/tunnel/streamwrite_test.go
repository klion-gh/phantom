package tunnel

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"time"

	"phantom/internal/protocol"
)

// TestStreamWriteChunksLargeWrite covers the write that used to overflow the
// frame header's uint16 length and desynchronise the multiplexer permanently:
// a single Write larger than one frame can hold must arrive intact, split across
// frames, with the connection still usable afterwards.
func TestStreamWriteChunksLargeWrite(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	payload := make([]byte, 3*protocol.DataChunkSize+1234)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	s1, err := m1.Open("example.com:443")
	if err != nil {
		t.Fatal(err)
	}

	accepted := make(chan *Stream, 1)
	go func() {
		s, err := m2.Accept()
		if err != nil {
			return
		}
		accepted <- s
	}()

	var s2 *Stream
	select {
	case s2 = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the stream to be accepted")
	}

	go func() {
		if _, err := s1.Write(payload); err != nil {
			t.Errorf("Write(%d bytes): %v", len(payload), err)
		}
	}()

	got := make([]byte, len(payload))
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(s2, got)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reading back %d bytes: %v", len(payload), err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out reading the payload back - the multiplexer likely desynchronised")
	}

	if !bytes.Equal(got, payload) {
		t.Fatal("payload came back corrupted")
	}

	// The connection has to still work after a chunked write, which is what a
	// desync would have broken.
	if _, err := s1.Write([]byte("still alive")); err != nil {
		t.Fatalf("write after a large write: %v", err)
	}
	tail := make([]byte, len("still alive"))
	if _, err := io.ReadFull(s2, tail); err != nil {
		t.Fatalf("read after a large write: %v", err)
	}
	if string(tail) != "still alive" {
		t.Fatalf("got %q after the large write", tail)
	}
}

// TestUDPStreamRejectsOversizedDatagram checks the other half: a datagram that
// can't fit one frame is refused with a sentinel the relays recognise, rather
// than being split (which would silently deliver two shorter datagrams) or
// overflowing the header.
func TestUDPStreamRejectsOversizedDatagram(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	s, err := m1.OpenUDP("1.1.1.1:53")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write(make([]byte, protocol.MaxDataPlaintext+1)); !errors.Is(err, ErrDatagramTooLarge) {
		t.Fatalf("oversized datagram: got %v, want ErrDatagramTooLarge", err)
	}

	// The largest datagram that does fit must still go through untouched, in one
	// frame - the boundary is exactly where relays start dropping.
	payload := make([]byte, protocol.MaxDataPlaintext)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	accepted := make(chan *Stream, 1)
	go func() {
		st, err := m2.Accept()
		if err != nil {
			return
		}
		accepted <- st
	}()

	var s2 *Stream
	select {
	case s2 = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the UDP stream to be accepted")
	}

	go func() {
		if _, err := s.Write(payload); err != nil {
			t.Errorf("Write(MaxDataPlaintext): %v", err)
		}
	}()

	buf := make([]byte, len(payload)+1)
	read := make(chan int, 1)
	go func() {
		n, err := s2.Read(buf)
		if err != nil {
			return
		}
		read <- n
	}()

	select {
	case n := <-read:
		if n != len(payload) {
			t.Fatalf("datagram arrived split: read %d of %d bytes in one Read", n, len(payload))
		}
		if !bytes.Equal(buf[:n], payload) {
			t.Fatal("datagram came back corrupted")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out reading the max-size datagram back")
	}
}
