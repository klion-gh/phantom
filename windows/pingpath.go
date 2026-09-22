package main

import (
	"sync/atomic"

	"golang.org/x/sys/windows"

	"phantom/internal/pingcheck"
)

// The physical interface config pings and auto-select probes are pinned to
// while a tunnel is up, 0 while none is. Set by StartWindows from the same
// index split tunneling already captures (before the tunnel's 0.0.0.0/0
// route exists, so it names the real adapter), cleared by Stop.
//
// Without it these pings followed the tunnel's default route like everything
// else: only the *current* server's IPs get /32 bypass routes, so a ping to
// any other config went device -> current server -> that server. A dead config
// could look alive through a live one, and every latency carried the current
// server's round trip on top.
var pingIfIndex atomic.Uint32

func setPingInterface(ifIndex uint32) { pingIfIndex.Store(ifIndex) }

// pingOptions is what every ping on this platform goes out with - read fresh
// per ping, so one that starts after the tunnel comes up is pinned to the
// physical interface, and one after it goes down uses the plain default route.
func pingOptions() pingcheck.Options {
	ifIndex := pingIfIndex.Load()
	if ifIndex == 0 {
		return pingcheck.Options{}
	}
	return pingcheck.Options{ProtectFD: func(fd int) bool {
		return bindSocketToInterface(windows.Handle(uintptr(fd)), ifIndex) == nil
	}}
}

// bindSocketToInterface forces a socket out through ifIndex regardless of the
// routing table - the same IP_UNICAST_IF mechanism dialDirect uses for
// excluded apps (see splittunnel.go). Pings only ever dial IPv4 (pingcheck
// resolves "ip4"), but the IPv6 option is tried as a fallback so a socket Go
// happened to open dual-stack still gets pinned rather than silently tunneled.
func bindSocketToInterface(s windows.Handle, ifIndex uint32) error {
	v := int(hostToNetworkIfIndex(ifIndex))
	err := windows.SetsockoptInt(s, windows.IPPROTO_IP, ipUnicastIF, v)
	if err == nil {
		return nil
	}
	if err6 := windows.SetsockoptInt(s, windows.IPPROTO_IPV6, ipv6UnicastIF, int(ifIndex)); err6 == nil {
		return nil
	}
	return err
}
