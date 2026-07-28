package tunnel

import (
	"errors"
	"testing"
	"time"
)

// TestSendFrameDoesNotHangAfterClose is the regression test for the other half of
// "streams hang when the connection dies". Close was taught to wake blocked
// readers, but sendFrame could still block forever on the write side: both cases
// of its outer select are ready once the multiplexer is closed (writeCh is
// buffered), Go picks between ready cases at random, and the losing branch queued
// a frame onto a channel whose writeLoop had already returned - so nothing ever
// wrote req.errCh.
//
// Repeated because it is a race: a single attempt passes about half the time.
func TestSendFrameDoesNotHangAfterClose(t *testing.T) {
	for attempt := 0; attempt < 60; attempt++ {
		m1, m2 := makeTestPair(t)
		m2.Close()

		s, err := m1.Open("example.com:443")
		if err != nil {
			// Losing the race the other way (Open refused outright) is fine -
			// that's the correct failure.
			m1.Close()
			continue
		}

		go m1.Close()

		done := make(chan struct{})
		go func() {
			s.Write([]byte("in flight while the session dies"))
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d: Write blocked after the multiplexer closed - "+
				"a frame was queued onto writeCh after writeLoop had exited", attempt)
		}
		m1.Close()
	}
}

// TestOpenAfterCloseFailsFast covers the same hang reached through Open rather
// than Write: openStream calls sendFrame on the caller's own goroutine, so this
// wedged the caller directly.
func TestOpenAfterCloseFailsFast(t *testing.T) {
	for attempt := 0; attempt < 60; attempt++ {
		m1, m2 := makeTestPair(t)
		m2.Close()
		m1.Close()

		done := make(chan error, 1)
		go func() {
			_, err := m1.Open("example.com:443")
			done <- err
		}()

		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("attempt %d: Open succeeded on a closed multiplexer", attempt)
			}
			if !errors.Is(err, ErrSessionClosed) {
				t.Fatalf("attempt %d: Open failed with %v, want ErrSessionClosed", attempt, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d: Open blocked on a closed multiplexer", attempt)
		}
	}
}
