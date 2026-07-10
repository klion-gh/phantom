package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"phantom/internal/config"
	"phantom/internal/netstack"
	"phantom/internal/protocol"
	"phantom/internal/transport"
	"phantom/internal/tunnel"

	"golang.zx2c4.com/wireguard/tun"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// runNetCmd runs a netsh/route helper command with its console window
// hidden (exec.Command otherwise flashes a visible console since this is a
// GUI app with no console of its own) and logs the command and its output
// either way, so phantom.log shows exactly what each routing step did
// without needing to race a manual check against the session's lifetime.
func runNetCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("net cmd FAILED: %s %s -> %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	} else {
		log.Printf("net cmd ok: %s %s -> %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), err
}

const (
	tunMTU        = 1500
	tunIfaceName  = "Phantom"
	tunLocalIP    = "10.10.0.2"
	tunLocalCIDR  = "10.10.0.2/24"
	channelQueueN = 512
)

// WinTunnel is a running full-tunnel Windows VPN connection: a Wintun device
// bridged into internal/netstack the same way mobile/mobile.go bridges a raw
// Android TUN fd, plus the Windows-specific routing table bookkeeping needed
// to (a) route all system traffic through the tunnel and (b) keep this
// process's own connection to the Phantom server from looping back into the
// tunnel it is establishing - see StartWindows for why the ordering matters.
type WinTunnel struct {
	pool      *transport.ConnPool
	cancel    context.CancelFunc
	inner     *netstack.Tunnel
	tunDevice tun.Device
	linkEP    *channel.Endpoint

	bypassServerIP string // host route added via the original gateway; "" if none was added
}

// StartWindows parses configYAML, establishes the Phantom session, and routes
// all system IP traffic through it via a Wintun adapter.
//
// Order of operations matters: once a 0.0.0.0/0 route exists through the TUN
// interface, this process's own connection to the Phantom server would loop
// back into the tunnel it's building (the same class of bug fixed on Android
// via VpnService.protect() - Windows has no per-socket exemption API, so the
// fix here is routing-table specificity instead):
//  1. Resolve the server's IP and find the current default gateway.
//  2. Add a /32 host route for the server IP via the *original* gateway - more
//     specific than the /0 route added later, so Windows' longest-prefix-match
//     always prefers it regardless of route metric.
//  3. Only now dial and establish the Phantom session.
//  4. Create the TUN device, assign it an address, set DNS.
//  5. Add the 0.0.0.0/0 route through the TUN interface.
//  6. Start the netstack forwarders.
func StartWindows(configYAML string) (*WinTunnel, error) {
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

	host, _, err := net.SplitHostPort(cfg.Server)
	if err != nil {
		return nil, fmt.Errorf("invalid server address %q: %w", cfg.Server, err)
	}
	// LookupIP with "ip4" (rather than net.LookupHost, which resolves both
	// families) skips the AAAA query entirely - on this network the AAAA
	// lookup for the server's domain was stalling for several seconds before
	// falling back to A, which was most of the total connect time. The
	// explicit timeout keeps a slow/unresponsive resolver from stalling
	// connect indefinitely either way.
	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	serverIPs, err := net.DefaultResolver.LookupIP(resolveCtx, "ip4", host)
	resolveCancel()
	if err != nil || len(serverIPs) == 0 {
		return nil, fmt.Errorf("resolve server address %q: %w", host, err)
	}
	serverIP := serverIPs[0].String()

	gateway, err := findDefaultGateway()
	if err != nil {
		return nil, fmt.Errorf("find default gateway: %w", err)
	}

	// Must be captured now, before any routing changes below - once the
	// tunnel's 0.0.0.0/0 route exists, this would just return the tunnel's
	// own interface instead of the real one split-tunneled apps need to dial
	// out through. A failure here isn't fatal to the tunnel itself, just to
	// split tunneling (excluded apps' connections will fall back to being
	// tunneled - see openRemote in internal/netstack).
	physicalIfIndex, physicalIfErr := bestInterfaceIndex(serverIP)
	if physicalIfErr != nil {
		log.Printf("split tunneling unavailable: %v", physicalIfErr)
	}

	if err := addHostRoute(serverIP, gateway); err != nil {
		return nil, fmt.Errorf("add bypass route for %s via %s: %w", serverIP, gateway, err)
	}

	w := &WinTunnel{bypassServerIP: serverIP}

	tlsCfg := &transport.TLSClientConfig{
		Domain:      cfg.Domain,
		Fingerprint: cfg.Fingerprint,
		ServerAddr:  cfg.Server,
		PSK:         psk,
		ServerPub:   serverPub,
	}

	poolSize := cfg.PoolSize
	if poolSize <= 0 {
		poolSize = 4
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel

	pool := transport.NewConnPool(poolSize, 12*1024, func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error) {
		return transport.Dial(ctx, tlsCfg)
	})
	w.pool = pool

	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	mux, err := pool.Get(dialCtx)
	dialCancel()
	if err != nil {
		w.Stop()
		return nil, fmt.Errorf("connect: %w", err)
	}

	tunDev, err := tun.CreateTUN(tunIfaceName, tunMTU)
	if err != nil {
		w.Stop()
		return nil, fmt.Errorf("create TUN device (are you running as Administrator?): %w", err)
	}
	w.tunDevice = tunDev

	ifaceName, err := tunDev.Name()
	if err != nil {
		ifaceName = tunIfaceName
	}

	if err := configureInterface(ifaceName); err != nil {
		w.Stop()
		return nil, fmt.Errorf("configure TUN interface: %w", err)
	}

	if err := addDefaultRoute(ifaceName); err != nil {
		w.Stop()
		return nil, fmt.Errorf("add default route: %w", err)
	}

	routeTable, _ := runNetCmd("route", "print", "-4", "0.0.0.0")
	log.Printf("route table right after tunnel setup:\n%s", routeTable)
	// netsh's "add route" can print a syntax/usage error to stdout while
	// still exiting 0 (seen with the wrong parameter name during development -
	// see addDefaultRoute), so a clean err == nil above doesn't guarantee the
	// route actually landed. Verify it directly: our route has no explicit
	// nexthop, so Windows lists it as "0.0.0.0  0.0.0.0  On-link  ...". That
	// literal "On-link" keyword is printed unlocalized regardless of Windows
	// display language (unlike the rest of route print's output), and no
	// other default route on this machine can have it - a normal gateway
	// route always shows a real IP address there instead.
	defaultRouteRe := regexp.MustCompile(`(?m)^\s*0\.0\.0\.0\s+0\.0\.0\.0\s+On-link\s`)
	if !defaultRouteRe.MatchString(routeTable) {
		w.Stop()
		return nil, fmt.Errorf("default route via %s was not found in the route table after setup - traffic would not be tunneled", ifaceName)
	}

	linkEP := channel.New(channelQueueN, tunMTU, "")
	w.linkEP = linkEP
	go pumpTunToChannel(tunDev, linkEP)
	go pumpChannelToTun(linkEP, tunDev)

	session := tunnel.NewSessionFromMux(mux)
	inner, err := netstack.New(session, linkEP, tunMTU)
	if err != nil {
		session.Close()
		w.Stop()
		return nil, err
	}
	w.inner = inner
	if physicalIfErr == nil {
		inner.SetBypass(newSplitTunnelBypass(physicalIfIndex))
	}

	return w, nil
}

// Stop tears down everything StartWindows set up, in reverse order. Safe to
// call on a partially-initialized WinTunnel (every field is nil-checked)
// since StartWindows calls it on its own error paths.
func (w *WinTunnel) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	if w.inner != nil {
		w.inner.Stop()
	}
	if w.linkEP != nil {
		w.linkEP.Close()
	}
	if w.tunDevice != nil {
		w.tunDevice.Close()
		// Windows drops routes bound to this interface's LUID automatically
		// once the adapter disappears, so the 0.0.0.0/0 route needs no
		// explicit cleanup here.
	}
	if w.pool != nil {
		w.pool.Close()
	}
	if w.bypassServerIP != "" {
		// Not tied to the tunnel interface, so it does NOT get cleaned up
		// automatically - must remove it explicitly.
		removeHostRoute(w.bypassServerIP)
	}
}

