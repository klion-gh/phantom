package proxy

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"phantom/internal/tunnel"
)

// How often currentSession is allowed to actually attempt a refresh while
// the session is dead - mirrors netstack.Tunnel's own cooldown so a
// prolonged outage doesn't turn every single SOCKS5 request into its own
// redial attempt against a server that's still unreachable.
const sessionRefreshCooldown = 3 * time.Second

type SOCKS5Server struct {
	addr string

	sessionMu          sync.Mutex
	session            *tunnel.Session
	refreshSession     func() (*tunnel.Session, error)
	lastRefreshAttempt time.Time

	mu       sync.Mutex
	listener net.Listener
	stopped  bool
}

func NewSOCKS5Server(addr string, session *tunnel.Session) *SOCKS5Server {
	return &SOCKS5Server{
		addr:    addr,
		session: session,
	}
}

// SetSessionRefresher installs an optional hook that lets the server recover
// from its one connection to the Phantom server dying - a brief Wi-Fi blip,
// the full-tunnel VPN's own TUN interface briefly capturing and then losing
// this connection when toggled on/off, a server-side hiccup - by fetching a
// fresh session (typically pool.Get() wrapped in tunnel.NewSessionFromMux)
// on demand. Without this, once that one connection died, every SOCKS5
// request would fail forever, even after the underlying transport.ConnPool
// had already quietly redialed a healthy replacement in the background -
// requiring a manual proxy restart to recover instead of the pool's own
// self-healing actually being put to use. Mirrors
// netstack.Tunnel.SetSessionRefresher exactly.
func (s *SOCKS5Server) SetSessionRefresher(fn func() (*tunnel.Session, error)) {
	s.sessionMu.Lock()
	s.refreshSession = fn
	s.sessionMu.Unlock()
}

// currentSession returns the server's session, transparently replacing it
// with a fresh one if the current one has died. Safe for concurrent use;
// every SOCKS5 request goes through this.
func (s *SOCKS5Server) currentSession() *tunnel.Session {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()

	if s.session != nil && s.session.IsAlive() {
		return s.session
	}
	if s.refreshSession == nil || time.Since(s.lastRefreshAttempt) < sessionRefreshCooldown {
		return s.session
	}

	s.lastRefreshAttempt = time.Now()
	fresh, err := s.refreshSession()
	if err != nil {
		log.Printf("[socks5] session refresh failed: %v", err)
		return s.session
	}
	log.Printf("[socks5] recovered with a fresh session after the previous one died")
	s.session = fresh
	return s.session
}

// Start binds addr and serves until Stop is called (returns nil) or a real
// accept error occurs. Kept as a single blocking call for callers (cmd/client)
// that just run it for the process's whole lifetime with no need to read
// back the bound address first - see Listen/Serve for callers that do (e.g.
// binding to port 0 and needing the OS-assigned port before traffic starts).
func (s *SOCKS5Server) Start() error {
	listener, err := s.Listen()
	if err != nil {
		return err
	}
	return s.Serve(listener)
}

// Listen binds addr without serving yet, so a caller using port 0 (OS picks
// a free port) can read the actual port back via Addr() before calling Serve.
func (s *SOCKS5Server) Listen() (net.Listener, error) {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("listen socks5: %w", err)
	}
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()
	return listener, nil
}

// Serve accepts connections on listener (from Listen, or Start's own) until
// Stop closes it, at which point it returns nil rather than looping on the
// resulting accept error forever.
func (s *SOCKS5Server) Serve(listener net.Listener) error {
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	log.Printf("[socks5] listening on %s", listener.Addr())

	for {
		conn, err := listener.Accept()
		if err != nil {
			s.mu.Lock()
			stopped := s.stopped
			s.mu.Unlock()
			if stopped {
				return nil
			}
			log.Printf("[socks5] accept error: %v", err)
			return err
		}

		go s.handleClient(conn)
	}
}

// Stop closes the listener, ending Start/Serve's accept loop. Safe to call
// even if the listener was never bound (e.g. Listen itself failed).
func (s *SOCKS5Server) Stop() {
	s.mu.Lock()
	s.stopped = true
	listener := s.listener
	s.mu.Unlock()
	if listener != nil {
		listener.Close()
	}
}

// Addr returns the actual bound address (nil until Listen/Start succeeds) -
// mainly useful when addr passed to NewSOCKS5Server ends in ":0" and the
// caller needs to know which port the OS actually picked.
func (s *SOCKS5Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

func (s *SOCKS5Server) handleClient(conn net.Conn) {
	defer conn.Close()

	buf := make([]byte, 256)

	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}

	if buf[0] != 0x05 {
		return
	}

	nmethods := int(buf[1])
	if _, err := io.ReadFull(conn, buf[:nmethods]); err != nil {
		return
	}

	conn.Write([]byte{0x05, 0x00})

	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return
	}

	if buf[0] != 0x05 || buf[1] != 0x01 {
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	var target string
	switch buf[3] {
	case 0x01:
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			return
		}
		target = fmt.Sprintf("%d.%d.%d.%d", buf[0], buf[1], buf[2], buf[3])
	case 0x03:
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return
		}
		domainLen := int(buf[0])
		if _, err := io.ReadFull(conn, buf[:domainLen]); err != nil {
			return
		}
		target = string(buf[:domainLen])
	case 0x04:
		if _, err := io.ReadFull(conn, buf[:16]); err != nil {
			return
		}
		target = net.IP(buf[:16]).String()
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	port := uint16(buf[0])<<8 | uint16(buf[1])
	target = fmt.Sprintf("%s:%d", target, port)

	stream, err := s.currentSession().Open(target)
	if err != nil {
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer stream.Close()

	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	pipe(conn, stream)
}

func pipe(a net.Conn, b io.ReadWriteCloser) {
	done := make(chan struct{})

	go func() {
		io.Copy(b, a)
		b.Close()
		close(done)
	}()

	io.Copy(a, b)
	a.Close()
	<-done
}
