package transport

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"net"

	"phantom/internal/protocol"
	"phantom/internal/tunnel"
)

type ConnPool struct {
	conns    []*poolConn
	mu       sync.Mutex
	dialFunc func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error)
	maxConns int
	closed   int32
}

type poolConn struct {
	conn    net.Conn
	crypto  *protocol.SessionCrypto
	mux     *tunnel.Multiplexer
	healthy bool
	mu      sync.Mutex
}

func NewConnPool(maxConns int, dialFunc func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error)) *ConnPool {
	return &ConnPool{
		conns:    make([]*poolConn, 0, maxConns),
		dialFunc: dialFunc,
		maxConns: maxConns,
	}
}

func (p *ConnPool) Get(ctx context.Context) (*tunnel.Multiplexer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if atomic.LoadInt32(&p.closed) == 1 {
		return nil, ErrPoolClosed
	}

	// A single healthy connection carries every stream (the multiplexer is built
	// for exactly that), so steady state is one conn reused here; the rest of the
	// pool only ever fills in transiently while a dead one is being replaced.
	for _, pc := range p.conns {
		pc.mu.Lock()
		healthy := pc.healthy
		pc.mu.Unlock()
		if healthy {
			return pc.mux, nil
		}
	}

	// Nothing healthy to reuse. If the pool is full of dead conns, drop them
	// (their readLoops have already closed them; rotateConn may not have pruned
	// them yet) so we can still redial instead of being wedged at maxConns.
	if len(p.conns) >= p.maxConns {
		for _, pc := range p.conns {
			pc.mux.Close()
		}
		p.conns = p.conns[:0]
	}

	pc, err := p.newConn(ctx)
	if err != nil {
		return nil, err
	}
	p.conns = append(p.conns, pc)
	return pc.mux, nil
}

func (p *ConnPool) newConn(ctx context.Context) (*poolConn, error) {
	conn, crypto, err := p.dialFunc(ctx)
	if err != nil {
		return nil, err
	}

	mux := tunnel.NewMultiplexer(conn, crypto)

	pc := &poolConn{
		conn:    conn,
		crypto:  crypto,
		mux:     mux,
		healthy: true,
	}

	go p.monitorConn(pc)

	return pc, nil
}

func (p *ConnPool) monitorConn(pc *poolConn) {
	// Blocks until the underlying connection actually dies - its readLoop hitting
	// a real I/O error (the network interface it was bound to disappearing under
	// it, a reset, etc.) - then marks it dead and redials a replacement. Without
	// this, Get() would keep handing out this same dead mux forever on an
	// otherwise-idle pool, which is exactly what made a phone's Wi-Fi<->cellular
	// switch look like total internet loss instead of a brief reconnect.
	<-pc.mux.Done()
	pc.mu.Lock()
	pc.healthy = false
	pc.mu.Unlock()
	p.rotateConn(pc)
}

func (p *ConnPool) rotateConn(old *poolConn) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, pc := range p.conns {
		if pc == old {
			old.mux.Close()
			p.conns = append(p.conns[:i], p.conns[i+1:]...)
			break
		}
	}

	if atomic.LoadInt32(&p.closed) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		newPC, err := p.newConn(ctx)
		if err != nil {
			log.Printf("[connpool] rotation failed: %v", err)
			return
		}
		p.conns = append(p.conns, newPC)
	}
}

// Recycle closes and drops every current connection without shutting the pool
// down, so the next Get() (and each closed conn's own monitorConn, which fires
// on the Close below) dials fresh ones. Meant to be called right after the
// underlying network changed out from under the existing sockets - a phone's
// Wi-Fi<->cellular switch - to force an immediate redial on the new network
// instead of waiting for the dead sockets to be noticed on their own (which,
// with no traffic flowing, can take a full TCP timeout). Harmless no-op on a
// closed pool.
func (p *ConnPool) Recycle() {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()

	for _, pc := range conns {
		pc.mux.Close()
	}
}

func (p *ConnPool) Close() error {
	atomic.StoreInt32(&p.closed, 1)
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, pc := range p.conns {
		pc.mux.Close()
	}
	p.conns = nil
	return nil
}

var ErrPoolClosed = &PoolError{"pool closed"}

type PoolError struct {
	msg string
}

func (e *PoolError) Error() string {
	return e.msg
}
