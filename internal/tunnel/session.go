package tunnel

import (
	"context"

	"phantom/internal/logx"
)

type Session struct {
	mux *Multiplexer
}

func NewSessionFromMux(mux *Multiplexer) *Session {
	return &Session{mux: mux}
}

func (s *Session) Open(target string) (*Stream, error) {
	return s.mux.Open(target)
}

func (s *Session) OpenUDP(target string) (*Stream, error) {
	return s.mux.OpenUDP(target)
}

func (s *Session) Accept() (*Stream, error) {
	return s.mux.Accept()
}

func (s *Session) Close() error {
	return s.mux.Close()
}

func (s *Session) Multiplexer() *Multiplexer {
	return s.mux
}

func (s *Session) IsAlive() bool {
	return !s.mux.IsClosed()
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
