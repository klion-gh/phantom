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
	"net"
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

// Tunnel routes all IP traffic arriving on a gVisor link endpoint through a
// Phantom session. Obtain one via New.
type Tunnel struct {
	session   *tunnel.Session
	netstack  *stack.Stack
	startTime time.Time
	bytesUp   int64
	bytesDown int64
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

	stream, streamErr := t.session.Open(target)
	if streamErr != nil {
		local.Close()
		return
	}

	t.splice(local, stream)
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

	stream, streamErr := t.session.OpenUDP(target)
	if streamErr != nil {
		local.Close()
		return false
	}

	go t.spliceUDP(local, stream)
	return true
}

// splice bridges a netstack-side TCP connection with the corresponding
// Phantom stream, mirroring the pipe() pattern used by the desktop
// SOCKS5/HTTP proxies (internal/proxy/socks5.go).
func (t *Tunnel) splice(local *gonet.TCPConn, stream *tunnel.Stream) {
	done := make(chan struct{})

	go func() {
		n, _ := io.Copy(stream, local)
		atomic.AddInt64(&t.bytesUp, n)
		stream.Close()
		close(done)
	}()

	n, _ := io.Copy(local, stream)
	atomic.AddInt64(&t.bytesDown, n)
	local.Close()
	<-done
}

func (t *Tunnel) spliceUDP(local *gonet.UDPConn, stream *tunnel.Stream) {
	defer local.Close()
	defer stream.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)
		buf := make([]byte, 65535)
		for {
			n, err := stream.Read(buf)
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
		if _, err := stream.Write(buf[:n]); err != nil {
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
	if t.session != nil {
		t.session.Close()
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

// IsAlive reports whether the underlying Phantom session is still connected.
func (t *Tunnel) IsAlive() bool {
	return t.session != nil && t.session.IsAlive()
}
