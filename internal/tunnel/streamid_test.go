package tunnel

import (
	"errors"
	"testing"
	"time"
)

// TestStreamIDsSkipLiveStreams is the regression test for the wrapping counter:
// the allocator must never hand out an ID that is still open. Before this, a bare
// `nextClientID += 2` wrapped after 32768 streams and overwrote live entries in
// the map, splicing unrelated connections' data together.
func TestStreamIDsSkipLiveStreams(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	// Hold a stream open on the very ID the counter is about to wrap back onto.
	held, err := m1.Open("held.example:443")
	if err != nil {
		t.Fatal(err)
	}
	heldID := held.ID()

	// Wind the counter all the way around to that ID again.
	m1.mu.Lock()
	m1.nextClientID = heldID
	m1.mu.Unlock()

	next, err := m1.Open("next.example:443")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() == heldID {
		t.Fatalf("allocator reused ID %d while it was still open", heldID)
	}

	// The held stream must still be the one registered under its own ID.
	m1.mu.RLock()
	registered := m1.streams[heldID]
	m1.mu.RUnlock()
	if registered != held {
		t.Fatalf("ID %d no longer maps to the stream that holds it", heldID)
	}
}

// TestStreamIDsStayOddAcrossWrap checks the parity invariant the +=2 walk relies
// on: IDs must stay odd across the uint16 wrap so that 0 (reserved for
// session-level frames like PING) is never allocated.
func TestStreamIDsStayOddAcrossWrap(t *testing.T) {
	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	m1.mu.Lock()
	m1.nextClientID = 65535 // last odd value before the wrap
	m1.mu.Unlock()

	for i := 0; i < 4; i++ {
		s, err := m1.Open("example.com:443")
		if err != nil {
			t.Fatal(err)
		}
		if s.ID() == 0 {
			t.Fatal("allocated stream ID 0, which is reserved for session-level frames")
		}
		if s.ID()%2 == 0 {
			t.Fatalf("allocated even stream ID %d; the server's half of the space", s.ID())
		}
		s.Close()
	}
}

// TestStalledStreamDoesNotFreezeOthers is the head-of-line-blocking regression
// test: one stream whose reader never consumes used to block the multiplexer's
// single readLoop indefinitely, freezing every other stream on the connection.
// Now that stream is reset after streamStallTimeout and the rest keep working.
func TestStalledStreamDoesNotFreezeOthers(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out streamStallTimeout")
	}

	m1, m2 := makeTestPair(t)
	defer m1.Close()
	defer m2.Close()

	// Stream A: opened, accepted, and then never read from.
	stalled, err := m1.Open("stalled.example:443")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan *Stream, 2)
	go func() {
		for {
			s, err := m2.Accept()
			if err != nil {
				return
			}
			accepted <- s
		}
	}()
	var stalledPeer *Stream
	select {
	case stalledPeer = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out accepting the first stream")
	}

	// Overrun its receive buffer without anyone reading the far end.
	filler := make([]byte, 512)
	for i := 0; i < cap(stalledPeer.readCh)+4; i++ {
		if _, err := stalled.Write(filler); err != nil {
			break
		}
	}

	// Stream B on the same connection must still work while A is stuck.
	healthy, err := m1.Open("healthy.example:443")
	if err != nil {
		t.Fatalf("could not open a second stream while the first was stalled: %v", err)
	}
	var healthyPeer *Stream
	select {
	case healthyPeer = <-accepted:
	case <-time.After(streamStallTimeout + 10*time.Second):
		t.Fatal("the stalled stream froze the whole multiplexer - second stream never arrived")
	}

	if _, err := healthy.Write([]byte("still moving")); err != nil {
		t.Fatalf("write on the healthy stream: %v", err)
	}
	buf := make([]byte, 32)
	readDone := make(chan error, 1)
	go func() {
		_, err := healthyPeer.Read(buf)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("read on the healthy stream: %v", err)
		}
	case <-time.After(streamStallTimeout + 10*time.Second):
		t.Fatal("the healthy stream never delivered while another was stalled")
	}

	// And the stalled one is reset rather than left hanging forever.
	deadline := time.After(streamStallTimeout + 10*time.Second)
	for {
		if err := stalledPeer.endErr(); errors.Is(err, ErrStreamStalled) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("stalled stream was never reset (endErr = %v)", stalledPeer.endErr())
		case <-time.After(200 * time.Millisecond):
		}
	}
}
