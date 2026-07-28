package proxy

import (
	"errors"
	"fmt"
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
// addresses on the server. See blockedIP for why it's off by default.
func (d *DirectOutbound) AllowLocalTargets(allow bool) {
	d.allowLocal = allow
}

// blockedIP reports whether a resolved address is somewhere a tunnelled client
// has no business reaching through this server.
//
// Two ranges matter. Loopback is the server's own private surface: an SSH daemon
// bound to 127.0.0.1, a database, an admin endpoint deliberately not exposed to
// the internet - all of it reachable by anyone holding the PSK, which is a shared
// secret handed to every user. Link-local covers 169.254.169.254, the cloud
// metadata endpoint, where on most providers a plain unauthenticated GET returns
// credentials for the whole VPS.
//
// RFC1918 ranges are deliberately NOT blocked: a personal VPN is a legitimate way
// to reach one's own LAN, and blocking that would break a real use case to
// mitigate a threat the PSK holder doesn't have (they're already inside).
func blockedIP(ip net.IP) (string, bool) {
	switch {
	case ip.IsLoopback():
		return "loopback", true
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "link-local (cloud metadata)", true
	case ip.IsUnspecified():
		return "unspecified", true
	}
	return "", false
}

// resolveAllowed resolves target exactly once and returns dialable ip:port
// addresses, all of them already checked.
//
// Resolving once is the whole point. The first version of this guard resolved the
// name to decide whether to allow it and then handed the *name* to net.Dial,
// which resolved it again independently - two separate DNS queries with nothing
// binding them together. A domain under an attacker's control answers the first
// with a public address and the second with 127.0.0.1, and the check is bypassed
// entirely: textbook DNS rebinding, defeating the guard against exactly the
// adversary it exists for. Callers now dial the verified address, so there is no
// second lookup to disagree with the first. It also halves the DNS traffic every
// tunnelled connection generates.
//
// If any resolved address is blocked, the whole target is refused rather than
// filtered down to the allowed ones: a name that resolves to both a public
// address and loopback is not a name with an innocent explanation.
func resolveAllowed(target string, allowLocal bool) ([]string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("bad target %q: %w", target, err)
	}

	// An IP literal parses here and skips the resolver entirely.
	if ip := net.ParseIP(host); ip != nil {
		if !allowLocal {
			if reason, blocked := blockedIP(ip); blocked {
				return nil, fmt.Errorf("refused %s target", reason)
			}
		}
		return []string{net.JoinHostPort(ip.String(), port)}, nil
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve %q: no addresses", host)
	}

	addrs := make([]string, 0, len(ips))
	for _, ip := range ips {
		if !allowLocal {
			if reason, blocked := blockedIP(ip); blocked {
				return nil, fmt.Errorf("refused %s target", reason)
			}
		}
		addrs = append(addrs, net.JoinHostPort(ip.String(), port))
	}
	return addrs, nil
}

// dialChecked resolves target once and dials the resulting addresses in order.
// Trying each preserves the fallback behaviour dialing a hostname used to get for
// free from the resolver returning several addresses.
func (d *DirectOutbound) dialChecked(network, target string) (net.Conn, error) {
	addrs, err := resolveAllowed(target, d.allowLocal)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, addr := range addrs {
		conn, err := net.DialTimeout(network, addr, d.timeout)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
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

	conn, err := d.dialChecked("tcp", target)
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
	conn, err := d.dialChecked("udp", target)
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
