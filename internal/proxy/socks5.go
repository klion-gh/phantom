package proxy

import (
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"phantom/internal/tunnel"
)

// How often currentSession is allowed to actually attempt a refresh while
// the session is dead - mirrors netstack.Tunnel's own cooldown so a
// prolonged outage doesn't turn every single SOCKS5 request into its own
// redial attempt against a server that's still unreachable.
const sessionRefreshCooldown = 3 * time.Second

type SOCKS5Server struct {
	addr string

	sessionMu          sync.Mutex
	session            *tunnel.Session
	refreshSession     func() (*tunnel.Session, error)
	lastRefreshAttempt time.Time

	mu       sync.Mutex
	listener net.Listener
	stopped  bool
}

func NewSOCKS5Server(addr string, session *tunnel.Session) *SOCKS5Server {
	return &SOCKS5Server{
		addr:    addr,
		session: session,
	}
}

// SetSessionRefresher installs an optional hook that lets the server recover
// from its one connection to the Phantom server dying - a brief Wi-Fi blip,
// the full-tunnel VPN's own TUN interface briefly capturing and then losing
// this connection when toggled on/off, a server-side hiccup - by fetching a
// fresh session (typically pool.Get() wrapped in tunnel.NewSessionFromMux)
// on demand. Without this, once that one connection died, every SOCKS5
// request would fail forever, even after the underlying transport.ConnPool
// had already quietly redialed a healthy replacement in the background -
// requiring a manual proxy restart to recover instead of the pool's own
// self-healing actually being put to use. Mirrors
// netstack.Tunnel.SetSessionRefresher exactly.
func (s *SOCKS5Server) SetSessionRefresher(fn func() (*tunnel.Session, error)) {
	s.sessionMu.Lock()
	s.refreshSession = fn
	s.sessionMu.Unlock()
}

// currentSession returns the server's session, transparently replacing it
// with a fresh one if the current one has died. Safe for concurrent use;
// every SOCKS5 request goes through this.
func (s *SOCKS5Server) currentSession() *tunnel.Session {
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
		log.Printf("[socks5] session refresh failed: %v", err)
		return s.session
	}
	log.Printf("[socks5] recovered with a fresh session after the previous one died")
	s.session = fresh
	return s.session
}

// Start binds addr and serves until Stop is called (returns nil) or a real
// accept error occurs. Kept as a single blocking call for callers (cmd/client)
// that just run it for the process's whole lifetime with no need to read
// back the bound address first - see Listen/Serve for callers that do (e.g.
// binding to port 0 and needing the OS-assigned port before traffic starts).
func (s *SOCKS5Server) Start() error {
	listener, err := s.Listen()
	if err != nil {
		return err
	}
	return s.Serve(listener)
}

// Listen binds addr without serving yet, so a caller using port 0 (OS picks
// a free port) can read the actual port back via Addr() before calling Serve.
func (s *SOCKS5Server) Listen() (net.Listener, error) {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("listen socks5: %w", err)
	}
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()
	return listener, nil
}

// Serve accepts connections on listener (from Listen, or Start's own) until
// Stop closes it, at which point it returns nil rather than looping on the
// resulting accept error forever.
func (s *SOCKS5Server) Serve(listener net.Listener) error {
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	log.Printf("[socks5] listening on %s", listener.Addr())

	for {
		conn, err := listener.Accept()
		if err != nil {
			s.mu.Lock()
			stopped := s.stopped
			s.mu.Unlock()
			if stopped {
				return nil
			}
			log.Printf("[socks5] accept error: %v", err)
			return err
		}

		go s.handleClient(conn)
	}
}

// Stop closes the listener, ending Start/Serve's accept loop. Safe to call
// even if the listener was never bound (e.g. Listen itself failed).
func (s *SOCKS5Server) Stop() {
	s.mu.Lock()
	s.stopped = true
	listener := s.listener
	s.mu.Unlock()
	if listener != nil {
		listener.Close()
	}
}

// Addr returns the actual bound address (nil until Listen/Start succeeds) -
// mainly useful when addr passed to NewSOCKS5Server ends in ":0" and the
// caller needs to know which port the OS actually picked.
func (s *SOCKS5Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
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

	// Request header: VER, CMD, RSV, ATYP.
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return
	}
	if buf[0] != 0x05 {
		return
	}
	cmd := buf[1]
	atyp := buf[3]

	host, port, err := readAddr(conn, atyp, buf)
	if err != nil {
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // address type not supported
		return
	}

	switch cmd {
	case 0x01: // CONNECT
		target := net.JoinHostPort(host, strconv.Itoa(int(port)))
		stream, err := s.currentSession().Open(target)
		if err != nil {
			conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
		defer stream.Close()
		conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		pipe(conn, stream)
	case 0x03: // UDP ASSOCIATE
		// The DST.ADDR/PORT the client sent here (host/port above) is ignored -
		// per RFC 1928 the real per-datagram destinations arrive in each UDP
		// packet's own header. We just need the control conn to stay open for
		// the association's lifetime.
		s.handleUDPAssociate(conn)
	default:
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // command not supported
	}
}

