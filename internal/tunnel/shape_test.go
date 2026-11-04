package tunnel

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"phantom/internal/protocol"
)

// countingConn counts Writes on the client end of a test pair.
type countingConn struct {
	net.Conn
	writes atomic.Int32
}

func (c *countingConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(p)
}

func shapedPair(t *testing.T) (*Multiplexer, *Multiplexer, *countingConn) {
	t.Helper()
	c1, c2 := net.Pipe()
	ecdh := []byte("0123456789abcdef0123456789abcdef")
	psk := []byte("shared-psk-bytes-1234567890abcdef")
	cp := []byte("client-ephemeral-pub-1234567890ab")
	sp := []byte("server-static-pub-1234567890abcd")
	k1, _ := protocol.DeriveSessionKeys(ecdh, psk, cp, sp, protocol.RoleClient)
	k2, _ := protocol.DeriveSessionKeys(ecdh, psk, cp, sp, protocol.RoleServer)
	cc := &countingConn{Conn: c1}
	m1, m2 := NewMultiplexer(cc, k1), NewMultiplexer(c2, k2)
	t.Cleanup(func() { m1.Close(); m2.Close() })
	return m1, m2, cc
}

func acceptWithin(t *testing.T, m *Multiplexer, d time.Duration) *Stream {
	t.Helper()
	got := make(chan *Stream, 1)
	go func() {
		s, err := m.Accept()
		if err == nil {
			got <- s
		}
	}()
	select {
	case s := <-got:
		return s
	case <-time.After(d):
		t.Fatal("the peer never saw the stream open")
		return nil
	}
}

// A stream's OPEN leaves with its first data, in one write.
func TestOpenRidesWithTheFirstData(t *testing.T) {
	m1, m2, cc := shapedPair(t)
	s, err := m1.Open("example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	if n := cc.writes.Load(); n != 0 {
		t.Fatalf("Open wrote %d times before any data", n)
	}
	go s.Write([]byte("client hello"))
	peer := acceptWithin(t, m2, 2*time.Second)
	buf := make([]byte, 12)
	if _, err := io.ReadFull(peer, buf); err != nil || string(buf) != "client hello" {
		t.Fatalf("peer read %q, %v", buf, err)
	}
	if n := cc.writes.Load(); n != 1 {
		t.Fatalf("OPEN and first data took %d writes, want 1", n)
	}
}

// With nothing to send - the far side speaks first - the OPEN goes out on its
// own after the holdback, and the server's bytes come back.
func TestOpenGoesAloneWhenTheClientIsSilent(t *testing.T) {
	m1, m2, _ := shapedPair(t)
	s, err := m1.Open("example.org:25")
	if err != nil {
		t.Fatal(err)
	}
	peer := acceptWithin(t, m2, 2*time.Second)
	go peer.Write([]byte("220 hello"))
	buf := make([]byte, 9)
	if _, err := io.ReadFull(s, buf); err != nil || string(buf) != "220 hello" {
		t.Fatalf("read %q, %v", buf, err)
	}
}

// Opened and closed with nothing sent: the peer still sees it open and end.
func TestOpenThenCloseIsStillSeen(t *testing.T) {
	m1, m2, _ := shapedPair(t)
	s, err := m1.Open("example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	peer := acceptWithin(t, m2, 2*time.Second)
	done := make(chan error, 1)
	go func() {
		_, err := peer.Read(make([]byte, 1))
		done <- err
	}()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("peer read %v, want EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the peer never saw the stream close")
	}
}

// After a stream's opening phase a frame gets next to no padding; in it, a
// small frame is padded towards a browser-sized record but never beyond the
// largest one measured plus the jitter cap.
func TestPadForBounds(t *testing.T) {
	maxResp := responseRecordSizes[len(responseRecordSizes)-1]
	for i := 0; i < 2000; i++ {
		if p := padFor(20000, true, false); p < 0 || p > tailPadMax {
			t.Fatalf("tail pad %d outside [0,%d]", p, tailPadMax)
		}
		for _, req := range []bool{true, false} {
			p := padFor(40, req, true)
			if p < 0 || p > maxResp+openingJitterMax {
				t.Fatalf("opening pad %d out of bounds", p)
			}
		}
	}
	// Requests are mostly small: the median padded request record stays well
	// under the old bucket floor of 256 bytes plus framing.
	var sizes []int
	for i := 0; i < 1001; i++ {
		sizes = append(sizes, 40+padFor(40, true, true)+protocol.SealedFrameOverhead+recordOverhead)
	}
	if med := median(sizes); med > 300 {
		t.Fatalf("median request record %d, want under 300", med)
	}
}

func median(xs []int) int {
	c := append([]int(nil), xs...)
	for i := range c {
		for j := i + 1; j < len(c); j++ {
			if c[j] < c[i] {
				c[i], c[j] = c[j], c[i]
			}
		}
	}
	return c[len(c)/2]
}

// The holdback timer takes the OPEN at the moment the first Write arrives:
// that data must still reach the wire after the OPEN, not ahead of it -
// otherwise the peer drops it as data for an unknown stream and the stream
// hangs. (Hit on a slow CI runner, where a Write ~25 ms after Open is common.)
func TestFirstWriteRacingTheHoldbackStaysBehindOpen(t *testing.T) {
	m1, m2, _ := shapedPair(t)
	taken := make(chan struct{})
	wrote := make(chan struct{})
	var once sync.Once
	hook := func() {
		once.Do(func() {
			close(taken)
			// Give a Write that wrongly bypassed the OPEN time to hit the wire.
			select {
			case <-wrote:
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
	afterOpenTaken.Store(&hook)
	defer afterOpenTaken.Store(nil)

	s, err := m1.Open("example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	<-taken
	go func() {
		s.Write([]byte("hello"))
		close(wrote)
	}()
	peer := acceptWithin(t, m2, 2*time.Second)
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 5)
		io.ReadFull(peer, buf)
		got <- string(buf)
	}()
	select {
	case g := <-got:
		if g != "hello" {
			t.Fatalf("peer read %q", g)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first data never arrived - it went out ahead of the OPEN")
	}
}
