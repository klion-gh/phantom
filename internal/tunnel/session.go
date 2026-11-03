package tunnel

import (
	"context"

	"phantom/internal/logx"
)

type Session struct {
	mux  *Multiplexer
	pick func() *Multiplexer
}

func NewSessionFromMux(mux *Multiplexer) *Session {
	return &Session{mux: mux}
}

// NewSessionFromPool builds a client session that spreads its streams over
// several connections: each new stream goes on the connection pick returns
// (a pool's least-loaded healthy one - transport.ConnPool.Pick). When pick has
// none, mux - the connection the session was built on - carries it.
func NewSessionFromPool(mux *Multiplexer, pick func() *Multiplexer) *Session {
	return &Session{mux: mux, pick: pick}
}

// current is the connection the next stream goes on.
func (s *Session) current() *Multiplexer {
	if s.pick != nil {
		if m := s.pick(); m != nil {
			return m
		}
	}
	return s.mux
}

func (s *Session) Open(target string) (*Stream, error) {
	return s.current().Open(target)
}

func (s *Session) OpenUDP(target string) (*Stream, error) {
	return s.current().OpenUDP(target)
}

func (s *Session) Accept() (*Stream, error) {
	return s.mux.Accept()
}

func (s *Session) Close() error {
	return s.mux.Close()
}

// Multiplexer is the connection the next stream would go on - for a pooled
// session, not necessarily the one it was built on.
func (s *Session) Multiplexer() *Multiplexer {
	return s.current()
}

// IsAlive reports whether a new stream has a live connection to go on. A
// pooled session stays alive while any of the pool's connections is.
func (s *Session) IsAlive() bool {
	return !s.current().IsClosed()
}

func (s *Session) HandleIncoming(ctx context.Context, handler func(stream *Stream)) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		stream, err := s.Accept()
		if err != nil {
			if s.mux.IsClosed() {
				return
			}
			logx.Warnf("[session] accept error: %v", err)
			continue
		}

		go handler(stream)
	}
}

// There is deliberately no Ping method here any more. The one that existed was
// never called by anything, and measured the wrong thing anyway: it returned how
// long it took to hand the frame to the write loop, with no correlation to the
// pong that came back, so it reported queueing latency rather than a round trip.
//
// handlePing on the receiving side stays - answering a peer's ping costs nothing
// and keeps the frame type usable - but nothing in this codebase sends one, so
// the tunnel currently has no application-level keepalive at all. See
// PROTOCOL.md's known-gaps list; whoever implements one should write a correct
// round-trip measurement rather than restore this.