// readAddr reads the ATYP-typed address + 2-byte port that follows a SOCKS5
// request header (or a UDP datagram header). atyp is the byte already read as
// part of the fixed header; buf is scratch space (>=256 bytes).
func readAddr(conn io.Reader, atyp byte, buf []byte) (host string, port uint16, err error) {
	switch atyp {
	case 0x01: // IPv4
		if _, err = io.ReadFull(conn, buf[:4]); err != nil {
			return "", 0, err
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // domain name
		if _, err = io.ReadFull(conn, buf[:1]); err != nil {
			return "", 0, err
		}
		domainLen := int(buf[0])
		if _, err = io.ReadFull(conn, buf[:domainLen]); err != nil {
			return "", 0, err
		}
		host = string(buf[:domainLen])
	case 0x04: // IPv6
		if _, err = io.ReadFull(conn, buf[:16]); err != nil {
			return "", 0, err
		}
		host = net.IP(buf[:16]).String()
	default:
		return "", 0, fmt.Errorf("unsupported address type %d", atyp)
	}
	if _, err = io.ReadFull(conn, buf[:2]); err != nil {
		return "", 0, err
	}
	return host, uint16(buf[0])<<8 | uint16(buf[1]), nil
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

// udpFlowIdleTimeout evicts a per-target UDP flow that's seen no traffic in
// either direction for this long - mirrors the server-side UDP relay's own
// idle timeout (internal/proxy/direct.go), so a long-lived association that
// touches many one-shot destinations (DNS especially) doesn't accumulate dead
// tunnel streams.
const udpFlowIdleTimeout = 60 * time.Second

// handleUDPAssociate serves a SOCKS5 UDP ASSOCIATE (RFC 1928): it binds a local
// UDP socket, tells the client where to send its datagrams, and relays each one
// through the Phantom tunnel as a UDP stream (session.OpenUDP - the same relay
// the full VPN uses for UDP), fanning replies back. The association lives for as
// long as the control TCP connection (ctrlConn) stays open; when it closes,
// everything here is torn down. This is what lets an app pointed at the proxy
// (e.g. Telegram's voice calls, or plain DNS-over-UDP) use UDP through Phantom,
// not just TCP.
func (s *SOCKS5Server) handleUDPAssociate(ctrlConn net.Conn) {
	// Bind on loopback only - the proxy is always local (127.0.0.1), so only a
	// local app can reach this relay socket.
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		ctrlConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // general failure
		return
	}
	defer udpConn.Close()

	localPort := udpConn.LocalAddr().(*net.UDPAddr).Port
	// Success reply, BND.ADDR = 127.0.0.1, BND.PORT = the relay socket's port.
	// The client sends its UDP datagrams there.
	ctrlConn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, byte(localPort >> 8), byte(localPort)})

	relay := &udpRelay{server: s, udpConn: udpConn, flows: make(map[string]*udpFlow)}
	defer relay.close()
	go relay.readLoop()
	go relay.janitor()

	// Block until the control connection closes; per RFC 1928 that ends the
	// association. Any read result (data we don't expect, or EOF) means we're
	// done - the deferred close/udpConn.Close tear the relay down.
	io.Copy(io.Discard, ctrlConn)
}

// udpRelay carries one UDP ASSOCIATE association: a single local UDP socket
// shared by the client, and one Phantom UDP stream per distinct destination.
type udpRelay struct {
	server  *SOCKS5Server
	udpConn *net.UDPConn

	mu         sync.Mutex
	flows      map[string]*udpFlow // target "host:port" -> tunnel stream
	clientAddr *net.UDPAddr        // where to send replies (the app's own UDP source)
	closed     bool
}

type udpFlow struct {
	stream      *tunnel.Stream
	replyHeader []byte // precomputed SOCKS5 UDP reply header for this target (RSV+FRAG+ATYP+ADDR+PORT)
	lastSeen    time.Time
}

// readLoop reads datagrams the client sends to the relay socket and forwards
// each into the tunnel.
func (r *udpRelay) readLoop() {
	buf := make([]byte, 65535)
	for {
		n, src, err := r.udpConn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed -> association torn down
		}
		r.forwardClientDatagram(buf[:n], src)
	}
}

