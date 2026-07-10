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
	maxBytes int64
	closed   int32
}

type poolConn struct {
	conn      net.Conn
	crypto    *protocol.SessionCrypto
	mux       *tunnel.Multiplexer
	bytesUsed int64
	healthy   bool
	mu        sync.Mutex
}

func NewConnPool(maxConns int, maxBytes int64, dialFunc func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error)) *ConnPool {
	return &ConnPool{
		conns:    make([]*poolConn, 0, maxConns),
		dialFunc: dialFunc,
		maxConns: maxConns,
		maxBytes: maxBytes,
	}
}

func (p *ConnPool) Get(ctx context.Context) (*tunnel.Multiplexer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if atomic.LoadInt32(&p.closed) == 1 {
		return nil, ErrPoolClosed
	}

	for _, pc := range p.conns {
		pc.mu.Lock()
		if pc.healthy && pc.bytesUsed < p.maxBytes {
			pc.mu.Unlock()
			return pc.mux, nil
		}
		pc.mu.Unlock()
	}

	if len(p.conns) < p.maxConns {
		pc, err := p.newConn(ctx)
		if err != nil {
			return nil, err
		}
		p.conns = append(p.conns, pc)
		return pc.mux, nil
	}

	var leastLoaded *poolConn
	var minBytes int64 = 1<<63 - 1
	for _, pc := range p.conns {
		pc.mu.Lock()
		if pc.healthy && pc.bytesUsed < minBytes {
			leastLoaded = pc
			minBytes = pc.bytesUsed
		}
		pc.mu.Unlock()
	}

	if leastLoaded == nil {
		pc, err := p.newConn(ctx)
		if err != nil {
			return nil, err
		}
		p.conns = append(p.conns, pc)
		return pc.mux, nil
	}

	return leastLoaded.mux, nil
}

func (p *ConnPool) newConn(ctx context.Context) (*poolConn, error) {
	conn, crypto, err := p.dialFunc(ctx)
	if err != nil {
		return nil, err
	}

	mux := tunnel.NewMultiplexer(conn, crypto, true)

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
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pc.mux.Done():
			// The underlying connection actually died (its readLoop hit a real
			// I/O error - e.g. the network interface it was bound to disappeared
			// under it, a reset, etc.), not just aged out by byte volume. Without
			// this, Get() would keep handing out this same dead mux forever on an
			// otherwise-idle pool, since the byte-volume check below would never
			// trigger - which is exactly what made a phone's Wi-Fi<->cellular
			// switch look like total internet loss instead of a brief reconnect.
			pc.mu.Lock()
			pc.healthy = false
			pc.mu.Unlock()
			p.rotateConn(pc)
			return
		case <-ticker.C:
			pc.mu.Lock()
			if pc.bytesUsed >= p.maxBytes {
				pc.healthy = false
				pc.mu.Unlock()
				p.rotateConn(pc)
				return
			}
			pc.mu.Unlock()
		}
	}
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

func (p *ConnPool) AddBytes(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pc := range p.conns {
		pc.mu.Lock()
		pc.bytesUsed += n
		pc.mu.Unlock()
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
