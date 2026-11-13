// Package netcheck answers the two questions "keep the connection up"
// (internal/linkguard) asks over and over: does this device have internet
// without the VPN at all, and does a given path - the live tunnel, or a
// saved config dialed on its own - actually carry traffic to the internet?
//
// The second one is not the same as "the server answers a handshake": the
// failure this exists for is a tunnel that still looks connected after a
// network blip while nothing gets through it. So a path is checked by
// fetching a tiny page through it - a stream opened through the server to a
// well-known connectivity-check URL, the same kind phones and browsers use to
// decide whether a network has internet.
package netcheck

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Target is one connectivity-check page: where to open the stream (host:port,
// resolved by the server at the far end of the tunnel) and what to ask for.
type Target struct {
	Addr string
	Host string
	Path string
}

// TunnelTargets are fetched through a server. Plain HTTP on purpose: the
// stream itself already rides the tunnel's TLS, and a status line is all
// that's needed - any HTTP answer means the server reached the internet.
// Two operators, so one of them being down isn't read as the tunnel being down.
var TunnelTargets = []Target{
	{Addr: "connectivitycheck.gstatic.com:80", Host: "connectivitycheck.gstatic.com", Path: "/generate_204"},
	{Addr: "cp.cloudflare.com:80", Host: "cp.cloudflare.com", Path: "/generate_204"},
}

// DirectTargets are dialed around the VPN to tell whether the device has any
// internet at all. Bare addresses, so the answer doesn't depend on DNS -
// which is often the first thing to go in a blip - and Russian resolvers
// first: those stay reachable even while mobile internet is cut down to an
// allowlist, which is still "the network works, the VPN doesn't".
var DirectTargets = []string{
	"77.88.8.8:53", // Yandex DNS
	"77.88.8.1:53", // Yandex DNS
	"1.1.1.1:443",  // Cloudflare
	"8.8.8.8:443",  // Google
}

// ErrNoAnswer is a stream that opened but never produced an HTTP answer.
var ErrNoAnswer = errors.New("no HTTP answer")

// HTTP sends one GET for t over rw and waits for the response's status line,
// closing rw when ctx ends so a stream that never answers can't hang the
// caller. rw is closed on return either way.
func HTTP(ctx context.Context, rw io.ReadWriteCloser, t Target) error {
	stop := context.AfterFunc(ctx, func() { rw.Close() })
	defer stop()
	defer rw.Close()

	req := "GET " + t.Path + " HTTP/1.1\r\nHost: " + t.Host +
		"\r\nUser-Agent: Mozilla/5.0\r\nAccept: */*\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(rw, req); err != nil {
		return ctxErr(ctx, err)
	}
	line, err := bufio.NewReaderSize(rw, 512).ReadString('\n')
	if err != nil && line == "" {
		return ctxErr(ctx, fmt.Errorf("%w: %v", ErrNoAnswer, err))
	}
	if !strings.HasPrefix(line, "HTTP/") {
		return fmt.Errorf("%w: got %q", ErrNoAnswer, strings.TrimSpace(line))
	}
	return nil
}

// Through checks one path: open is how a stream to a target is opened on it
// (a session's Open, a multiplexer's Open). The targets are tried in turn
// until one answers; the time to that answer is returned.
func Through(ctx context.Context, open func(addr string) (io.ReadWriteCloser, error)) (time.Duration, error) {
	start := time.Now()
	var lastErr error
	for _, t := range TunnelTargets {
		if ctx.Err() != nil {
			break
		}
		rw, err := open(t.Addr)
		if err != nil {
			lastErr = err
			continue
		}
		if err := HTTP(ctx, rw, t); err != nil {
			lastErr = err
			continue
		}
		return time.Since(start), nil
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return 0, lastErr
}

// Dialer is how Direct opens a connection - a net.Dialer whose sockets are
// taken out of the VPN's routing.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Direct reports whether any of DirectTargets accepts a TCP connection over
// d - dialed all at once, the first to connect answers.
func Direct(ctx context.Context, d Dialer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(DirectTargets))
	for _, addr := range DirectTargets {
		go func(addr string) {
			conn, err := d.DialContext(ctx, "tcp", addr)
			if err == nil {
				conn.Close()
			}
			errs <- err
		}(addr)
	}
	var lastErr error
	for range DirectTargets {
		err := <-errs
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("no internet without the VPN: %w", lastErr)
}

func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
