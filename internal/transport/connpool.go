package transport

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"net"

	"phantom/internal/diag"
	"phantom/internal/protocol"
	"phantom/internal/tunnel"
)

// connSeq numbers every connection this process dials, so a log's dial, stall
// and death lines for one connection can be told apart from the next one's.
var connSeq atomic.Int64

// stallAfter is how long the server may stay silent while we keep sending
// before the connection is logged as stalled. Long enough that an ordinary
// round trip (even a slow mobile one) never trips it, short enough to line up
// with the DNS timeouts a stall causes - a resolver gives up after ~5s.
const stallAfter = 8 * time.Second

// activeConns is how many connections the pool keeps carrying streams, so a
// big transfer isn't held to one TCP connection's congestion window. Every
// stream used to share a single connection: on a lossy international path
// one loss stalled all of them at once, and a per-connection throttle capped
// the whole tunnel. Two, not more: an ordinary site is one or two long-lived
// connections, and every extra one is another handshake on the wire. The
// second is opened only once the first is up, and the handshake gate
// (waitHandshakeTurn) keeps the two at least half a second apart.
const activeConns = 2

// growRetry is how long the pool waits after a failed second dial before
// trying again: one connection still works, and a server that refused the
// second shouldn't be asked again on every new stream.
const growRetry = 30 * time.Second

type ConnPool struct {
	conns    []*poolConn
	mu       sync.Mutex
	dialFunc func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error)
	maxConns int
	closed   int32

	growing      bool      // a background dial for another active connection is in flight
	growFailedAt time.Time // when the last one failed - see growRetry

	// Cancelled by Close, so a replacement dial still in flight when the
	// pool shuts down is abandoned rather than finished - and Close, which
	// waits for mu, isn't held up behind it for the full dial timeout.
	life       context.Context
	cancelLife context.CancelFunc
}

type poolConn struct {
	conn    net.Conn
	crypto  *protocol.SessionCrypto
	mux     *tunnel.Multiplexer
	healthy bool
	retired bool // closed on purpose as surplus - see retire
	mu      sync.Mutex
	id      int64
}

func NewConnPool(maxConns int, dialFunc func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error)) *ConnPool {
	life, cancelLife := context.WithCancel(context.Background())
	return &ConnPool{
		conns:      make([]*poolConn, 0, maxConns),
		dialFunc:   dialFunc,
		maxConns:   maxConns,
		life:       life,
		cancelLife: cancelLife,
	}
}

func (p *ConnPool) Get(ctx context.Context) (*tunnel.Multiplexer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if atomic.LoadInt32(&p.closed) == 1 {
		return nil, ErrPoolClosed
	}

	// Any healthy connection will do here: Get is what a session is built on
	// and what refreshers call when theirs died. Spreading streams over the
	// connections is Pick's job.
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

// Pick returns the healthy connection carrying the fewest streams, for the
// next stream to go on, or nil if there is none (the caller falls back to the
// connection it has, and Get redials). It never dials itself, so opening a
// stream never waits on a handshake; instead, while fewer than activeConns are
// up, it starts one more in the background.
func (p *ConnPool) Pick() *tunnel.Multiplexer {
	p.mu.Lock()
	defer p.mu.Unlock()
	if atomic.LoadInt32(&p.closed) == 1 {
		return nil
	}
	var best *poolConn
	bestStreams, healthy := 0, 0
	for _, pc := range p.conns {
		if !pc.isHealthy() {
			continue
		}
		healthy++
		if n := pc.mux.NumStreams(); best == nil || n < bestStreams {
			best, bestStreams = pc, n
		}
	}
	p.growLocked(healthy)
	if best == nil {
		return nil
	}
	return best.mux
}

func (p *ConnPool) wantConns() int {
	if p.maxConns < activeConns {
		return p.maxConns
	}
	return activeConns
}

// growLocked starts a background dial for one more active connection when
// one is missing. Only from a working pool (healthy > 0): with nothing up,
// Get's own dial comes first. Caller holds p.mu.
func (p *ConnPool) growLocked(healthy int) {
	if healthy == 0 || healthy >= p.wantConns() || p.growing ||
		len(p.conns) >= p.maxConns || time.Since(p.growFailedAt) < growRetry {
		return
	}
	p.growing = true
	go func() {
		ctx, cancel := context.WithTimeout(p.life, 15*time.Second)
		defer cancel()
		pc, err := p.newConn(ctx)

		p.mu.Lock()
		defer p.mu.Unlock()
		p.growing = false
		if err != nil {
			p.growFailedAt = time.Now()
			return
		}
		live := 0
		for _, c := range p.conns {
			if c.isHealthy() {
				live++
			}
		}
		// A rotation may have filled the gap while this was dialing.
		if atomic.LoadInt32(&p.closed) == 1 || live >= p.wantConns() {
			pc.retire()
			return
		}
		p.conns = append(p.conns, pc)
	}()
}

func (pc *poolConn) isHealthy() bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.healthy
}

