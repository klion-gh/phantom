package main

import "phantom/internal/daemonapi"

// applyRoutingPlatform hands the smart-VPN settings to phantomd, whose engine
// the tunnel consults per flow. The returned channel closes once the service
// has resolved the new list (or wasn't running - then there is no tunnel to
// update, and the next Connect carries the settings anyway).
func applyRoutingPlatform(sites []string, smart bool) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		withDaemon(false, func(c *daemonapi.Client) error {
			return c.SetRouting(daemonapi.RoutingArgs{Smart: smart, Sites: sites})
		})
	}()
	return done
}