func (w *WinTunnel) Stats() string {
	if w.inner == nil {
		return "{}"
	}
	return w.inner.Stats()
}

func (w *WinTunnel) IsAlive() bool {
	return w.inner != nil && w.inner.IsAlive()
}

// pumpTunToChannel reads raw IP packets coming from the OS (i.e. from every
// app on the machine, now that the default route points here) and injects
// them into the netstack.
func pumpTunToChannel(dev tun.Device, ep *channel.Endpoint) {
	batchSize := dev.BatchSize()
	bufs := make([][]byte, batchSize)
	sizes := make([]int, batchSize)
	for i := range bufs {
		bufs[i] = make([]byte, tunMTU+32)
	}

	for {
		n, err := dev.Read(bufs, sizes, 0)
		if err != nil {
			return
		}
		for i := 0; i < n; i++ {
			packet := make([]byte, sizes[i])
			copy(packet, bufs[i][:sizes[i]])
			proto := protocolNumberFor(packet)
			if proto == 0 {
				continue
			}
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
				Payload: buffer.MakeWithData(packet),
			})
			ep.InjectInbound(proto, pkt)
			pkt.DecRef()
		}
	}
}

// pumpChannelToTun does the reverse: packets the netstack wants delivered
// back to local apps (e.g. a TCP ACK, a DNS response) get written out to the
// TUN device so Windows' own IP stack hands them to the right process.
func pumpChannelToTun(ep *channel.Endpoint, dev tun.Device) {
	bufs := make([][]byte, 1)
	for {
		pkt := ep.ReadContext(context.Background())
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		data := view.AsSlice()
		bufs[0] = data
		dev.Write(bufs, 0)
		pkt.DecRef()
	}
}

