// Package mobile is the shared entry point for mobile VPN clients (Android now,
// iOS later via the same gomobile-bound source). It owns nothing platform
// specific except the raw TUN file descriptor handed to it by the OS: parsing
// config, establishing the Phantom session, and routing IP packets all
// happen here so both platforms reuse identical logic.
//
// The exported API only uses gomobile-safe types (string, int, error) so it
// can be bound with `gomobile bind` for both Android (.aar) and iOS
// (.xcframework).
package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

	"phantom/internal/config"
	"phantom/internal/protocol"
	"phantom/internal/transport"
	"phantom/internal/tunnel"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const tunNICID = tcpip.NICID(1)
const udpIdleTimeout = 60 * time.Second

// Protector exempts a raw socket fd from the platform's VPN routing, e.g.
// Android's VpnService.protect(). Implemented on the Kotlin/Swift side and
// passed into Start.
type Protector interface {
	Protect(fd int) bool
}

// Tunnel is a running Phantom VPN tunnel. Obtain one via Start.
type Tunnel struct {
	pool      *transport.ConnPool
	session   *tunnel.Session
	netstack  *stack.Stack
	cancel    context.CancelFunc
	startTime time.Time
	bytesUp   int64
	bytesDown int64
}

// Start parses configYAML (the raw contents of a client.yaml file - the
// mobile app just imports/pastes it as text, no separate YAML parser needed
// on the Kotlin/Swift side), connects to the Phantom server, and begins
// routing all IP traffic arriving on tunFD (an already-established OS TUN
// device, e.g. the fd from Android's VpnService.Builder.establish()) through
// the tunnel. mtu should match the MTU the TUN device was configured with
// (1500 if unsure). protector must be able to exempt a raw socket fd from
// the VPN's own routing (VpnService.protect on Android) - without it, this
// process's own connection to the Phantom server gets captured by the
// tunnel it is trying to establish and never completes.
func Start(configYAML string, tunFD int, mtu int, protector Protector) (*Tunnel, error) {
	if mtu <= 0 {
		mtu = 1500
	}

	cfg, err := config.ParseClientConfig([]byte(configYAML))
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	psk, err := cfg.GetPSK()
	if err != nil {
		return nil, fmt.Errorf("psk: %w", err)
	}
	serverPub, err := cfg.GetServerPublicKey()
	if err != nil {
		return nil, fmt.Errorf("server_public_key: %w", err)
	}

	tlsCfg := &transport.TLSClientConfig{
		Domain:      cfg.Domain,
		Fingerprint: cfg.Fingerprint,
		ServerAddr:  cfg.Server,
		PSK:         psk,
		ServerPub:   serverPub,
	}
	if protector != nil {
		tlsCfg.ProtectFD = protector.Protect
	}

	poolSize := cfg.PoolSize
	if poolSize <= 0 {
		poolSize = 4
	}

	ctx, cancel := context.WithCancel(context.Background())

	pool := transport.NewConnPool(poolSize, 12*1024, func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error) {
		return transport.Dial(ctx, tlsCfg)
	})

	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	mux, err := pool.Get(dialCtx)
	dialCancel()
	if err != nil {
		pool.Close()
		cancel()
		return nil, fmt.Errorf("connect: %w", err)
	}

	t := &Tunnel{
		pool:      pool,
		session:   tunnel.NewSessionFromMux(mux),
		cancel:    cancel,
		startTime: time.Now(),
	}

	if err := t.setupNetstack(tunFD, mtu); err != nil {
		t.Stop()
		return nil, err
	}

	log.Printf("[mobile] tunnel started, server=%s", cfg.Server)
	return t, nil
}

func (t *Tunnel) setupNetstack(tunFD int, mtu int) error {
	linkEP, err := fdbased.New(&fdbased.Options{
		FDs: []int{tunFD},
		MTU: uint32(mtu),
	})
	if err != nil {
		return fmt.Errorf("link endpoint: %w", err)
	}

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	t.netstack = s

	if tcpipErr := s.CreateNIC(tunNICID, linkEP); tcpipErr != nil {
		return fmt.Errorf("create nic: %v", tcpipErr)
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

	return nil
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

// endpointTarget resolves the real internet destination an app on the device
// is trying to reach. In netstack's terms the "local" endpoint address/port
// is the destination address the intercepted packet was sent to (netstack
// transparently answers for every address), while "remote" is the
// originating app's internal TUN-side address - see stack.TransportEndpointID.
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

// Stop tears down the tunnel: the netstack, the Phantom session/pool, and
// any in-flight splices.
func (t *Tunnel) Stop() {
	if t.cancel != nil {
		t.cancel()
	}
	if t.netstack != nil {
		t.netstack.Destroy()
	}
	if t.session != nil {
		t.session.Close()
	}
	if t.pool != nil {
		t.pool.Close()
	}
}

// Stats returns a small JSON blob {"uptime_seconds":N,"bytes_up":N,"bytes_down":N}
// for the UI to poll. A plain string keeps this gomobile-safe.
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
