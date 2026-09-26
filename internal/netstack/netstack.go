// Package netstack is the platform-neutral half of every full-tunnel Phantom
// client: it turns raw IP packets arriving on a gVisor link endpoint into
// TCP/UDP flows and relays them through a Phantom session
// (session.Open/OpenUDP), exactly the way a VPN client needs to. None of this
// cares how the packets got to the link endpoint - the Android client
// (mobile/mobile.go) feeds it from a raw TUN file descriptor via gVisor's
// fdbased endpoint, the Windows client (windows/wintun.go) feeds it from a
// Wintun device via gVisor's channel.Endpoint. Both wrap this package's
// Tunnel with their own platform-specific setup/teardown (dialing, TUN
// device lifecycle, routing table changes) rather than duplicating the
// netstack plumbing itself.
package netstack

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"phantom/internal/diag"
	"phantom/internal/tunnel"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const tunNICID = tcpip.NICID(1)
const udpIdleTimeout = 60 * time.Second

// How often currentSession is allowed to actually attempt a refresh while
// the session is dead - without this, a prolonged outage would turn every
// single new connection attempt (and every IsAlive() poll from the UI) into
// its own redial attempt against a server that's still unreachable.
const sessionRefreshCooldown = 3 * time.Second

// RouteDecision is what a RouterFunc says about one new flow.
type RouteDecision int

const (
	// RouteTunnel sends the flow through the Phantom session - the default,
	// and what every flow does when no router is installed.
	RouteTunnel RouteDecision = iota
	// RouteDirect sends the flow straight out the physical interface,
	// bypassing the tunnel.
	RouteDirect
)

// RouterFunc decides where a new flow should go, based only on its
// destination (network is "tcp" or "udp", target is "ip:port").
//
// This is the "smart VPN" axis: it asks *where a connection is going* - so a
// user can say "route these sites through the VPN and leave everything else
// alone". Installing one inverts the client's default posture from "tunnel
// everything" to "tunnel what this says to".
//
// Consulted once per new flow, never per packet, so it can afford a map
// lookup but not real work.
type RouterFunc func(network string, target string) RouteDecision

// DirectDialer opens a connection to target *outside* the tunnel. Only the
// platform layer knows how to do this (binding the socket to the physical
// interface on Windows, VpnService.protect() on Android), so netstack asks
// for one rather than dialing itself - a plain net.Dial here would be
// captured by the client's own default route and loop back into the tunnel.
type DirectDialer func(network, target string) (io.ReadWriteCloser, error)

// Tunnel routes all IP traffic arriving on a gVisor link endpoint through a
// Phantom session. Obtain one via New.
type Tunnel struct {
	session   *tunnel.Session
	sessionMu sync.Mutex
	netstack  *stack.Stack
	startTime time.Time
	bytesUp   int64
	bytesDown int64
	router    RouterFunc
	direct    DirectDialer
	dnsWrap   func(io.ReadWriteCloser) io.ReadWriteCloser
	fakeDNS   string
	realDNS   string

	refreshSession     func() (*tunnel.Session, error)
	lastRefreshAttempt time.Time

	stats     flowStats
	diagExtra func() []any
	dnsRouter DNSRouterFunc
	directDNS string
	stopDiag  chan struct{}
	stopOnce  sync.Once

	// admitMu/stopping close the door on new flows before Stop tears the
	// netstack down: a forwarder handler checks stopping and creates its
	// endpoint under the read lock, so once Stop has set it (under the write
	// lock) no endpoint can appear that Destroy's abort sweep would miss.
	admitMu  sync.RWMutex
	stopping bool

	// Open direct (non-tunnelled) flows, so ResetMisroutedFlows can find the
	// ones a routing change has made wrong.
	directMu    sync.Mutex
	directFlows map[*directFlow]struct{}
}

// directFlow is one open direct connection, registered for as long as it is
// open - see ResetMisroutedFlows.
type directFlow struct {
	io.ReadWriteCloser
	t       *Tunnel
	network string
	target  string
	once    sync.Once
}

