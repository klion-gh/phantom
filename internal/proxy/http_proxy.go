package proxy

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"phantom/internal/tunnel"
)

type HTTPProxyServer struct {
	addr    string
	session *tunnel.Session
}

func NewHTTPProxyServer(addr string, session *tunnel.Session) *HTTPProxyServer {
	return &HTTPProxyServer{
		addr:    addr,
		session: session,
	}
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

	stream, err := s.session.Open(target)
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

	stream, err := s.session.Open(host)
	if err != nil {
		fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer stream.Close()

	req.RequestURI = ""
	req.Write(stream)

	pipe(conn, stream)
}
