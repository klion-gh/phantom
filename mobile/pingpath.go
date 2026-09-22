//go:build !windows

package mobile

import (
	"sync"

	"phantom/internal/pingcheck"
)

// The protector every ping and auto-select probe applies to its sockets, set
// by the app while its own VPN is up and cleared when it goes down.
//
// Without it these pings ran through whatever tunnel was currently up: the
// app is not excluded from its own VpnService, so its sockets follow the same
// 0.0.0.0/0 route as everything else. A config's ping then measured device ->
// current server -> that server - a dead config could look alive through a
// live one, and every latency carried the current server's round trip on top.
var (
	pingProtectorMu sync.Mutex
	pingProtector   Protector
)

// SetPingProtector registers (or, with nil, clears) the protector pings use.
// Call it with the same protector as Start right after the tunnel is up, and
// with nil once it is torn down - protecting a socket against a VpnService
// that is no longer running fails, and with no tunnel there is nothing to
// protect against anyway.
func SetPingProtector(protector Protector) {
	pingProtectorMu.Lock()
	pingProtector = protector
	pingProtectorMu.Unlock()
}

// pingOptions is what every ping on this platform goes out with - read fresh
// per ping, so one that starts after the tunnel comes up is protected.
func pingOptions() pingcheck.Options {
	pingProtectorMu.Lock()
	p := pingProtector
	pingProtectorMu.Unlock()
	if p == nil {
		return pingcheck.Options{}
	}
	return pingcheck.Options{ProtectFD: p.Protect}
}