func (f *directFlow) Close() error {
	f.once.Do(func() {
		f.t.directMu.Lock()
		delete(f.t.directFlows, f)
		f.t.directMu.Unlock()
	})
	return f.ReadWriteCloser.Close()
}

func (t *Tunnel) trackDirect(network, target string, conn io.ReadWriteCloser) io.ReadWriteCloser {
	f := &directFlow{ReadWriteCloser: conn, t: t, network: network, target: target}
	t.directMu.Lock()
	if t.directFlows == nil {
		t.directFlows = map[*directFlow]struct{}{}
	}
	t.directFlows[f] = struct{}{}
	t.directMu.Unlock()
	return f
}

// ResetMisroutedFlows closes every open direct flow that the router would
// now send through the tunnel, and reports how many it closed.
//
// Routing is decided once per flow, when it opens - so a site added to the
// smart list kept going direct over whatever connections the browser already
// had open to it (a browser keeps them for minutes), and adding a site looked
// like it hadn't worked until the whole tunnel was reconnected. Closing just
// those connections makes the app open fresh ones, which the router now
// sends through the tunnel; nothing else is disturbed. (For UDP - QUIC - there
// is nothing to signal, but the flow's next datagram opens a new one, which is
// routed afresh the same way.)
//
// Call it after the router's view has changed, including after any DNS
// pre-seeding for new entries has finished: a flow is matched by its address.
func (t *Tunnel) ResetMisroutedFlows() int {
	router, _, _ := t.currentRouting()
	t.directMu.Lock()
	flows := make([]*directFlow, 0, len(t.directFlows))
	for f := range t.directFlows {
		flows = append(flows, f)
	}
	t.directMu.Unlock()

	reset := 0
	for _, f := range flows {
		if router == nil || router(f.network, f.target) == RouteTunnel {
			f.Close()
			reset++
		}
	}
	if reset > 0 {
		diag.Event(diag.CatNet, "misroutedReset", "flows", reset, "open", len(flows))
	}
	return reset
}

// admit creates a new flow's endpoint - unless the tunnel is stopping, in
// which case it reports false and the flow is refused. See admitMu.
func (t *Tunnel) admit(create func() bool) bool {
	t.admitMu.RLock()
	defer t.admitMu.RUnlock()
	if t.stopping {
		return false
	}
	return create()
}

// stopAbandonAfter bounds how long Stop waits for the netstack to finish
// tearing down. Past it the stack is left to finish (or not) on its own:
// a few leaked goroutines are a far smaller cost than a stop that never
// returns, which keeps the device's VPN interface up in front of a stack
// that can no longer carry anything.
var stopAbandonAfter = 2 * time.Second

// SetRouting installs the destination-based routing decision and the direct
// dialer it needs - see RouterFunc and DirectDialer. Passing a nil router
// restores the default "tunnel everything" behaviour, which is how the smart
// VPN mode is turned back off without rebuilding the tunnel.
//
// Both are set together on purpose: a router with no dialer could only ever
// answer RouteTunnel, and silently degrading to that would look exactly like
// the feature being broken.
func (t *Tunnel) SetRouting(router RouterFunc, direct DirectDialer) {
	t.sessionMu.Lock()
	t.router = router
	t.direct = direct
	t.sessionMu.Unlock()
}

// SetDNSObserver installs a wrapper applied to tunnelled DNS flows (UDP port
// 53) so a caller can watch the answers going past - which is what makes
// routing by domain name possible at all, see internal/routing's SniffDNS.
// Kept as an opaque wrapper so this package stays unaware of DNS itself.
func (t *Tunnel) SetDNSObserver(wrap func(io.ReadWriteCloser) io.ReadWriteCloser) {
	t.sessionMu.Lock()
	t.dnsWrap = wrap
	t.sessionMu.Unlock()
}

func (t *Tunnel) currentRouting() (RouterFunc, DirectDialer, func(io.ReadWriteCloser) io.ReadWriteCloser) {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	return t.router, t.direct, t.dnsWrap
}