// protocolNumberFor peeks at the IP version nibble to tell gVisor which
// network protocol is parsing the packet - the TUN device hands us raw IP
// packets with no framing of its own.
func protocolNumberFor(packet []byte) tcpip.NetworkProtocolNumber {
	if len(packet) == 0 {
		return 0
	}
	switch packet[0] >> 4 {
	case 4:
		return header.IPv4ProtocolNumber
	case 6:
		return header.IPv6ProtocolNumber
	default:
		return 0
	}
}

// --- Windows routing/interface configuration (netsh/route - see PROTOCOL.md
// for why this is v1's approach rather than the raw IP Helper API) ---

func findDefaultGateway() (string, error) {
	out, err := runNetCmd("route", "print", "-4", "0.0.0.0")
	if err != nil {
		return "", fmt.Errorf("route print: %w", err)
	}
	re := regexp.MustCompile(`(?m)^\s*0\.0\.0\.0\s+0\.0\.0\.0\s+(\d+\.\d+\.\d+\.\d+)\s+(\d+\.\d+\.\d+\.\d+)\s+(\d+)\s*$`)
	matches := re.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		return "", fmt.Errorf("no default route found in:\n%s", out)
	}

	best := matches[0]
	bestMetric, _ := strconv.Atoi(best[3])
	for _, m := range matches[1:] {
		metric, _ := strconv.Atoi(m[3])
		if metric < bestMetric {
			best, bestMetric = m, metric
		}
	}
	return best[1], nil
}

func addHostRoute(ip, gateway string) error {
	_, err := runNetCmd("route", "add", ip, "mask", "255.255.255.255", gateway)
	return err
}

func removeHostRoute(ip string) error {
	_, err := runNetCmd("route", "delete", ip)
	return err
}

func configureInterface(ifaceName string) error {
	steps := [][]string{
		{"netsh", "interface", "ip", "set", "address", "name=" + ifaceName, "static", tunLocalIP, "255.255.255.0"},
		// validate=no skips netsh's default behavior of probing the DNS
		// server for reachability before committing - on a freshly-created
		// adapter (routing not fully up yet) that probe can stall for
		// several seconds per server, which was the single biggest chunk of
		// connect time.
		{"netsh", "interface", "ip", "set", "dns", "name=" + ifaceName, "static", "1.1.1.1", "validate=no"},
		{"netsh", "interface", "ip", "add", "dns", "name=" + ifaceName, "8.8.8.8", "index=2", "validate=no"},
		// Windows' actual route preference is (route metric + interface metric),
		// not the route metric alone. A fresh Wintun adapter's automatic interface
		// metric can outrank a fast physical NIC, so the 0.0.0.0/0 route added
		// below (metric=1) can silently lose the routing race unless the
		// interface's own metric is also pinned low.
		{"netsh", "interface", "ipv4", "set", "interface", ifaceName, "metric=1"},
	}
	for _, args := range steps {
		if _, err := runNetCmd(args[0], args[1:]...); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

func addDefaultRoute(ifaceName string) error {
	// Unlike the legacy "netsh interface ip" family used in configureInterface
	// (which takes name=), "netsh interface ipv4 add route" takes interface=.
	// Passing name= here doesn't just fail - netsh prints a usage/syntax error
	// to stdout but still exits 0, so the route silently never gets added
	// while the Go error check (err != nil) sees nothing wrong.
	_, err := runNetCmd("netsh", "interface", "ipv4", "add", "route", "0.0.0.0/0", "interface="+ifaceName, "metric=1")
	return err
}
