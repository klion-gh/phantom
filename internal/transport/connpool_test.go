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