// SetDNSUpstream makes fakeAddr - a bare IP, with no port, the same address
// the platform layer hands to the OS as "the VPN's DNS server" - transparently
// redirect to realUpstream ("ip:port") whenever an app dials it on port 53.
//
// This exists because advertising a well-known public resolver (1.1.1.1,
// 8.8.8.8) as the VPN's own DNS server used to mean Android's "Private DNS:
// Automatic" would silently upgrade the system resolver to DNS-over-TLS
// against that same address, since both are well-known DoT providers - once
// that happens every DNS query leaves as encrypted port-853 traffic that this
// package's DNS sniffing (see internal/routing.SniffDNS) can never see the
// plaintext of, so every domain-based smart-routing entry silently stops
// matching, even though the site is on the list. It reproduced reliably on a
// real phone and never in the emulator, which doesn't opportunistically
// upgrade private DNS the same way. Advertising an address nothing real
// listens on defeats that upgrade outright (the probe just fails and Android
// falls back to plain UDP:53) - this rewrite is what makes queries to that
// fake address actually resolve to something instead of timing out.
func (t *Tunnel) SetDNSUpstream(fakeAddr, realUpstream string) {
	t.sessionMu.Lock()
	t.fakeDNS = fakeAddr
	t.realDNS = realUpstream
	t.sessionMu.Unlock()
}

func (t *Tunnel) currentDNSUpstream() (string, string) {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	return t.fakeDNS, t.realDNS
}

// SetSessionRefresher installs an optional hook that lets the tunnel recover
// from its one connection to the Phantom server dying - a brief Wi-Fi blip, a
// server-side hiccup, a network interface change - by fetching a fresh
// session (typically pool.Get() wrapped in tunnel.NewSessionFromMux) on
// demand, instead of staying bound forever to whichever one happened to be
// alive when New was called.
//
// Without this, once that one connection died, every new TCP/UDP flow (and
// IsAlive(), which the UI polls to decide whether to show a tile as
// connected or in error) kept failing indefinitely - even though the
// underlying transport.ConnPool had *already* quietly redialed a healthy
// replacement connection in the background (see ConnPool.monitorConn). This
// is what made a transient connectivity interruption look identical to the
// whole tunnel having died, requiring a manual disconnect/reconnect to
// recover instead of the pool's own self-healing actually being put to use.
func (t *Tunnel) SetSessionRefresher(fn func() (*tunnel.Session, error)) {
	t.sessionMu.Lock()
	t.refreshSession = fn
	t.sessionMu.Unlock()
}

// currentSession returns the tunnel's session, transparently replacing it
// with a fresh one if the current one has died - see SetSessionRefresher.
// Safe for concurrent use; every new TCP/UDP flow and every IsAlive() call
// goes through this.
func (t *Tunnel) currentSession() *tunnel.Session {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()

	if t.session != nil && t.session.IsAlive() {
		return t.session
	}
	if t.refreshSession == nil || time.Since(t.lastRefreshAttempt) < sessionRefreshCooldown {
		return t.session
	}

	t.lastRefreshAttempt = time.Now()
	fresh, err := t.refreshSession()
	if err != nil {
		diag.Event(diag.CatTunnel, "sessionRefreshFail", "err", err)
		return t.session
	}
	diag.Event(diag.CatTunnel, "sessionRefreshed")
	t.session = fresh
	return t.session
}

// New wires linkEndpoint (already attached to whatever OS-specific packet
// source the caller has - a raw fd, a Wintun device, anything implementing
// gVisor's stack.LinkEndpoint) into a gVisor netstack that forwards every
// TCP/UDP flow it sees through session.Open/OpenUDP. mtu should match the one
// the link endpoint/TUN device was configured with.
func New(session *tunnel.Session, linkEndpoint stack.LinkEndpoint, mtu int) (*Tunnel, error) {
	t := &Tunnel{
		session:   session,
		startTime: time.Now(),
	}

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	t.netstack = s

	if tcpipErr := s.CreateNIC(tunNICID, linkEndpoint); tcpipErr != nil {
		return nil, fmt.Errorf("create nic: %v", tcpipErr)
	}
	s.SetPromiscuousMode(tunNICID, true)
	s.SetSpoofing(tunNICID, true)
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: tunNICID},
		{Destination: header.IPv6EmptySubnet, NIC: tunNICID},
	})

	tcpForwarder := tcp.NewForwarder(s, 0, 512, t.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)

	udpForwarder := udp.NewForwarder(s, t.handleUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)

	t.stopDiag = make(chan struct{})
	go t.runSummaries(t.stopDiag)

	return t, nil
}

