// Package relay is the entry point a client can put in front of its real
// server (cmd/relay): a TCP forwarder that picks where each connection goes
// from the SNI in its TLS ClientHello and from then on copies bytes both ways.
//
// It holds no keys and decrypts nothing. The client still runs TLS with the
// real server's certificate and the disguised handshake with the real
// server's keys - the relay only ever sees a ClientHello and ciphertext. What
// it changes is the address the client's network sees: a relay inside the
// country is reachable where a foreign server's IP is blocked or filtered.
package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"phantom/internal/logx"
)

// Config is relay.yaml.
type Config struct {
	// Listen is every address the relay accepts on. Clients reach it on
	// whichever of these their network lets through.
	Listen []string `yaml:"listen"`
	// TargetPort is the port on the SNI's host connections go to - the port a
	// Phantom server listens on by default.
	TargetPort int `yaml:"target_port"`
	// Allow, if non-empty, is the only domains the relay forwards to:
	// "vpn.example" matches exactly, "*.example" any subdomain. Empty forwards
	// to any domain that resolves to a public address.
	Allow []string `yaml:"allow"`
	// Routes sends a domain somewhere other than domain:target_port, for a
	// server on another port or one whose domain points elsewhere.
	Routes map[string]string `yaml:"routes"`
	// AllowPrivate lets targets be private addresses too, for a relay that
	// reaches its server over a private network (the same cloud VPC, or the
	// same machine on another port).
	AllowPrivate bool `yaml:"allow_private"`
	// MaxConns caps connections in flight; past it new ones are refused.
	MaxConns int    `yaml:"max_conns"`
	LogLevel string `yaml:"log_level"`
}

// ParseConfig reads relay.yaml, filling in defaults.
func ParseConfig(data []byte) (*Config, error) {
	cfg := &Config{
		Listen:     []string{":443", ":8443"},
		TargetPort: 8443,
		MaxConns:   4096,
		LogLevel:   "info",
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if len(cfg.Listen) == 0 {
		return nil, errors.New("listen is empty")
	}
	if cfg.TargetPort < 1 || cfg.TargetPort > 65535 {
		return nil, fmt.Errorf("target_port out of range: %d", cfg.TargetPort)
	}
	for domain, target := range cfg.Routes {
		if _, _, err := net.SplitHostPort(target); err != nil {
			return nil, fmt.Errorf("route for %s must be host:port: %w", domain, err)
		}
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 4096
	}
	return cfg, nil
}

const (
	helloTimeout = 10 * time.Second
	dialTimeout  = 10 * time.Second
	keepAlive    = 30 * time.Second
)

// Relay forwards accepted connections. One serves any number of listeners.
type Relay struct {
	cfg   *Config
	slots chan struct{}
	// ownPorts are the ports this relay listens on: a target on this
	// machine at one of them would be the relay connecting to itself.
	ownPorts map[string]bool

	// lookup resolves a target host; isLocal reports whether an IP belongs
	// to this machine. Tests swap out lookup.
	lookup  func(ctx context.Context, host string) ([]net.IP, error)
	isLocal func(net.IP) bool
}

// New returns a relay for cfg.
func New(cfg *Config) *Relay {
	own := map[string]bool{}
	for _, l := range cfg.Listen {
		if _, port, err := net.SplitHostPort(l); err == nil {
			own[port] = true
		}
	}
	return &Relay{
		cfg:      cfg,
		slots:    make(chan struct{}, cfg.MaxConns),
		ownPorts: own,
		lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		isLocal: isLocalIP,
	}
}

// Serve accepts on ln until ctx is done or ln fails.
func (r *Relay) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		select {
		case r.slots <- struct{}{}:
		default:
			logx.Warnf("[relay] %d connections in flight, refusing one", r.cfg.MaxConns)
			c.Close()
			continue
		}
		go func() {
			defer func() { <-r.slots }()
			r.handle(ctx, c)
		}()
	}
}

