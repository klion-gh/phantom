package proxy

import (
	"errors"
	"io"
	"net"
	"time"

	"phantom/internal/logx"
	"phantom/internal/tunnel"
)

// Every log line here that names a destination goes through logx at Debug
// level, never Info. This is the server side of the tunnel: at Info (the
// default) it was writing "connected to <host:port>" for every single stream
// every user opened, unconditionally and with no way to turn it off - i.e. the
// server kept a timestamped browsing history of all its users in the systemd
// journal. For a tool whose entire purpose is that such a record shouldn't
// exist, that was the most damaging thing on the box: seizing the server got
// the adversary the logs. An operator diagnosing a routing problem can still
// opt in with log_level: debug, and now knows what they're turning on.

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
		logx.Warnf("[direct] empty target")
		return
	}

	if stream.IsUDP() {
		d.handleUDPStream(stream, target)
		return
	}

	conn, err := net.DialTimeout("tcp", target, d.timeout)
	if err != nil {
		logx.Debugf("[direct] dial %s failed: %v", target, err)
		return
	}
	defer conn.Close()

	logx.Debugf("[direct] connected to %s", target)

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
		logx.Debugf("[direct] udp dial %s failed: %v", target, err)
		return
	}
	udpConn := conn.(*net.UDPConn)
	defer udpConn.Close()

	logx.Debugf("[direct] udp connected to %s", target)

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
				// A datagram too large for one frame is dropped, not fatal. This
				// is the path any host on the internet could reach by answering
				// with a ~64KB UDP datagram, which used to overflow the frame
				// header and desynchronise the tunnel for every stream on the
				// connection (see protocol.MaxFramePayload).
				if errors.Is(err, tunnel.ErrDatagramTooLarge) {
					continue
				}
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
