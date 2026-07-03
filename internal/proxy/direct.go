package proxy

import (
	"io"
	"log"
	"net"
	"time"

	"phantom/internal/tunnel"
)

type DirectOutbound struct {
	timeout time.Duration
}

func NewDirectOutbound(timeout time.Duration) *DirectOutbound {
	return &DirectOutbound{timeout: timeout}
}

func (d *DirectOutbound) HandleStream(stream *tunnel.Stream) {
	defer stream.Close()

	target := stream.Target()
	if target == "" {
		log.Printf("[direct] empty target")
		return
	}

	if stream.IsUDP() {
		d.handleUDPStream(stream, target)
		return
	}

	conn, err := net.DialTimeout("tcp", target, d.timeout)
	if err != nil {
		log.Printf("[direct] dial %s failed: %v", target, err)
		return
	}
	defer conn.Close()

	log.Printf("[direct] connected to %s", target)

	done := make(chan struct{})

	go func() {
		io.Copy(conn, stream)
		conn.Close()
		close(done)
	}()

	io.Copy(stream, conn)
	stream.Close()
	<-done
}

const udpIdleTimeout = 60 * time.Second

func (d *DirectOutbound) handleUDPStream(stream *tunnel.Stream, target string) {
	conn, err := net.DialTimeout("udp", target, d.timeout)
	if err != nil {
		log.Printf("[direct] udp dial %s failed: %v", target, err)
		return
	}
	udpConn := conn.(*net.UDPConn)
	defer udpConn.Close()

	log.Printf("[direct] udp connected to %s", target)

	done := make(chan struct{})

	go func() {
		defer close(done)
		buf := make([]byte, 65535)
		for {
			udpConn.SetReadDeadline(time.Now().Add(udpIdleTimeout))
			n, err := udpConn.Read(buf)
			if err != nil {
				stream.Close()
				return
			}
			if _, err := stream.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	buf := make([]byte, 65535)
	for {
		n, err := stream.Read(buf)
		if err != nil {
			break
		}
		udpConn.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		if _, err := udpConn.Write(buf[:n]); err != nil {
			break
		}
	}

	udpConn.Close()
	<-done
}