// forwardClientDatagram parses one SOCKS5 UDP request datagram
// ([RSV(2)][FRAG(1)][ATYP][ADDR][PORT][DATA]) and relays its payload to the
// destination through a per-target tunnel stream, opening one on first use.
func (r *udpRelay) forwardClientDatagram(pkt []byte, src *net.UDPAddr) {
	target, hdr, payload, ok := parseUDPDatagram(pkt)
	if !ok {
		return
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.clientAddr = src
	flow := r.flows[target]
	if flow == nil {
		stream, err := r.server.currentSession().OpenUDP(target)
		if err != nil {
			r.mu.Unlock()
			return
		}
		// Reply header for this target: RSV(0,0) + FRAG(0) + the exact
		// ATYP+ADDR+PORT bytes the client used, so replies echo back the
		// destination the app expects.
		replyHeader := append([]byte{0, 0, 0}, hdr...)
		flow = &udpFlow{stream: stream, replyHeader: replyHeader, lastSeen: time.Now()}
		r.flows[target] = flow
		go r.streamReadLoop(target, flow)
	}
	flow.lastSeen = time.Now()
	stream := flow.stream
	r.mu.Unlock()

	stream.Write(payload) // one datagram == one Write (see internal/tunnel/stream.go)
}

// parseUDPDatagram decodes a SOCKS5 UDP request datagram
// ([RSV(2)][FRAG(1)][ATYP][ADDR][PORT(2)][DATA]). It returns the destination
// "host:port", the ATYP+ADDR+PORT header bytes (for building the reply header),
// the payload, and ok=false for anything malformed or fragmented (FRAG != 0,
// which we don't support). hdr and payload alias pkt - copy before retaining.
func parseUDPDatagram(pkt []byte) (target string, hdr, payload []byte, ok bool) {
	if len(pkt) < 4 || pkt[2] != 0 {
		return "", nil, nil, false
	}
	var host string
	var addrEnd int // index just past ADDR (start of the 2-byte port)
	switch pkt[3] {
	case 0x01: // IPv4
		if len(pkt) < 4+4+2 {
			return "", nil, nil, false
		}
		host = net.IP(pkt[4:8]).String()
		addrEnd = 8
	case 0x03: // domain
		if len(pkt) < 5 {
			return "", nil, nil, false
		}
		dlen := int(pkt[4])
		if len(pkt) < 5+dlen+2 {
			return "", nil, nil, false
		}
		host = string(pkt[5 : 5+dlen])
		addrEnd = 5 + dlen
	case 0x04: // IPv6
		if len(pkt) < 4+16+2 {
			return "", nil, nil, false
		}
		host = net.IP(pkt[4:20]).String()
		addrEnd = 20
	default:
		return "", nil, nil, false
	}
	port := int(pkt[addrEnd])<<8 | int(pkt[addrEnd+1])
	return net.JoinHostPort(host, strconv.Itoa(port)), pkt[3 : addrEnd+2], pkt[addrEnd+2:], true
}

// streamReadLoop reads datagrams coming back from target through the tunnel,
// wraps each in the SOCKS5 UDP reply header, and sends it to the client.
func (r *udpRelay) streamReadLoop(target string, flow *udpFlow) {
	buf := make([]byte, 65535)
	for {
		n, err := flow.stream.Read(buf)
		if err != nil {
			r.removeFlow(target)
			return
		}
		r.mu.Lock()
		client := r.clientAddr
		flow.lastSeen = time.Now()
		r.mu.Unlock()
		if client == nil {
			continue
		}
		out := make([]byte, 0, len(flow.replyHeader)+n)
		out = append(out, flow.replyHeader...)
		out = append(out, buf[:n]...)
		r.udpConn.WriteToUDP(out, client)
	}
}

// janitor evicts flows idle in both directions past udpFlowIdleTimeout.
func (r *udpRelay) janitor() {
	ticker := time.NewTicker(udpFlowIdleTimeout / 2)
	defer ticker.Stop()
	for range ticker.C {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return
		}
		cutoff := time.Now().Add(-udpFlowIdleTimeout)
		for target, flow := range r.flows {
			if flow.lastSeen.Before(cutoff) {
				flow.stream.Close()
				delete(r.flows, target)
			}
		}
		r.mu.Unlock()
	}
}

func (r *udpRelay) removeFlow(target string) {
	r.mu.Lock()
	if flow, ok := r.flows[target]; ok {
		flow.stream.Close()
		delete(r.flows, target)
	}
	r.mu.Unlock()
}

func (r *udpRelay) close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	for _, flow := range r.flows {
		flow.stream.Close()
	}
	r.flows = nil
	r.mu.Unlock()
}
