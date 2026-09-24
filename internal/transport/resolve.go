package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"syscall"
	"time"

	"phantom/internal/diag"
)

// Resolving the server's own hostname without depending on the system
// resolver, and surviving when no resolver answers at all.
//
// On Android the app isn't excluded from its own VPN, so while a tunnel is up
// the system resolver points *into* it (10.10.0.1) - and while that tunnel is
// being replaced, at nothing that answers. A connect or pool redial right
// then failed with "lookup <server>: no such host": the tunnel couldn't come
// back because it needed itself to find its own server. Hence a resolver
// whose sockets are protected out of the VPN, and a remembered last-good
// address per host for when even that fails.

// IPLookuper is the one resolver method a dial needs, so net.DefaultResolver
// and the protected resolver below can stand in for each other.
type IPLookuper interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

// Public resolvers a protected lookup asks, over sockets taken out of the VPN.
// Two, so one being filtered on a given network doesn't take lookups down.
var directDNSServers = []string{"1.1.1.1:53", "8.8.8.8:53"}

// NewProtectedResolver returns a resolver whose DNS sockets go through protect
// (VpnService.protect on Android, an interface bind on Windows) to a public
// resolver, falling back to the system resolver only if neither answers - a
// network that blocks outside DNS outright would otherwise lose every lookup
// while a tunnel is up.
func NewProtectedResolver(protect func(fd int) bool) IPLookuper {
	return &protectedResolver{protect: protect}
}

type protectedResolver struct {
	protect func(fd int) bool
}

func (r *protectedResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	// A literal address needs no lookup at all.
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	direct := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second, Control: ProtectControl(r.protect)}
			var lastErr error
			for _, server := range directDNSServers {
				conn, err := d.DialContext(ctx, network, server)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
	ips, err := direct.LookupIP(ctx, network, host)
	if err == nil && len(ips) > 0 {
		return ips, nil
	}
	return net.DefaultResolver.LookupIP(ctx, network, host)
}

// ProtectControl adapts a protect callback to net.Dialer.Control.
func ProtectControl(protect func(fd int) bool) func(string, string, syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var protectErr error
		if err := c.Control(func(fd uintptr) {
			if !protect(int(fd)) {
				protectErr = errors.New("failed to take the socket out of the VPN")
			}
		}); err != nil {
			return err
		}
		return protectErr
	}
}

// The last address each server hostname was successfully dialed on - the
// fallback when no resolver answers. A server's address rarely changes, and a
// connection to a stale one simply fails the handshake like any unreachable
// endpoint would, so trying it costs nothing a failed lookup didn't already.
var (
	lastGoodMu sync.Mutex
	lastGood   = map[string]string{}
)

func rememberGoodIP(host, ip string) {
	lastGoodMu.Lock()
	lastGood[host] = ip
	lastGoodMu.Unlock()
}

func lastGoodIP(host string) string {
	lastGoodMu.Lock()
	defer lastGoodMu.Unlock()
	return lastGood[host]
}

// resolveServer turns addr ("host:port") into the concrete addresses to dial,
// IPv4 first. A literal IP passes straight through.
func resolveServer(ctx context.Context, resolver IPLookuper, addr string) ([]string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(host) != nil {
		return []string{addr}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	ips, lookupErr := resolver.LookupIP(lookupCtx, "ip", host)
	cancel()
	if lookupErr != nil || len(ips) == 0 {
		if ip := lastGoodIP(host); ip != "" {
			diag.Event(diag.CatTunnel, "serverResolveFallback", "host", host, "ip", ip, "err", lookupErr)
			return []string{net.JoinHostPort(ip, port)}, nil
		}
		if lookupErr == nil {
			lookupErr = fmt.Errorf("no addresses")
		}
		return nil, fmt.Errorf("resolve %s: %w", host, lookupErr)
	}
	sort.SliceStable(ips, func(i, j int) bool { return ips[i].To4() != nil && ips[j].To4() == nil })
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = net.JoinHostPort(ip.String(), port)
	}
	return out, nil
}
