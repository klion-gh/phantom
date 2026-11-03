package transport

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"phantom/internal/handshake"
	"phantom/internal/logx"
	"phantom/internal/protocol"
)

// The tunnel inside HTTP/2.
//
// Over HTTP/1.1 the handshake is a WebSocket upgrade: a 282-byte request and
// a 225-byte 101 answer, the same pair of records at the start of every
// connection, which flowlab separated from browsers on by itself. When the
// server negotiates h2 the client instead opens an ordinary HTTP/2
// connection - preface, SETTINGS, a request - and runs the same handshake as
// one POST: the auth cookie goes in its headers, the server answers 200 with
// its ephemeral key in a Set-Cookie, and from then on the request body
// carries the tunnel upstream and the response body downstream. Inside TLS a
// passive observer sees an HTTP/2 connection opening like any other.
//
// The multiplexer, the frames and the keys are exactly those of the HTTP/1.1
// path; only the carrier differs, so WireVersion stays 2. ALPN picks the
// carrier: a server offering h2 gets this, one offering only http/1.1 (every
// server before 2.3) gets the upgrade.

const h2ContentType = "application/octet-stream"

// h2UserAgent matches the Firefox ClientHello the client mimics.
const h2UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0"

// clientH2 runs the handshake as an HTTP/2 request over conn, which has
// negotiated h2, and returns the request's stream as the tunnel connection.
func clientH2(conn net.Conn, domain string, psk, serverPub []byte, exporter handshake.ExportKeyingMaterial) (net.Conn, *protocol.SessionCrypto, error) {
	auth, err := handshake.NewClientAuth(psk, serverPub, exporter)
	if err != nil {
		return nil, nil, err
	}

	cc, err := (&http2.Transport{}).NewClientConn(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("h2 connection: %w", err)
	}

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, "https://"+domain+auth.Path, pr)
	if err != nil {
		cc.Close()
		return nil, nil, err
	}
	req.ContentLength = -1
	req.Header.Set("Cookie", auth.Cookie)
	req.Header.Set("Content-Type", h2ContentType)
	req.Header.Set("User-Agent", h2UserAgent)
	req.Header.Set("Accept", "*/*")

	// Returns once the response headers are in; the body keeps flowing both
	// ways after that.
	resp, err := cc.RoundTrip(req)
	if err != nil {
		pw.Close()
		cc.Close()
		return nil, nil, fmt.Errorf("h2 request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		pw.Close()
		cc.Close()
		return nil, nil, fmt.Errorf("%w: server answered %d", handshake.ErrAuthFailed, resp.StatusCode)
	}
	crypto, err := auth.Finish(resp)
	if err != nil {
		resp.Body.Close()
		pw.Close()
		cc.Close()
		return nil, nil, err
	}
	return &h2ClientConn{base: conn, cc: cc, body: resp.Body, pw: pw}, crypto, nil
}

// h2ClientConn is the client end of the tunnel stream as a net.Conn: reads
// come from the response body, writes go into the request body.
type h2ClientConn struct {
	base net.Conn
	cc   *http2.ClientConn
	body io.ReadCloser
	pw   *io.PipeWriter
	once sync.Once
}

func (c *h2ClientConn) Read(p []byte) (int, error)  { return c.body.Read(p) }
func (c *h2ClientConn) Write(p []byte) (int, error) { return c.pw.Write(p) }

func (c *h2ClientConn) Close() error {
	c.once.Do(func() {
		c.pw.Close()
		c.body.Close()
		c.cc.Close()
		c.base.Close()
	})
	return nil
}

func (c *h2ClientConn) LocalAddr() net.Addr                { return c.base.LocalAddr() }
func (c *h2ClientConn) RemoteAddr() net.Addr               { return c.base.RemoteAddr() }
func (c *h2ClientConn) SetDeadline(t time.Time) error      { return c.base.SetDeadline(t) }
func (c *h2ClientConn) SetReadDeadline(t time.Time) error  { return c.base.SetReadDeadline(t) }
func (c *h2ClientConn) SetWriteDeadline(t time.Time) error { return c.base.SetWriteDeadline(t) }