// retire closes a connection the pool decided not to keep. Its death must
// not be mistaken for a lost connection that needs replacing.
func (pc *poolConn) retire() {
	pc.mu.Lock()
	pc.retired = true
	pc.mu.Unlock()
	pc.mux.Close()
}

func (p *ConnPool) newConn(ctx context.Context) (*poolConn, error) {
	id := connSeq.Add(1)
	start := time.Now()
	conn, crypto, err := p.dialFunc(ctx)
	if err != nil {
		diag.Event(diag.CatTunnel, "dialFail", "conn", id, "ms", time.Since(start), "err", err)
		return nil, err
	}
	if atomic.LoadInt32(&p.closed) == 1 {
		// The pool shut down while this was dialing: nothing will ever use
		// or close this connection, so don't keep it.
		conn.Close()
		return nil, ErrPoolClosed
	}
	diag.Event(diag.CatTunnel, "dialOk", "conn", id, "ms", time.Since(start), "remote", conn.RemoteAddr())

	mux := tunnel.NewMultiplexer(conn, crypto)

	pc := &poolConn{
		conn:    conn,
		crypto:  crypto,
		mux:     mux,
		healthy: true,
		id:      id,
	}

	go p.monitorConn(pc)
	go watchStall(pc)

	return pc, nil
}

// watchStall logs when the server goes silent on a connection we are still
// sending on, and when it comes back. A censor that blackholes a connection
// (drops its packets instead of resetting it) produces exactly this and
// nothing else: no error, no close, the connection simply stops answering -
// so without this the log would show a perfectly healthy tunnel while every
// request through it, DNS included, times out.
func watchStall(pc *poolConn) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var stalledSince time.Time
	for {
		select {
		case <-pc.mux.Done():
			return
		case now := <-ticker.C:
			a := pc.mux.Activity()
			silent := now.Sub(a.LastRx)
			sendingIntoSilence := a.LastTx.After(a.LastRx)
			switch {
			case stalledSince.IsZero() && sendingIntoSilence && silent > stallAfter:
				stalledSince = a.LastRx
				diag.Event(diag.CatTunnel, "stall",
					"conn", pc.id,
					"silentMs", silent,
					"lastTxAgoMs", now.Sub(a.LastTx),
					"ageS", int(now.Sub(a.Created).Seconds()),
					"rxBytes", a.BytesRx,
					"txBytes", a.BytesTx)
			case !stalledSince.IsZero() && a.LastRx.After(stalledSince):
				diag.Event(diag.CatTunnel, "stallEnd",
					"conn", pc.id,
					"stalledMs", a.LastRx.Sub(stalledSince))
				stalledSince = time.Time{}
			}
		}
	}
}

func (p *ConnPool) monitorConn(pc *poolConn) {
	// Blocks until the underlying connection actually dies - its readLoop hitting
	// a real I/O error (the network interface it was bound to disappearing under
	// it, a reset, etc.) - then marks it dead and redials a replacement. Without
	// this, Get() would keep handing out this same dead mux forever on an
	// otherwise-idle pool, which is exactly what made a phone's Wi-Fi<->cellular
	// switch look like total internet loss instead of a brief reconnect.
	<-pc.mux.Done()
	a := pc.mux.Activity()
	now := time.Now()
	diag.Event(diag.CatTunnel, "connDied",
		"conn", pc.id,
		"ageS", int(now.Sub(a.Created).Seconds()),
		"silentMs", now.Sub(a.LastRx),
		"rxBytes", a.BytesRx,
		"txBytes", a.BytesTx,
		"err", errOrClosed(pc.mux.Err()))
	pc.mu.Lock()
	pc.healthy = false
	retired := pc.retired
	pc.mu.Unlock()
	if retired {
		return
	}
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
		ctx, cancel := context.WithTimeout(p.life, 10*time.Second)
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
	p.cancelLife()
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

// errOrClosed names a nil error for the log: nil there means the connection
// was closed on purpose (disconnect, network change, pool shutdown), not that
// the cause went unrecorded.
func errOrClosed(err error) any {
	if err == nil {
		return "closedLocally"
	}
	return err
}
