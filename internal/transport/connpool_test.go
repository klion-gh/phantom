package transport

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"phantom/internal/protocol"
)

func testCrypto(t *testing.T) *protocol.SessionCrypto {
	t.Helper()
	crypto, err := protocol.DeriveSessionKeys(
		[]byte("0123456789abcdef0123456789abcdef"), []byte("shared-psk-bytes-1234567890abcdef"),
		[]byte("client-ephemeral-pub-1234567890ab"), []byte("server-static-pub-1234567890abcd"),
		protocol.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	return crypto
}

// A disconnect that lands while the pool is redialing a replacement (the
// connection it had just died) must not wait out the redial - up to 10s -
// nor leave the connection it produces open and unused.
func TestCloseAbandonsARedialInFlight(t *testing.T) {
	crypto := testCrypto(t)
	var dials atomic.Int32
	redialStarted := make(chan struct{})
	pool := NewConnPool(2, func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error) {
		if dials.Add(1) == 1 {
			c1, c2 := net.Pipe()
			go io.Copy(io.Discard, c2)
			return c1, crypto, nil
		}
		// The replacement: a server that takes its time answering.
		close(redialStarted)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})
	mux, err := pool.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mux.Close() // the connection dies; the pool starts redialing
	select {
	case <-redialStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("the pool never redialed")
	}

	closed := make(chan struct{})
	go func() {
		pool.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited on the redial in flight")
	}
}

// Nothing dialed after Close is kept: the pool is gone, so nothing would
// ever use or close it.
func TestNoConnectionOutlivesAClosedPool(t *testing.T) {
	crypto := testCrypto(t)
	var pool *ConnPool
	var leaked net.Conn
	pool = NewConnPool(2, func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error) {
		c1, c2 := net.Pipe()
		go io.Copy(io.Discard, c2)
		// Shuts down while this dial is completing. (Close waits for the
		// pool's lock, which Get holds for the dial, so it runs alongside.)
		go pool.Close()
		for atomic.LoadInt32(&pool.closed) == 0 {
			time.Sleep(time.Millisecond)
		}
		leaked = c2
		return c1, crypto, nil
	})
	if _, err := pool.Get(context.Background()); err != ErrPoolClosed {
		t.Fatalf("expected ErrPoolClosed, got %v", err)
	}
	// The client end was closed, so the server end reads EOF.
	leaked.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := leaked.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("the connection dialed after Close was left open: %v", err)
	}
}

// pipeDialer returns a dial function whose every call yields a fresh working
// connection (the far end just drains), and a counter of dials made.
func pipeDialer(t *testing.T) (func(context.Context) (net.Conn, *protocol.SessionCrypto, error), *atomic.Int32) {
	crypto := testCrypto(t)
	var dials atomic.Int32
	return func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error) {
		dials.Add(1)
		c1, c2 := net.Pipe()
		go io.Copy(io.Discard, c2)
		return c1, crypto, nil
	}, &dials
}

func healthyConns(p *ConnPool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, pc := range p.conns {
		if pc.isHealthy() {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Once the first connection is up, picking a connection for a stream brings
// up a second one in the background - and never a third.
func TestPickGrowsToTwoConnections(t *testing.T) {
	dial, dials := pipeDialer(t)
	pool := NewConnPool(4, dial)
	defer pool.Close()
	if _, err := pool.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pool.Pick() == nil {
		t.Fatal("Pick found no connection on a working pool")
	}
	waitFor(t, "a second connection", func() bool { return healthyConns(pool) == 2 })
	for i := 0; i < 20; i++ {
		pool.Pick()
	}
	time.Sleep(50 * time.Millisecond)
	if n := healthyConns(pool); n != 2 {
		t.Fatalf("pool holds %d connections, want 2", n)
	}
	if n := dials.Load(); n != 2 {
		t.Fatalf("%d dials, want 2", n)
	}
}

// A pool capped at one connection stays at one: pool_size: 1 still means it.
func TestPickRespectsAPoolOfOne(t *testing.T) {
	dial, dials := pipeDialer(t)
	pool := NewConnPool(1, dial)
	defer pool.Close()
	if _, err := pool.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		pool.Pick()
	}
	time.Sleep(50 * time.Millisecond)
	if n := dials.Load(); n != 1 {
		t.Fatalf("%d dials on a pool of one, want 1", n)
	}
}

// New streams go to whichever connection carries fewer.
func TestPickBalancesStreams(t *testing.T) {
	dial, _ := pipeDialer(t)
	pool := NewConnPool(4, dial)
	defer pool.Close()
	first, err := pool.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pool.Pick()
	waitFor(t, "a second connection", func() bool { return healthyConns(pool) == 2 })

	// Load the first connection with streams; the next pick must be the other.
	for i := 0; i < 3; i++ {
		if _, err := first.Open("example.org:443"); err != nil {
			t.Fatal(err)
		}
	}
	if got := pool.Pick(); got == first {
		t.Fatal("Pick chose the busier connection")
	}
}

// A surplus connection the pool closes on purpose is not "lost": closing it
// must not trigger a replacement dial.
func TestRetiredConnectionIsNotReplaced(t *testing.T) {
	dial, dials := pipeDialer(t)
	pool := NewConnPool(4, dial)
	defer pool.Close()
	pc, err := pool.newConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pc.retire()
	time.Sleep(100 * time.Millisecond)
	if n := dials.Load(); n != 1 {
		t.Fatalf("%d dials after retiring a connection, want 1 (no replacement)", n)
	}
}
