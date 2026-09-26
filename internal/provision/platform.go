package provision

import (
	"context"
	"net"

	"phantom/internal/transport"
)

// Around returns Options whose connections go around the device's own VPN
// when protect is set (VpnService.protect on Android, a bind to the physical
// interface on Windows - the same hook the apps' pings use), and over the
// default route when it is nil. Setting up a server has to reach it directly:
// through the tunnel it would be talking to the current server instead, and
// with a half-working tunnel it might not reach anything.
func Around(protect func(fd int) bool, verify func(configYAML string) (int64, error)) Options {
	d := net.Dialer{}
	var lookup transport.IPLookuper = net.DefaultResolver
	if protect != nil {
		d.Control = transport.ProtectControl(protect)
		lookup = transport.NewProtectedResolver(protect)
	}
	return Options{
		Dial: d.DialContext,
		Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			return lookup.LookupIP(ctx, "ip4", host)
		},
		Verify: verify,
	}
}
