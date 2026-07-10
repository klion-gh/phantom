package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// --- Why this approach instead of a firewall driver ---
//
// Windows has no per-socket "exempt this connection from the VPN" API like
// Android's VpnService.protect(), and routing table entries can only key on
// *destination* address, never on which process opened the connection - so
// routing tricks alone can't single out one app. The technically-correct
// mechanism real VPN clients use for this is the Windows Filtering Platform
// (a kernel-level per-process firewall API), which is substantial kernel-
// adjacent surface to get right and out of scope here.
//
// Instead, this uses a narrower technique available entirely from userspace:
// for every new connection netstack forwards (see internal/netstack's
// BypassFunc hook), look up which process owns the local port that's already
// been assigned to it (GetExtendedTcpTable/GetExtendedUdpTable - Windows
// tracks socket ownership independent of which route the packets end up
// taking), and if that process's exe is on the excluded list, dial the real
// destination directly instead of tunneling it - with the dial's socket
// bound to the original physical interface via IP_UNICAST_IF, which forces
// outbound routing through that interface regardless of what the routing
// table's normal destination-based lookup would otherwise pick (our tunnel's
// 0.0.0.0/0 route). This needs no driver and touches nothing system-wide;
// it only affects sockets this process itself opens for excluded apps.

const (
	ipUnicastIF   = 31 // IP_UNICAST_IF
	ipv6UnicastIF = 31 // IPV6_UNICAST_IF (same numeric value, different level)
)

var (
	iphlpapi                = syscall.NewLazyDLL("iphlpapi.dll")
	procGetExtendedTCPTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUDPTable = iphlpapi.NewProc("GetExtendedUdpTable")
	procGetBestInterface    = iphlpapi.NewProc("GetBestInterface")
)

const (
	tcpTableOwnerPIDAll = 5 // TCP_TABLE_OWNER_PID_ALL
	udpTableOwnerPID    = 1 // UDP_TABLE_OWNER_PID
	errInsufficientBuf  = 122
)

// newSplitTunnelBypass returns a netstack.BypassFunc bound to physicalIfIndex
// (the real network interface's index, captured *before* the tunnel's
// 0.0.0.0/0 route was added - see StartWindows) that reads the current
// excluded-apps list fresh on every call (cheap: a small JSON file read,
// and only invoked once per new connection, not per packet).
func newSplitTunnelBypass(physicalIfIndex uint32) func(network string, localPort uint16, target string) io.ReadWriteCloser {
	return func(network string, localPort uint16, target string) io.ReadWriteCloser {
		apps, err := loadExcludedApps()
		if err != nil || len(apps) == 0 {
			return nil
		}
		exePath, err := processOwnerExePath(network == "tcp", localPort)
		if err != nil || exePath == "" || !isExcludedProcess(exePath, apps) {
			return nil
		}
		conn, err := dialDirect(network, target, physicalIfIndex)
		if err != nil {
			return nil // falls back to tunneling - see openRemote in netstack.go
		}
		return conn
	}
}

func isExcludedProcess(exePath string, apps []ExcludedApp) bool {
	base := strings.ToLower(filepath.Base(exePath))
	for _, a := range apps {
		if strings.ToLower(filepath.Base(a.ExePath)) == base {
			return true
		}
	}
	return false
}

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

// processOwnerExePath resolves the exe path of whichever process currently
// owns localPort (as seen by Windows' own TCP/UDP tables - this reflects
// normal socket ownership regardless of which route its packets end up
// taking). IPv6 isn't handled here; an excluded app's IPv6 connections will
// just fall through to being tunneled rather than erroring, which is a safe
// default rather than a broken one.
func processOwnerExePath(isTCP bool, localPort uint16) (string, error) {
	pid, err := findOwningPID(isTCP, localPort)
	if err != nil {
		return "", err
	}
	return exePathForPID(pid)
}

func findOwningPID(isTCP bool, localPort uint16) (uint32, error) {
	var size uint32
	var proc *syscall.LazyProc
	var tableClass uintptr
	if isTCP {
		proc = procGetExtendedTCPTable
		tableClass = tcpTableOwnerPIDAll
	} else {
		proc = procGetExtendedUDPTable
		tableClass = udpTableOwnerPID
	}

	// First call with a nil buffer just to learn the required size, then
	// retry with a real buffer - growing once more if the table changed size
	// in between (rare, but the API can race with new connections appearing).
	for attempt := 0; attempt < 3; attempt++ {
		ret, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(windows.AF_INET), tableClass, 0)
		if ret != 0 && ret != errInsufficientBuf {
			return 0, fmt.Errorf("size query failed: code %d", ret)
		}
		if size == 0 {
			return 0, fmt.Errorf("empty table")
		}
		buf := make([]byte, size)
		ret, _, _ = proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(windows.AF_INET), tableClass, 0)
		if ret == errInsufficientBuf {
			continue // table grew between the two calls - retry with the new size
		}
		if ret != 0 {
			return 0, fmt.Errorf("table query failed: code %d", ret)
		}
		if isTCP {
			return findTCPRowPID(buf, localPort)
		}
		return findUDPRowPID(buf, localPort)
	}
	return 0, fmt.Errorf("owning process table kept changing size")
}

// findTCPRowPID walks a raw MIB_TCPTABLE_OWNER_PID buffer. Each row is 24
// bytes: dwState(4) dwLocalAddr(4) dwLocalPort(4) dwRemoteAddr(4)
// dwRemotePort(4) dwOwningPid(4). Only the low 16 bits of the port fields are
// used, and - unlike the address fields, which are raw byte blobs already in
// the right order - the port occupies those low 16 bits in big-endian order,
// so the first two bytes of that 4-byte field are the real port as-is.
func findTCPRowPID(buf []byte, localPort uint16) (uint32, error) {
	const rowSize = 24
	if len(buf) < 4 {
		return 0, fmt.Errorf("short buffer")
	}
	numEntries := int(leUint32(buf[0:4]))
	for i := 0; i < numEntries; i++ {
		off := 4 + i*rowSize
		if off+rowSize > len(buf) {
			break
		}
		row := buf[off : off+rowSize]
		port := beUint16(row[8:10])
		if port == localPort {
			return leUint32(row[20:24]), nil
		}
	}
	return 0, fmt.Errorf("no tcp row owns port %d", localPort)
}

// findUDPRowPID walks a raw MIB_UDPTABLE_OWNER_PID buffer. Each row is 12
// bytes: dwLocalAddr(4) dwLocalPort(4) dwOwningPid(4) - same port encoding
// as the TCP table.
func findUDPRowPID(buf []byte, localPort uint16) (uint32, error) {
	const rowSize = 12
	if len(buf) < 4 {
		return 0, fmt.Errorf("short buffer")
	}
	numEntries := int(leUint32(buf[0:4]))
	for i := 0; i < numEntries; i++ {
		off := 4 + i*rowSize
		if off+rowSize > len(buf) {
			break
		}
		row := buf[off : off+rowSize]
		port := beUint16(row[4:6])
		if port == localPort {
			return leUint32(row[8:12]), nil
		}
	}
	return 0, fmt.Errorf("no udp row owns port %d", localPort)
}

func leUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func beUint16(b []byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}

func exePathForPID(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}
