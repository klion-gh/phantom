package proxy

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"phantom/internal/tunnel"
)

type HTTPProxyServer struct {
	addr string

	sessionMu          sync.Mutex
	session            *tunnel.Session
	refreshSession     func() (*tunnel.Session, error)
	lastRefreshAttempt time.Time
}

func NewHTTPProxyServer(addr string, session *tunnel.Session) *HTTPProxyServer {
	return &HTTPProxyServer{
		addr:    addr,
		session: session,
	}
}

// SetSessionRefresher is the HTTP proxy's copy of SOCKS5Server.SetSessionRefresher,
// which see. Until this existed the CONNECT proxy had no recovery path at all on
// any platform: it captured one *tunnel.Session at construction and held it
// forever, so the first time that connection died every request answered 502 for
// the rest of the process's life - even though the underlying ConnPool had
// already redialed a healthy replacement.
func (s *HTTPProxyServer) SetSessionRefresher(fn func() (*tunnel.Session, error)) {
	s.sessionMu.Lock()
	s.refreshSession = fn
	s.sessionMu.Unlock()
}

// currentSession returns the proxy's session, transparently replacing a dead one.
// Mirrors SOCKS5Server.currentSession, including the cooldown that keeps a
// prolonged outage from turning every request into its own redial attempt.
func (s *HTTPProxyServer) currentSession() *tunnel.Session {
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
		log.Printf("[http-proxy] session refresh failed: %v", err)
		return s.session
	}
	log.Printf("[http-proxy] recovered with a fresh session after the previous one died")
	s.session = fresh
	return s.session
}

func (s *HTTPProxyServer) Start() error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen http proxy: %w", err)
	}
	defer listener.Close()

	log.Printf("[http-proxy] listening on %s", s.addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("[http-proxy] accept error: %v", err)
			continue
		}
		go s.handleClient(conn)
	}
}

func (s *HTTPProxyServer) handleClient(conn net.Conn) {
	defer conn.Close()

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	if req.Method == http.MethodConnect {
		s.handleConnect(conn, req)
	} else {
		s.handleHTTP(conn, req)
	}
}

func (s *HTTPProxyServer) handleConnect(conn net.Conn, req *http.Request) {
	target := req.Host
	if !strings.Contains(target, ":") {
		target = target + ":443"
	}

	stream, err := s.currentSession().Open(target)
	if err != nil {
		fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer stream.Close()

	fmt.Fprintf(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	pipe(conn, stream)
}

func (s *HTTPProxyServer) handleHTTP(conn net.Conn, req *http.Request) {
	host := req.Host
	if !strings.Contains(host, ":") {
		host = host + ":80"
	}

	stream, err := s.currentSession().Open(host)
	if err != nil {
		fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer stream.Close()

	req.RequestURI = ""
	req.Write(stream)

	pipe(conn, stream)
}
