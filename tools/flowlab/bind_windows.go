package main

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// IP_UNICAST_IF: send through this interface whatever the routing table says
// - the same mechanism the desktop app uses to keep pings out of its tunnel.
const ipUnicastIF = 31

func bindFD(iface int) func(fd int) bool {
	if iface == 0 {
		return nil
	}
	return func(fd int) bool { return bind(uintptr(fd), iface) == nil }
}

func bindControl(iface int) func(network, address string, c syscall.RawConn) error {
	if iface == 0 {
		return nil
	}
	return func(network, address string, c syscall.RawConn) error {
		var err error
		if cerr := c.Control(func(fd uintptr) { err = bind(fd, iface) }); cerr != nil {
			return cerr
		}
		return err
	}
}

func bind(fd uintptr, iface int) error {
	i := uint32(iface)
	// The option takes the index in network byte order.
	v := (i>>24)&0xff | (i>>8)&0xff00 | (i<<8)&0xff0000 | (i<<24)&0xff000000
	return windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIF, int(v))
}