func (t *Tunnel) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	target := endpointTarget(id)
	if t.refuseFakeDNS(target) {
		r.Complete(true) // RST: an instant, unambiguous "nothing here"
		return
	}

	var wq waiter.Queue
	var ep tcpip.Endpoint
	admitted := t.admit(func() bool {
		var err tcpip.Error
		ep, err = r.CreateEndpoint(&wq)
		return err == nil
	})
	if !admitted {
		r.Complete(true)
		return
	}
	r.Complete(false)

	local := gonet.NewTCPConn(&wq, ep)

	remote := t.openRemote("tcp", target)
	if remote == nil {
		local.Close()
		return
	}

	t.splice(local, remote)
}

// handleUDP runs *inline* on the packet-processing path - unlike TCP, gVisor's
// UDP forwarder calls it synchronously, on the one goroutine that handles
// every packet the device sends - so it must never wait on the network. It
// only sets up the local end and returns; reaching the remote happens on the
// flow's own goroutine.
//
// It used to open the remote right here. Opening a tunnel stream waits for the
// multiplexer to write its OPEN frame, and with the server gone silent that
// write never completes - so one new UDP flow through the tunnel (QUIC to a
// listed site, a Telegram call) stopped the device's packet processing
// outright, direct traffic included, for as long as the server stayed silent:
// "Умный VPN took the whole internet down". It also deadlocked Stop, whose
// netstack teardown waited on that same stuck handler.
// TestSilentServerDoesNotFreezePacketProcessing reproduces it.
func (t *Tunnel) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	target := endpointTarget(id)
	if t.refuseFakeDNS(target) {
		return false
	}

	var wq waiter.Queue
	var ep tcpip.Endpoint
	if !t.admit(func() bool {
		var err tcpip.Error
		ep, err = r.CreateEndpoint(&wq)
		return err == nil
	}) {
		return false
	}
	local := gonet.NewUDPConn(&wq, ep)

	if isDNSTarget(target) {
		if route, direct, dnsWrap, directDNS := t.currentDNSSplit(); route != nil {
			go t.serveSplitDNS(local, t.dnsUpstream(target), route, direct, dnsWrap, directDNS)
			return true
		}
	}

	go func() {
		remote := t.openRemote("udp", target)
		if remote == nil {
			// Nothing to relay to: the datagrams already queued on local are
			// dropped, the same as a network that can't reach the target.
			local.Close()
			return
		}
		t.spliceUDP(local, remote)
	}()
	return true
}

