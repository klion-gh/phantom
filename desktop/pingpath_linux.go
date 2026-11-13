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
	configYAML = withRelaySetting(configYAML)
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

// checkDirect and checkConfigInternet run in phantomd for the same reason
// pingConfig does; with no service there's no tunnel to step around.
func checkDirect() error {
	_, err := daemonCheck(daemonapi.CheckArgs{Kind: daemonapi.CheckDirect})
	if errors.Is(err, daemonapi.ErrNotRunning) {
		return pingcheck.Direct(pingcheck.Options{})
	}
	return err
}

func checkConfigInternet(configYAML string) (int64, error) {
	configYAML = withRelaySetting(configYAML)
	ms, err := daemonCheck(daemonapi.CheckArgs{Kind: daemonapi.CheckConfig, YAML: configYAML})
	if errors.Is(err, daemonapi.ErrNotRunning) {
		r, err := pingcheck.Internet(configYAML, pingcheck.Options{})
		return r.LatencyMs, err
	}
	return ms, err
}

func daemonCheck(args daemonapi.CheckArgs) (int64, error) {
	var reply daemonapi.CheckReply
	err := withDaemon(false, func(c *daemonapi.Client) error {
		var callErr error
		reply, callErr = c.Check(args)
		return callErr
	})
	return reply.LatencyMs, err
}

// pingRelay times a relay's TCP port. This process can't mark its sockets
// (see pingConfig), so while the tunnel is up the connect may take the
// tunnel's route and the number is only a rough one.
func pingRelay(address string) (pingcheck.Result, error) {
	return pingcheck.PingRelay(address, pingcheck.Options{})
}

// provisionProtect: server setup dials from this process, which can't step
// around the tunnel on Linux - with a tunnel up its SSH session rides it
// (reaching the new server through the current one, which works), and with
// none it goes direct.
func provisionProtect() func(fd int) bool { return nil }