func (r *Relay) handle(ctx context.Context, c net.Conn) {
	defer c.Close()
	setKeepAlive(c)

	c.SetReadDeadline(time.Now().Add(helloTimeout))
	sni, hello, err := readClientHello(c)
	c.SetReadDeadline(time.Time{})
	if err != nil {
		logx.Debugf("[relay] %s: no usable ClientHello: %v", c.RemoteAddr(), err)
		return
	}

	target, err := r.target(ctx, sni)
	if err != nil {
		logx.Infof("[relay] %s: not forwarding %q: %v", c.RemoteAddr(), sni, err)
		return
	}

	d := net.Dialer{Timeout: dialTimeout, KeepAlive: keepAlive}
	up, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		logx.Infof("[relay] %s -> %s (%s): %v", c.RemoteAddr(), sni, target, err)
		return
	}
	defer up.Close()
	logx.Debugf("[relay] %s -> %s (%s)", c.RemoteAddr(), sni, target)

	if _, err := up.Write(hello); err != nil {
		return
	}
	pipe(c, up)
}

// target picks where a connection for sni goes: its route if it has one,
// otherwise sni:target_port at the first public address sni resolves to.
func (r *Relay) target(ctx context.Context, sni string) (string, error) {
	sni = strings.ToLower(strings.TrimSuffix(sni, "."))
	if !r.allowed(sni) {
		return "", errors.New("not in allow")
	}
	host, port := sni, strconv.Itoa(r.cfg.TargetPort)
	if route, ok := r.cfg.Routes[sni]; ok {
		host, port, _ = net.SplitHostPort(route)
	}

	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var err error
		ips, err = r.lookup(lookupCtx, host)
		cancel()
		if err != nil {
			return "", err
		}
	}
	// A domain resolving into a private network would have the relay reach
	// whatever sits behind it rather than a Phantom server on the internet;
	// one resolving to this machine on a port the relay itself listens on
	// would loop.
	for _, ip := range ips {
		if !r.cfg.AllowPrivate && !isPublicIP(ip) {
			continue
		}
		if r.ownPorts[port] && (ip.IsLoopback() || r.isLocal(ip)) {
			continue
		}
		return net.JoinHostPort(ip.String(), port), nil
	}
	return "", errors.New("resolves to no usable address")
}

func (r *Relay) allowed(sni string) bool {
	if len(r.cfg.Allow) == 0 {
		return true
	}
	for _, a := range r.cfg.Allow {
		a = strings.ToLower(a)
		if suffix, ok := strings.CutPrefix(a, "*."); ok {
			if strings.HasSuffix(sni, "."+suffix) {
				return true
			}
		} else if sni == a {
			return true
		}
	}
	return false
}

// readClientHello reads the client's first flight and returns its SNI along
// with every byte read, which is passed on as it is. crypto/tls does the
// parsing; the handshake is abandoned the moment the ClientHello is in.
func readClientHello(c net.Conn) (string, []byte, error) {
	rec := &recordingConn{Conn: c}
	var sni string
	errGotHello := errors.New("got ClientHello")
	err := tls.Server(rec, &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			sni = h.ServerName
			return nil, errGotHello
		},
	}).Handshake()
	if !errors.Is(err, errGotHello) {
		if err == nil {
			err = errors.New("handshake did not stop at the ClientHello")
		}
		return "", nil, err
	}
	if sni == "" {
		return "", nil, errors.New("no SNI")
	}
	return sni, rec.buf.Bytes(), nil
}

// recordingConn keeps a copy of everything read and drops every write, so
// the alert crypto/tls sends when the handshake is abandoned never reaches
// the client.
type recordingConn struct {
	net.Conn
	buf bytes.Buffer
}

func (c *recordingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.buf.Write(p[:n])
	return n, err
}

func (c *recordingConn) Write(p []byte) (int, error) { return len(p), nil }

// pipe copies both ways until both directions are done, passing a half-close
// on so each side sees the other's EOF.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyThenCloseWrite(b, a) }()
	go func() { defer wg.Done(); copyThenCloseWrite(a, b) }()
	wg.Wait()
}

func copyThenCloseWrite(dst, src net.Conn) {
	_, err := io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok && err == nil {
		cw.CloseWrite()
		return
	}
	// A failed copy tears down both: the other direction would otherwise
	// sit on a connection nothing will ever write to again.
	dst.Close()
	src.Close()
}

func setKeepAlive(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(keepAlive)
	}
}

func isPublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast())
}

func isLocalIP(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}