// openRemote decides how one new flow reaches the internet:
//
//  1. The router (RouterFunc) decides based on the destination - this is what
//     implements smart-VPN mode, where only listed sites are tunnelled.
//  2. Otherwise the flow is tunnelled, which is also the fallback whenever a
//     direct dial was chosen but failed: a flow losing its direct route is
//     better served by the tunnel than by no connectivity at all.
func (t *Tunnel) openRemote(network string, target string) io.ReadWriteCloser {
	dns := network == "udp" && isDNSTarget(target)
	if dns {
		target = t.dnsUpstream(target)
	}

	router, direct, dnsWrap := t.currentRouting()
	if router != nil && direct != nil && router(network, target) == RouteDirect {
		conn, err := direct(network, target)
		if err == nil {
			t.countFlow(network, false)
			if !dns {
				conn = t.trackDirect(network, target, conn)
			}
			return t.meterFlow(network, target, false, dns, conn)
		}
		// Falls through to the tunnel below - which is also why a broken
		// direct path shows up as traffic that should never have touched
		// the tunnel suddenly depending on it.
		t.stats.directFail.Add(1)
		logFailure("directDialFail", network, target, err)
	}

	session := t.currentSession()
	if session == nil {
		t.stats.noSession.Add(1)
		logFailure("noSession", network, target, nil)
		return nil
	}
	if network == "udp" {
		stream, err := session.OpenUDP(target)
		if err != nil {
			t.stats.tunnelOpenFail.Add(1)
			logFailure("tunnelOpenFail", network, target, err)
			return nil
		}
		t.countFlow(network, true)
		var rw io.ReadWriteCloser = stream
		// Only DNS is worth looking at, and only when someone asked to - see
		// SetDNSObserver. Every other UDP flow is passed through untouched.
		if dnsWrap != nil && dns {
			rw = dnsWrap(stream)
		}
		return t.meterFlow(network, target, true, dns, rw)
	}
	stream, err := session.Open(target)
	if err != nil {
		t.stats.tunnelOpenFail.Add(1)
		logFailure("tunnelOpenFail", network, target, err)
		return nil
	}
	t.countFlow(network, true)
	return t.meterFlow(network, target, true, false, stream)
}

// refuseFakeDNS reports whether target is the placeholder DNS address (see
// SetDNSUpstream) on any port but 53 - which nothing can ever answer. Android
// probes it on 853 to see whether "Private DNS" can be upgraded to DNS-over-
// TLS; left alone, that probe was routed out like any other flow and hung for
// the full 10s dial timeout, every time the tunnel came up. Refusing it
// straight away gives the system its answer ("no") at once.
func (t *Tunnel) refuseFakeDNS(target string) bool {
	fake, _ := t.currentDNSUpstream()
	if fake == "" {
		return false
	}
	host, port, err := net.SplitHostPort(target)
	return err == nil && host == fake && port != "53"
}

// dnsUpstream applies SetDNSUpstream's rewrite: a query addressed to the
// placeholder resolver goes to the real one instead.
func (t *Tunnel) dnsUpstream(target string) string {
	if fake, real := t.currentDNSUpstream(); fake != "" {
		if host, _, err := net.SplitHostPort(target); err == nil && host == fake {
			return real
		}
	}
	return target
}

// isDNSTarget reports whether target ("ip:port") is a plain DNS destination.
// Port 53 only: DNS-over-TLS/HTTPS is encrypted and can't be observed here,
// which is a documented limit of routing by domain name rather than something
// this check should pretend to handle.
func isDNSTarget(target string) bool {
	_, port, err := net.SplitHostPort(target)
	return err == nil && port == "53"
}

// splice bridges a netstack-side TCP connection with the corresponding
// remote connection (a tunneled Phantom stream, or - for a split-tunneled
// app - a direct connection dialed by the bypass hook), mirroring the pipe()
// pattern used by the desktop SOCKS5/HTTP proxies (internal/proxy/socks5.go).
func (t *Tunnel) splice(local *gonet.TCPConn, remote io.ReadWriteCloser) {
	done := make(chan struct{})

	go func() {
		n, _ := io.Copy(remote, local)
		atomic.AddInt64(&t.bytesUp, n)
		remote.Close()
		close(done)
	}()

	n, _ := io.Copy(local, remote)
	atomic.AddInt64(&t.bytesDown, n)
	local.Close()
	<-done
}

func (t *Tunnel) spliceUDP(local *gonet.UDPConn, remote io.ReadWriteCloser) {
	defer local.Close()
	defer remote.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)
		buf := make([]byte, 65535)
		for {
			n, err := remote.Read(buf)
			if err != nil {
				return
			}
			if _, err := local.Write(buf[:n]); err != nil {
				return
			}
			atomic.AddInt64(&t.bytesDown, int64(n))
		}
	}()

	buf := make([]byte, 65535)
	for {
		local.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		n, err := local.Read(buf)
		if err != nil {
			break
		}
		if _, err := remote.Write(buf[:n]); err != nil {
			// One oversized datagram is dropped, not fatal to the flow - the same
			// thing a real network does to a packet that doesn't fit.
			if errors.Is(err, tunnel.ErrDatagramTooLarge) {
				continue
			}
			break
		}
		atomic.AddInt64(&t.bytesUp, int64(n))
	}
	<-done
}

