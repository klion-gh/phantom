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
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

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

// BypassFunc decides whether a new connection should bypass the Phantom
// tunnel entirely, given the *originating* app's local port on this machine
// (network is "tcp" or "udp", target is the real internet destination the
// app is trying to reach). Returning nil tunnels the connection normally
// through session.Open/OpenUDP; returning a non-nil connection splices it
// directly instead. Used by the Windows client for per-app split tunneling -
// nil on Android, which has no equivalent per-app concept at this layer.
type BypassFunc func(network string, localPort uint16, target string) io.ReadWriteCloser

// Tunnel routes all IP traffic arriving on a gVisor link endpoint through a
// Phantom session. Obtain one via New.
type Tunnel struct {
	session   *tunnel.Session
	sessionMu sync.Mutex
	netstack  *stack.Stack
	startTime time.Time
	bytesUp   int64
	bytesDown int64
	bypass    BypassFunc

	refreshSession     func() (*tunnel.Session, error)
	lastRefreshAttempt time.Time
}

// SetBypass installs an optional per-connection bypass hook - see BypassFunc.
// Safe to call any time after New; takes effect for connections forwarded
// afterwards. A nil Tunnel receiver check isn't needed since callers always
// have a valid *Tunnel from a successful New.
func (t *Tunnel) SetBypass(fn BypassFunc) {
	t.bypass = fn
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
	t.refreshSession = fn
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
		log.Printf("[netstack] session refresh failed: %v", err)
		return t.session
	}
	log.Printf("[netstack] recovered with a fresh session after the previous one died")
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

	return t, nil
}

func (t *Tunnel) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	target := endpointTarget(id)

	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		r.Complete(true)
		return
	}
	r.Complete(false)

	local := gonet.NewTCPConn(&wq, ep)

	remote := t.openRemote("tcp", id.RemotePort, target)
	if remote == nil {
		local.Close()
		return
	}

	t.splice(local, remote)
}

func (t *Tunnel) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	target := endpointTarget(id)

	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		return false
	}
	local := gonet.NewUDPConn(&wq, ep)

	remote := t.openRemote("udp", id.RemotePort, target)
	if remote == nil {
		local.Close()
		return false
	}

	go t.spliceUDP(local, remote)
	return true
}

// openRemote gives the bypass hook (if any) first refusal on a new
// connection - see BypassFunc - and falls back to tunneling through the
// Phantom session otherwise (including when a bypass was attempted but the
// direct dial itself failed, so an excluded app still gets connectivity via
// the tunnel rather than none at all).
func (t *Tunnel) openRemote(network string, localPort uint16, target string) io.ReadWriteCloser {
	if t.bypass != nil {
		if conn := t.bypass(network, localPort, target); conn != nil {
			return conn
		}
	}
	session := t.currentSession()
	if session == nil {
		return nil
	}
	if network == "udp" {
		stream, err := session.OpenUDP(target)
		if err != nil {
			return nil
		}
		return stream
	}
	stream, err := session.Open(target)
	if err != nil {
		return nil
	}
	return stream
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
func (t *Tunnel) Stop() {
	if t.netstack != nil {
		t.netstack.Destroy()
	}
	t.sessionMu.Lock()
	session := t.session
	t.sessionMu.Unlock()
	if session != nil {
		session.Close()
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
