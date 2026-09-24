package netstack

import (
	"encoding/binary"
	"io"
	"strings"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

// Split DNS: in smart mode, only queries for listed names ride the tunnel;
// everything else is resolved directly, the way it would be with no VPN.
//
// Before this, every DNS query on the device went through the tunnel in smart
// mode, listed site or not - so whenever the tunnel stopped answering (a
// network change, a server hiccup, or a censor freezing the connection, which
// drops packets rather than resetting it and so raises no error) no name
// resolved at all, and the whole internet looked down even though every
// non-listed site was supposed to be going direct anyway.
//
// A direct query that gets no answer within directFallbackAfter is re-sent
// through the tunnel: some networks block outside resolvers, and without the
// fallback this change would have broken DNS on exactly those networks, where
// the old tunnel-everything behaviour worked. Listed names never fall back the
// other way - resolving them through the local network is how they come back
// poisoned, and it would tell the network which listed sites are being used.

// DNSRouterFunc decides, per queried name, whether a DNS query rides the
// tunnel.
type DNSRouterFunc func(qname string) RouteDecision

const directFallbackAfter = 1500 * time.Millisecond

// SetDNSRouter installs split DNS (see above). It only takes effect alongside
// SetRouting's direct dialer - without a way to send a query around the tunnel
// there is nothing to split.
func (t *Tunnel) SetDNSRouter(fn DNSRouterFunc) {
	t.sessionMu.Lock()
	t.dnsRouter = fn
	t.sessionMu.Unlock()
}

// SetDirectDNSUpstream sets where directly-resolved queries go ("ip:port"),
// instead of wherever the query was addressed. The platform passes the
// physical network's own resolver (the ISP's or the router's), so a
// non-listed name resolves exactly as it would with the VPN off.
func (t *Tunnel) SetDirectDNSUpstream(addr string) {
	t.sessionMu.Lock()
	t.directDNS = addr
	t.sessionMu.Unlock()
}

func (t *Tunnel) currentDNSSplit() (DNSRouterFunc, DirectDialer, func(io.ReadWriteCloser) io.ReadWriteCloser, string) {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	if t.router == nil || t.direct == nil {
		return nil, nil, nil, ""
	}
	return t.dnsRouter, t.direct, t.dnsWrap, t.directDNS
}

// serveSplitDNS handles one UDP DNS flow from the device, query by query.
func (t *Tunnel) serveSplitDNS(local *gonet.UDPConn, target string, route DNSRouterFunc, direct DirectDialer, dnsWrap func(io.ReadWriteCloser) io.ReadWriteCloser, directUpstream string) {
	defer local.Close()

	directTarget := target
	if directUpstream != "" {
		directTarget = directUpstream
	}
	s := &dnsSplitter{
		route:         route,
		fallbackAfter: directFallbackAfter,
		stats:         &t.stats,
		reply: func(b []byte) error {
			_, err := local.Write(b)
			return err
		},
		openDirect: func() (io.ReadWriteCloser, error) {
			conn, err := direct("udp", directTarget)
			if err != nil {
				t.stats.directFail.Add(1)
				logFailure("directDialFail", "udp", directTarget, err, "dns", true)
				return nil, err
			}
			t.countFlow("udp", false)
			return t.meterFlow("udp", directTarget, false, true, conn), nil
		},
		openTunnel: func() (io.ReadWriteCloser, error) {
			session := t.currentSession()
			if session == nil {
				t.stats.noSession.Add(1)
				logFailure("noSession", "udp", target, nil, "dns", true)
				return nil, errNoSession
			}
			stream, err := session.OpenUDP(target)
			if err != nil {
				t.stats.tunnelOpenFail.Add(1)
				logFailure("tunnelOpenFail", "udp", target, err, "dns", true)
				return nil, err
			}
			t.countFlow("udp", true)
			var rw io.ReadWriteCloser = stream
			if dnsWrap != nil {
				rw = dnsWrap(stream)
			}
			return t.meterFlow("udp", target, true, true, rw), nil
		},
	}
	defer s.close()

	buf := make([]byte, 65535)
	for {
		local.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		n, err := local.Read(buf)
		if err != nil {
			return
		}
		q := make([]byte, n)
		copy(q, buf[:n])
		s.handleQuery(q)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

const errNoSession = errString("no live session")

// dnsSplitter routes one flow's queries between a direct and a tunnelled
// upstream, each opened lazily on first use, and writes every answer from
// either back to the device. Kept apart from gVisor's types so the routing
// and fallback logic can be tested with plain pipes.
type dnsSplitter struct {
	route                  DNSRouterFunc
	openDirect, openTunnel func() (io.ReadWriteCloser, error)
	reply                  func([]byte) error
	fallbackAfter          time.Duration
	stats                  *flowStats

	mu      sync.Mutex
	direct  io.ReadWriteCloser
	tunnel  io.ReadWriteCloser
	pending map[uint16]*time.Timer // direct queries awaiting an answer, by DNS id
	closed  bool
}

func (s *dnsSplitter) handleQuery(q []byte) {
	decision := RouteTunnel
	if name := dnsQueryName(q); name != "" && s.route != nil {
		decision = s.route(name)
	}
	if decision == RouteDirect {
		if rw := s.upstream(true); rw != nil {
			s.stats.dnsViaDirect.Add(1)
			s.armFallback(q)
			if _, err := rw.Write(q); err == nil {
				return
			}
		}
		// No direct path at all: the tunnel is still better than no answer.
	}
	if rw := s.upstream(false); rw != nil {
		s.stats.dnsViaTunnel.Add(1)
		rw.Write(q)
	}
}

// armFallback re-sends a direct query through the tunnel if no answer with
// its id has come back within fallbackAfter.
func (s *dnsSplitter) armFallback(q []byte) {
	if s.fallbackAfter <= 0 || len(q) < 2 {
		return
	}
	id := binary.BigEndian.Uint16(q)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.pending == nil {
		s.pending = map[uint16]*time.Timer{}
	}
	if old := s.pending[id]; old != nil {
		old.Stop()
	}
	s.pending[id] = time.AfterFunc(s.fallbackAfter, func() {
		s.mu.Lock()
		_, still := s.pending[id]
		delete(s.pending, id)
		closed := s.closed
		s.mu.Unlock()
		if !still || closed {
			return
		}
		if rw := s.upstream(false); rw != nil {
			s.stats.dnsDirectFallback.Add(1)
			logFailure("dnsDirectFallback", "udp", "", nil, "afterMs", s.fallbackAfter)
			rw.Write(q)
		}
	})
}

// upstream returns (opening on first use) the direct or tunnelled upstream.
func (s *dnsSplitter) upstream(direct bool) io.ReadWriteCloser {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	existing := s.tunnel
	if direct {
		existing = s.direct
	}
	s.mu.Unlock()
	if existing != nil {
		return existing
	}

	open := s.openTunnel
	if direct {
		open = s.openDirect
	}
	rw, err := open()
	if err != nil || rw == nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		rw.Close()
		return nil
	}
	// Lost a race with another caller opening the same side: keep theirs.
	if direct && s.direct != nil {
		s.mu.Unlock()
		rw.Close()
		return s.direct
	}
	if !direct && s.tunnel != nil {
		s.mu.Unlock()
		rw.Close()
		return s.tunnel
	}
	if direct {
		s.direct = rw
	} else {
		s.tunnel = rw
	}
	s.mu.Unlock()
	go s.readAnswers(rw, direct)
	return rw
}

func (s *dnsSplitter) readAnswers(rw io.ReadWriteCloser, direct bool) {
	buf := make([]byte, 65535)
	for {
		n, err := rw.Read(buf)
		if err != nil {
			return
		}
		if direct && n >= 2 {
			id := binary.BigEndian.Uint16(buf)
			s.mu.Lock()
			if timer := s.pending[id]; timer != nil {
				timer.Stop()
				delete(s.pending, id)
			}
			s.mu.Unlock()
		}
		if err := s.reply(buf[:n]); err != nil {
			return
		}
	}
}

func (s *dnsSplitter) close() {
	s.mu.Lock()
	s.closed = true
	for id, timer := range s.pending {
		timer.Stop()
		delete(s.pending, id)
	}
	direct, tunnel := s.direct, s.tunnel
	s.mu.Unlock()
	if direct != nil {
		direct.Close()
	}
	if tunnel != nil {
		tunnel.Close()
	}
}

// dnsQueryName returns the first question's name from a DNS query message,
// lowercased and without the trailing dot - or "" if it isn't a parseable
// query, which the caller treats as "tunnel it", the safe default.
func dnsQueryName(msg []byte) string {
	const headerLen = 12
	if len(msg) < headerLen {
		return ""
	}
	flags := binary.BigEndian.Uint16(msg[2:4])
	if flags&0x8000 != 0 { // a response, not a query
		return ""
	}
	if binary.BigEndian.Uint16(msg[4:6]) == 0 { // no question
		return ""
	}
	var b strings.Builder
	i := headerLen
	for {
		if i >= len(msg) {
			return ""
		}
		l := int(msg[i])
		i++
		if l == 0 {
			break
		}
		// A question name is never compressed; a pointer here means this
		// isn't a well-formed query.
		if l&0xC0 != 0 || i+l > len(msg) || b.Len()+l+1 > 255 {
			return ""
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.Write(msg[i : i+l])
		i += l
	}
	return strings.ToLower(b.String())
}