// serveH2 answers a connection that negotiated h2 and sent the HTTP/2
// preface: an authenticated POST becomes the tunnel, everything else gets
// the decoy site - for as long as the client keeps the connection.
func serveH2(c net.Conn, tlsConn *tls.Conn, cfg *TLSServerConfig, handler func(net.Conn, *protocol.SessionCrypto)) {
	exporter := func(label string, context []byte, length int) ([]byte, error) {
		state := tlsConn.ConnectionState()
		return state.ExportKeyingMaterial(label, context, length)
	}
	ip, _, _ := net.SplitHostPort(c.RemoteAddr().String())

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && handler != nil {
			// A POST is what an auth attempt looks like, so it spends the
			// per-IP budget (see ratelimit.go); over it, the decoy answers
			// without the attempt being tried.
			if !cfg.limiter.allow(ip) {
				metricRateLimited.Add(1)
				cfg.Decoy.ServeHTTP(w, r)
				return
			}
			crypto, answer, err := handshake.VerifyRequest(r, cfg.PSK, cfg.ServerPriv, cfg.ServerPub, exporter)
			if err != nil {
				logx.Warnf("[server] h2 handshake: %v", err)
			}
			if crypto != nil {
				serveH2Tunnel(w, r, c, crypto, answer, handler)
				return
			}
		}
		metricDecoyHits.Add(1)
		cfg.Decoy.ServeHTTP(w, r)
	})

	srv := &http2.Server{
		IdleTimeout: 60 * time.Second,
		// Room for a fast upload on a long path: the defaults (1 MB) cap one
		// stream at about 1 MB per round trip.
		MaxUploadBufferPerConnection: 16 << 20,
		MaxUploadBufferPerStream:     16 << 20,
	}
	srv.ServeConn(c, &http2.ServeConnOpts{Handler: h})
}

// serveH2Tunnel answers an authenticated request and runs the tunnel on its
// stream until either end closes it.
func serveH2Tunnel(w http.ResponseWriter, r *http.Request, base net.Conn, crypto *protocol.SessionCrypto, answer *http.Cookie, handler func(net.Conn, *protocol.SessionCrypto)) {
	w.Header().Set("Content-Type", h2ContentType)
	http.SetCookie(w, answer)
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return
	}

	sc := &h2ServerConn{base: base, w: w, rc: rc, body: r.Body}
	metricHandshakeOK.Add(1)
	metricActiveNow.Add(1)
	defer metricActiveNow.Add(-1)
	handler(sc, crypto)
	sc.Close()
}

// h2ServerConn is the server end of the tunnel stream as a net.Conn: reads
// come from the request body, writes go out as response data, flushed at
// once. Closing it ends only this stream; the handler returning is what
// finishes it on the wire.
type h2ServerConn struct {
	base   net.Conn
	w      http.ResponseWriter
	rc     *http.ResponseController
	body   io.ReadCloser
	mu     sync.Mutex
	closed bool
}

var errH2StreamClosed = errors.New("h2 tunnel stream closed")

func (c *h2ServerConn) Read(p []byte) (int, error) { return c.body.Read(p) }

func (c *h2ServerConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The ResponseWriter must not be touched once the handler has returned;
	// Close (before it returns) waits for a write in progress, then bars more.
	if c.closed {
		return 0, errH2StreamClosed
	}
	n, err := c.w.Write(p)
	if err == nil {
		err = c.rc.Flush()
	}
	return n, err
}

func (c *h2ServerConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.body.Close()
}

func (c *h2ServerConn) LocalAddr() net.Addr                { return c.base.LocalAddr() }
func (c *h2ServerConn) RemoteAddr() net.Addr               { return c.base.RemoteAddr() }
func (c *h2ServerConn) SetDeadline(t time.Time) error      { return nil }
func (c *h2ServerConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *h2ServerConn) SetWriteDeadline(t time.Time) error { return nil }
