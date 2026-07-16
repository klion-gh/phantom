package tunnel

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"phantom/internal/protocol"
)

func makeTestPair(t *testing.T) (*Multiplexer, *Multiplexer) {
	t.Helper()

	c1, c2 := net.Pipe()

	ecdhSecret := []byte("0123456789abcdef0123456789abcdef")
	psk := []byte("shared-psk-bytes-1234567890abcdef")
	clientPub := []byte("client-ephemeral-pub-1234567890ab")
	serverPub := []byte("server-static-pub-1234567890abcd")

	crypto1, err := protocol.DeriveSessionKeys(ecdhSecret, psk, clientPub, serverPub)
	if err != nil {
		t.Fatal(err)
	}
	crypto2, err := protocol.DeriveSessionKeys(ecdhSecret, psk, clientPub, serverPub)
	if err != nil {
		t.Fatal(err)
	}

	m1 := NewMultiplexer(c1, crypto1)
	m2 := NewMultiplexer(c2, crypto2)

	return m1, m2
}

func TestMultiplexerOpenAccept(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	go func() {
		s, err := m1.Open("example.com:80")
		if err != nil {
			t.Errorf("Open() error = %v", err)
			return
		}
		s.Close()
	}()

	time.Sleep(50 * time.Millisecond)

	s, err := m2.Accept()
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	defer s.Close()

	if s.Target() != "example.com:80" {
		t.Errorf("Target() = %q, want %q", s.Target(), "example.com:80")
	}
}

func TestStreamReadWrite(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	msg := []byte("hello from client")

	go func() {
		s, err := m1.Open("test:80")
		if err != nil {
			t.Errorf("Open() error = %v", err)
			return
		}

		_, err = s.Write(msg)
		if err != nil {
			t.Errorf("Write() error = %v", err)
		}

		time.Sleep(100 * time.Millisecond)
		s.Close()
	}()

	time.Sleep(50 * time.Millisecond)

	s, err := m2.Accept()
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	defer s.Close()

	buf := make([]byte, len(msg))
	_, err = io.ReadFull(s, buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if !bytes.Equal(buf, msg) {
		t.Errorf("Read() = %q, want %q", buf, msg)
	}
}

func TestStreamBidirectional(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	msg1 := []byte("hello from client")
	msg2 := []byte("hello from server")

	go func() {
		s, err := m1.Open("test:80")
		if err != nil {
			t.Errorf("Open() error = %v", err)
			return
		}
		defer s.Close()

		s.Write(msg1)

		buf := make([]byte, len(msg2))
		io.ReadFull(s, buf)
		if !bytes.Equal(buf, msg2) {
			t.Errorf("Read() = %q, want %q", buf, msg2)
		}
	}()

	time.Sleep(50 * time.Millisecond)

	s, err := m2.Accept()
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	defer s.Close()

	buf := make([]byte, len(msg1))
	io.ReadFull(s, buf)
	if !bytes.Equal(buf, msg1) {
		t.Errorf("Read() = %q, want %q", buf, msg1)
	}

	s.Write(msg2)
	time.Sleep(50 * time.Millisecond)
}

func TestStreamClose(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	go func() {
		s, err := m1.Open("test:80")
		if err != nil {
			return
		}
		s.Write([]byte("data"))
		s.Close()
	}()

	time.Sleep(50 * time.Millisecond)

	s, err := m2.Accept()
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}

	buf := make([]byte, 100)
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Read() error = %v", err)
	}

	if string(buf[:n]) != "data" {
		t.Errorf("Read() = %q, want %q", buf[:n], "data")
	}
}

func TestMultiplexerClose(t *testing.T) {
	m1, m2 := makeTestPair(t)

	m1.Close()

	if !m1.IsClosed() {
		t.Error("IsClosed() should return true after Close()")
	}

	m2.Close()
}
