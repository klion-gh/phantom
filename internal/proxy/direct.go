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
	// allowLocal disables the guard below. Off by default; an operator who
	// genuinely wants clients to reach services on the VPN host itself can turn
	// it on.
	allowLocal bool
}

func NewDirectOutbound(timeout time.Duration) *DirectOutbound {
	return &DirectOutbound{timeout: timeout}
}

// AllowLocalTargets lets tunnelled traffic reach loopback and link-local
// addresses on the server. See blockedTarget for why it's off by default.
func (d *DirectOutbound) AllowLocalTargets(allow bool) {
	d.allowLocal = allow
}

// blockedTarget reports whether target resolves somewhere a tunnelled client has
// no business reaching through this server.
//
// Two ranges matter. Loopback is the server's own private surface: an SSH daemon
// bound to 127.0.0.1, a database, an admin endpoint deliberately not exposed to
// the internet - all of it was reachable by anyone holding the PSK, which is a
// shared secret handed to every user. Link-local covers 169.254.169.254, the
// cloud metadata endpoint, where on most providers a plain unauthenticated GET
// returns credentials for the whole VPS.
//
// RFC1918 ranges are deliberately NOT blocked: a personal VPN is a legitimate way
// to reach one's own LAN, and blocking that would break a real use case to
// mitigate a threat the PSK holder doesn't have (they're already inside).
func blockedTarget(target string, allowLocal bool) (string, bool) {
	if allowLocal {
		return "", false
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return "", false
	}
	// Resolve rather than pattern-match the literal: "localhost", a domain with
	// an A record of 127.0.0.1, and a decimal-encoded address all reach the same
	// place, and only resolution catches all three.
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", false // let the dial fail on its own terms
	}
	for _, ip := range ips {
		switch {
		case ip.IsLoopback():
			return "loopback", true
		case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
			return "link-local (cloud metadata)", true
		case ip.IsUnspecified():
			return "unspecified", true
		}
	}
	return "", false
}

func (d *DirectOutbound) HandleStream(stream *tunnel.Stream) {
	defer stream.Close()

	target := stream.Target()
	if target == "" {
		logx.Warnf("[direct] empty target")
		return
	}

	if reason, blocked := blockedTarget(target, d.allowLocal); blocked {
		logx.Warnf("[direct] refused %s target", reason)
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
