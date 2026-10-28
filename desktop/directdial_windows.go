package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Sending a connection around the tunnel on Windows.
//
// With the tunnel's 0.0.0.0/0 route installed, every socket this process opens
// would normally follow it into the tunnel. Two things must not: smart mode's
// direct flows (sites that aren't on the list, see installRouting) and config
// pings / auto-select probes (see pingpath.go). Both bind their socket to the
// physical interface captured before that route existed, via IP_UNICAST_IF -
// which forces the interface regardless of the routing table's own choice,
// needs no driver and touches nothing system-wide.
//
// (This used to live in splittunnel.go alongside per-app split tunneling,
// which has since been removed; the interface binding outlived it because
// smart mode and pings were built on the same mechanism.)

const (
	ipUnicastIF   = 31 // IP_UNICAST_IF
	ipv6UnicastIF = 31 // IPV6_UNICAST_IF (same numeric value, different level)
)

var (
	iphlpapi             = syscall.NewLazyDLL("iphlpapi.dll")
	procGetBestInterface = iphlpapi.NewProc("GetBestInterface")
)

// dialDirect connects to target the same way a normal app would - except the
// dial's own socket is bound to physicalIfIndex via IP_UNICAST_IF/
// IPV6_UNICAST_IF, so it goes out that specific interface regardless of the
// tunnel's default route.
func dialDirect(network, target string, physicalIfIndex uint32) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, c syscall.RawConn) error {
			isIPv6 := strings.Contains(address, "[") || strings.Count(address, ":") > 1
			var ctrlErr error
			err := c.Control(func(fd uintptr) {
				if isIPv6 {
					ctrlErr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, ipv6UnicastIF, int(hostToNetworkIfIndex(physicalIfIndex)))
				} else {
					ctrlErr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIF, int(hostToNetworkIfIndex(physicalIfIndex)))
				}
			})
			if err != nil {
				return err
			}
			return ctrlErr
		},
	}
	return dialer.DialContext(context.Background(), network, target)
}

// hostToNetworkIfIndex byte-swaps the interface index the way IP_UNICAST_IF
// expects it (as if it were a 4-byte network-order value, matching htonl) -
// documented Windows behavior for this particular option, unlike most other
// setsockopt values which take a plain host-order integer.
func hostToNetworkIfIndex(ifIndex uint32) uint32 {
	return (ifIndex>>24)&0xff | (ifIndex>>8)&0xff00 | (ifIndex<<8)&0xff0000 | (ifIndex<<24)&0xff000000
}

// bestInterfaceIndex returns the interface Windows would currently use to
// reach ip - must be called *before* the tunnel's 0.0.0.0/0 route is added
// (see StartWindows), otherwise it would just return the tunnel's own
// interface instead of the real physical one.
func bestInterfaceIndex(ip string) (uint32, error) {
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return 0, fmt.Errorf("not an IPv4 address: %q", ip)
	}
	// GetBestInterface takes the destination as a raw 4-byte IPv4Addr blob
	// (the same layout net.IP.To4() already gives us), not a host-order int.
	dest := *(*uint32)(unsafe.Pointer(&parsed[0]))
	var ifIndex uint32
	ret, _, _ := procGetBestInterface.Call(uintptr(dest), uintptr(unsafe.Pointer(&ifIndex)))
	if ret != 0 {
		return 0, fmt.Errorf("GetBestInterface failed: code %d", ret)
	}
	return ifIndex, nil
}