// endpointTarget resolves the real internet destination an app is trying to
// reach. In netstack's terms the "local" endpoint address/port is the
// destination address the intercepted packet was sent to (netstack
// transparently answers for every address), while "remote" is the
// originating app's own TUN-side address - see stack.TransportEndpointID.
func endpointTarget(id stack.TransportEndpointID) string {
	return net.JoinHostPort(addressString(id.LocalAddress), fmt.Sprintf("%d", id.LocalPort))
}

func addressString(a tcpip.Address) string {
	if a.Len() == 4 {
		b := a.As4()
		return net.IP(b[:]).String()
	}
	b := a.As16()
	return net.IP(b[:]).String()
}

// Stop tears down the netstack and the underlying Phantom session. It does
// NOT touch the link endpoint or whatever OS-specific packet source feeds
// it - that's the caller's responsibility (closing a TUN device, restoring
// routing tables, etc).
//
// New flows are refused first (see admitMu), then the session is closed, then
// the netstack destroyed. Destroy aborts the connections that exist when it
// starts and then waits for every connection to finish closing; one accepted
// after that sweep - the device keeps opening them the whole time - was
// closed gracefully by a stack whose TCP processing had already shut down,
// never finished closing, and hung Stop forever. That is what took the whole
// device offline on a config switch in the field, and what
// TestStopReturnsWhileTheDeviceKeepsOpeningConnections reproduces.
//
// The session is closed before Destroy so anything still waiting on it is
// released rather than left holding Destroy up. And Destroy gets
// stopAbandonAfter at most: if some other path ever wedges it again, Stop
// still returns and the caller can take the interface down.
func (t *Tunnel) Stop() {
	start := time.Now()
	t.stopOnce.Do(func() {
		if t.stopDiag != nil {
			close(t.stopDiag)
		}
	})
	t.admitMu.Lock()
	t.stopping = true
	t.admitMu.Unlock()
	t.sessionMu.Lock()
	session := t.session
	// No redialing a replacement for a tunnel that is going away: a flow
	// arriving between here and Destroy would otherwise open a fresh
	// connection to the server just to have it torn down.
	t.refreshSession = nil
	t.sessionMu.Unlock()
	if session != nil {
		session.Close()
	}
	if t.netstack != nil {
		// No more packets in: nothing still arriving can reach a handler,
		// and Destroy isn't racing a device that keeps sending.
		t.netstack.DisableNIC(tunNICID)
		destroyed := make(chan struct{})
		go func() {
			t.netstack.Destroy()
			close(destroyed)
		}()
		select {
		case <-destroyed:
		case <-time.After(stopAbandonAfter):
			diag.Event(diag.CatTunnel, "netstackStopAbandoned", "waitingMs", time.Since(start))
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		diag.Event(diag.CatTunnel, "tunnelStopped", "ms", elapsed)
	}
}

// Stats returns a small JSON blob {"uptime_seconds":N,"bytes_up":N,"bytes_down":N}.
func (t *Tunnel) Stats() string {
	stats := map[string]int64{
		"uptime_seconds": int64(time.Since(t.startTime).Seconds()),
		"bytes_up":       atomic.LoadInt64(&t.bytesUp),
		"bytes_down":     atomic.LoadInt64(&t.bytesDown),
	}
	data, _ := json.Marshal(stats)
	return string(data)
}

// IsAlive reports whether the underlying Phantom session is still connected -
// attempting a refresh first (see currentSession/SetSessionRefresher) if it
// isn't, so a UI polling this (e.g. every few seconds) self-heals its
// connected/error indicator automatically once connectivity returns, rather
// than staying stuck on error until a manual reconnect.
func (t *Tunnel) IsAlive() bool {
	session := t.currentSession()
	return session != nil && session.IsAlive()
}
