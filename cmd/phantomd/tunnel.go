//go:build linux

package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	gtun "gvisor.dev/gvisor/pkg/tcpip/link/tun"

	"phantom/internal/daemonapi"
	"phantom/mobile"
)

// The tunnel itself is the Android one: mobile.Start takes a TUN fd and a
// way to exempt its own sockets from the VPN, and everything above that -
// the netstack, the server pool, smart routing, split DNS - is shared. What
// Linux adds is how traffic gets *into* the device (netconfig.go) and how
// phantomd's own sockets stay out of it (markSocket).

const (
	tunName = "phantom0"
	tunMTU  = 1500
	tunAddr = "10.10.0.2/24"
)

// networkSettle is how long route changes are left to settle before the
// tunnel acts on them - a Wi-Fi <-> Ethernet handover is a burst of events.
const networkSettle = 1500 * time.Millisecond

type linuxTunnel struct {
	t  *mobile.Tunnel
	fd int

	watchDone chan struct{}
	stopOnce  sync.Once
}

// protector is mobile.Protector for Linux: instead of Android's
// VpnService.protect(), the socket gets phantomd's routing mark, which the
// policy rules send through the main table and around the tunnel.
type protector struct{}

func (protector) Protect(fd int) bool { return markSocket(fd) }

func markSocket(fd int) bool {
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_MARK, fwMark); err != nil {
		log.Printf("mark socket %d: %v", fd, err)
		return false
	}
	return true
}

// startLinuxTunnel brings the tunnel up. Nothing about the machine's routing
// changes until the server has actually answered: the device exists but
// carries nothing while mobile.Start dials, so a config that can't connect
// leaves the network exactly as it was.
func startLinuxTunnel(yaml string, routing daemonapi.RoutingArgs) (*linuxTunnel, error) {
	cleanupNetworking()

	fd, err := gtun.Open(tunName)
	if err != nil {
		return nil, fmt.Errorf("create %s (is /dev/net/tun available?): %w", tunName, err)
	}
	link, err := configureLink()
	if err != nil {
		unix.Close(fd) // a non-persistent TUN device disappears with its fd
		return nil, err
	}

	t, err := mobile.Start(yaml, fd, tunMTU, protector{})
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	lt := &linuxTunnel{t: t, fd: fd, watchDone: make(chan struct{})}
	lt.SetRouting(routing, 0)
	t.SetDirectDNS(physicalDNSServers())

	if err := installNetworking(link.Attrs().Index); err != nil {
		lt.Stop()
		return nil, err
	}
	lt.watchNetwork()
	return lt, nil
}

func configureLink() (netlink.Link, error) {
	link, err := netlink.LinkByName(tunName)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", tunName, err)
	}
	if err := netlink.LinkSetMTU(link, tunMTU); err != nil {
		return nil, fmt.Errorf("set mtu: %w", err)
	}
	addr, err := netlink.ParseAddr(tunAddr)
	if err != nil {
		return nil, err
	}
	if err := netlink.AddrReplace(link, addr); err != nil {
		return nil, fmt.Errorf("assign %s: %w", tunAddr, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return nil, fmt.Errorf("bring %s up: %w", tunName, err)
	}
	return link, nil
}

func (lt *linuxTunnel) Stop() {
	lt.stopOnce.Do(func() {
		close(lt.watchDone)
		// Rules first: once the device is gone, anything still matching them
		// would find an empty table and fall through to the main one anyway,
		// but there is no reason to leave that window open.
		cleanupNetworking()
		lt.t.Stop()
		unix.Close(lt.fd)
	})
}

func (lt *linuxTunnel) IsAlive() bool { return lt.t.IsAlive() }
func (lt *linuxTunnel) Stats() string { return lt.t.Stats() }
func (lt *linuxTunnel) ResetMisroutedFlows() {
	lt.t.ResetMisroutedFlows()
}

func (lt *linuxTunnel) SetRouting(r daemonapi.RoutingArgs, wait time.Duration) {
	lt.t.SetSmartRoutingWait(r.Smart, strings.Join(r.Sites, "\n"), int(wait.Milliseconds()))
}

// watchNetwork moves the tunnel onto a new physical network without
// rebuilding it, the way the Android app does: the server pool is recycled
// and split DNS pointed at the new network's resolver. The routing rules
// themselves need nothing - the mark sends phantomd's own sockets through
// whatever the main table says now, so there is no captured gateway to go
// stale (Windows has to re-pin one; see desktop/networkwatch_windows.go).
func (lt *linuxTunnel) watchNetwork() {
	updates := make(chan netlink.RouteUpdate, 64)
	if err := netlink.RouteSubscribe(updates, lt.watchDone); err != nil {
		log.Printf("network watch unavailable: %v", err)
		return
	}
	go func() {
		var settle <-chan time.Time
		for {
			select {
			case <-lt.watchDone:
				return
			case u, ok := <-updates:
				if !ok {
					return
				}
				// Only the main table's default routes are the physical
				// network; changes in phantomd's own table are its own doing.
				if u.Table != unix.RT_TABLE_MAIN || !isDefaultRoute(u.Route) {
					continue
				}
				settle = time.After(networkSettle)
			case <-settle:
				settle = nil
				log.Printf("default route changed - moving the tunnel to the new network")
				lt.t.NetworkChanged(physicalDNSServers())
			}
		}
	}()
}

func isDefaultRoute(r netlink.Route) bool {
	if r.Dst == nil {
		return true
	}
	ones, _ := r.Dst.Mask.Size()
	return ones == 0
}

// physicalDNSServers is the resolver the machine would use without the VPN,
// for names smart mode resolves directly. With a local stub (systemd-resolved
// at 127.0.0.53, dnsmasq at 127.0.0.1) the real upstreams are elsewhere:
// resolved keeps them in its own resolv.conf. Loopback entries are skipped by
// mobile.Tunnel.SetDirectDNS either way; with nothing usable it falls back to
// a public resolver.
func physicalDNSServers() string {
	var servers []string
	for _, path := range []string{"/etc/resolv.conf", "/run/systemd/resolve/resolv.conf"} {
		for _, s := range readNameservers(path) {
			if ip := net.ParseIP(s); ip != nil && !ip.IsLoopback() {
				servers = append(servers, s)
			}
		}
		if len(servers) > 0 {
			break
		}
	}
	return strings.Join(servers, ",")
}

func readNameservers(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			out = append(out, fields[1])
		}
	}
	return out
}
