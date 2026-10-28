package main

import (
	"errors"

	"phantom/internal/daemonapi"
	"phantom/internal/pingcheck"
)

// pingConfig pings a config's server from outside the tunnel. This process
// can't mark its own sockets (that takes CAP_NET_ADMIN), so while phantomd is
// running the service makes the handshake; when it isn't, there is no tunnel
// and the default route already goes around nothing.
func pingConfig(configYAML string) (pingcheck.Result, error) {
	var reply daemonapi.PingReply
	err := withDaemon(false, func(c *daemonapi.Client) error {
		var callErr error
		reply, callErr = c.Ping(configYAML)
		return callErr
	})
	if errors.Is(err, daemonapi.ErrNotRunning) {
		return pingcheck.PingWith(configYAML, pingcheck.Options{})
	}
	if err != nil {
		return pingcheck.Result{}, err
	}
	return pingcheck.Result{IP: reply.IP, LatencyMs: reply.LatencyMs}, nil
}

// provisionProtect: server setup dials from this process, which can't step
// around the tunnel on Linux - with a tunnel up its SSH session rides it
// (reaching the new server through the current one, which works), and with
// none it goes direct.
func provisionProtect() func(fd int) bool { return nil }
