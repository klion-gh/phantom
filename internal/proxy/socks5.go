package proxy

import (
	"fmt"
	"io"
	"log"
	"net"

	"phantom/internal/tunnel"
)

type SOCKS5Server struct {
	addr    string
	session *tunnel.Session
}

func NewSOCKS5Server(addr string, session *tunnel.Session) *SOCKS5Server {
	return &SOCKS5Server{
		addr:    addr,
		session: session,
	}
}

func (s *SOCKS5Server) Start() error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen socks5: %w", err)
	}
	defer listener.Close()

	log.Printf("[socks5] listening on %s", s.addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("[socks5] accept error: %v", err)
			continue
		}

		go s.handleClient(conn)
	}
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

	stream, err := s.session.Open(target)
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
