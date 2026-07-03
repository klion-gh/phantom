package tunnel

import (
	"context"
	"log"
	"net"
	"time"

	"phantom/internal/protocol"
)

type Session struct {
	mux       *Multiplexer
	conn      net.Conn
	crypto    *protocol.SessionCrypto
	startTime time.Time
}

func NewSession(conn net.Conn, crypto *protocol.SessionCrypto, sendAuth bool) *Session {
	return &Session{
		mux:       NewMultiplexer(conn, crypto, sendAuth),
		conn:      conn,
		crypto:    crypto,
		startTime: time.Now(),
	}
}

func NewSessionFromMux(mux *Multiplexer) *Session {
	return &Session{
		mux:       mux,
		startTime: time.Now(),
	}
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

func (s *Session) Uptime() time.Duration {
	return time.Since(s.startTime)
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
			log.Printf("[session] accept error: %v", err)
			continue
		}

		go handler(stream)
	}
}

func (s *Session) Ping() (time.Duration, error) {
	start := time.Now()

	pingData := make([]byte, 8)
	copy(pingData, []byte("PTLSping"))

	pingFrame := &protocol.Frame{
		Type:     protocol.FramePing,
		StreamID: 0,
		Payload:  pingData,
	}

	if err := s.mux.sendFrame(pingFrame); err != nil {
		return 0, err
	}

	return time.Since(start), nil
}
