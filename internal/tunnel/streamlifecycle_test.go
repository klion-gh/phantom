package tunnel

import (
	"errors"
	"io"
	"testing"
	"time"
)

// TestMultiplexerCloseUnblocksPendingRead is the regression test for streams
// that hung forever when the connection died: Close only closed the multiplexer,
// leaving every open Stream blocked on a closeCh nothing would ever close (the
// CLOSE frame that normally closes it can't come from a dead connection). The
// io.Copy pairs in internal/proxy and internal/netstack sat on those reads, so a
// dropped connection meant hung sockets rather than a fast failure.
func TestMultiplexerCloseUnblocksPendingRead(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m2.Close()

	s, err := m1.Open("example.com:443")
	if err != nil {
		t.Fatal(err)
	}

	type readResult struct {
		n   int
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := s.Read(buf)
		done <- readResult{n, err}
	}()

	// Let the Read actually block before killing the multiplexer.
	time.Sleep(50 * time.Millisecond)
	m1.Close()

	select {
	case res := <-done:
		if !errors.Is(res.err, ErrSessionClosed) {
			t.Fatalf("Read after the session died: got (%d, %v), want ErrSessionClosed", res.n, res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read never returned after the multiplexer closed - the stream is wedged")
	}

	// Write has to fail the same way rather than queueing into a dead session.
	if _, err := s.Write([]byte("x")); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("Write after the session died: got %v, want ErrSessionClosed", err)
	}
}

// TestPeerCloseReportsEOF pins the other half of the distinction: the peer
// closing one stream is an ordinary end-of-stream, and must stay io.EOF so
// io.Copy treats a completed transfer as complete.
func TestPeerCloseReportsEOF(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

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
		t.Fatal("timed out waiting for accept")
	}

	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		_, err := s1.Read(buf)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Read after the peer closed the stream: got %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read never returned after the peer closed the stream")
	}
}

// TestReceiveDataOnClosedStreamDoesNotBlock covers the blocking send that could
// wedge readLoop permanently: delivering to a closed stream whose buffer is full
// used to block on a channel nobody would read again, freezing every other
// stream sharing the connection.
func TestReceiveDataOnClosedStreamDoesNotBlock(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	s, err := m1.Open("example.com:443")
	if err != nil {
		t.Fatal(err)
	}

	s.closeWithErr(nil)
	for i := 0; i < cap(s.readCh); i++ {
		s.readCh <- []byte("filler")
	}

	done := make(chan struct{})
	go func() {
		s.receiveData([]byte("one frame past the buffer"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("receiveData blocked on a closed stream with a full buffer - readLoop would be wedged")
	}
}
