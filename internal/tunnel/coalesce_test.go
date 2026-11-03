package tunnel

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"phantom/internal/protocol"
)

// gatedConn counts Writes and holds the first one until released, so frames
// queue up behind it the way they do when several streams open at once.
type gatedConn struct {
	net.Conn
	writes  atomic.Int32
	release chan struct{}
	once    sync.Once
}

func (c *gatedConn) Write(p []byte) (int, error) {
	if c.writes.Add(1) == 1 {
		<-c.release
	}
	return c.Conn.Write(p)
}

// Frames that queue while a write is in progress leave together in the next
// one - and the far side still reads every one of them as its own frame.
func TestQueuedFramesLeaveInOneWrite(t *testing.T) {
	c1, c2 := net.Pipe()
	ecdh := []byte("0123456789abcdef0123456789abcdef")
	psk := []byte("shared-psk-bytes-1234567890abcdef")
	cp := []byte("client-ephemeral-pub-1234567890ab")
	sp := []byte("server-static-pub-1234567890abcd")
	crypto1, _ := protocol.DeriveSessionKeys(ecdh, psk, cp, sp, protocol.RoleClient)
	crypto2, _ := protocol.DeriveSessionKeys(ecdh, psk, cp, sp, protocol.RoleServer)

	gc := &gatedConn{Conn: c1, release: make(chan struct{})}
	m1 := NewMultiplexer(gc, crypto1)
	m2 := NewMultiplexer(c2, crypto2)
	defer m1.Close()
	defer m2.Close()

	const streams = 6
	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m1.Open("example.org:443"); err != nil {
				t.Error(err)
			}
		}()
	}
	// Let the first OPEN reach the gate and the other five queue behind it.
	deadline := time.Now().Add(2 * time.Second)
	for len(m1.writeCh) < streams-1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(gc.release)

	accepted := make(chan struct{})
	go func() {
		for i := 0; i < streams; i++ {
			if _, err := m2.Accept(); err != nil {
				t.Error(err)
				return
			}
		}
		close(accepted)
	}()
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("not every stream arrived")
	}
	wg.Wait()
	if n := gc.writes.Load(); n != 2 {
		t.Fatalf("%d writes for %d frames, want 2 (the gated one, then the rest together)", n, streams)
	}
}
