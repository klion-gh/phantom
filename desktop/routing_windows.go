package main

import (
	"io"
	"log"

	"phantom/internal/netstack"
	"phantom/internal/routing"
)

// engine is the live routing state the running tunnel consults. Shared rather
// than per-tunnel so the UI can edit it while disconnected and have it apply
// to whatever connects next.
var engine = routing.NewEngine()

// applyRoutingPlatform puts the smart-VPN settings into this process's own
// engine, which the in-process tunnel consults per flow.
func applyRoutingPlatform(sites []string, smart bool) <-chan struct{} {
	seeded := engine.SetSites(sites)
	if smart {
		engine.SetMode(routing.ModeSmart)
	} else {
		engine.SetMode(routing.ModeAll)
	}
	return seeded
}

// installRouting hooks the shared engine into a freshly built tunnel.
// physicalIfIndex is the real interface captured before the tunnel's default
// route existed - the only way a direct dial escapes the tunnel we just
// created (see directdial.go).
func installRouting(inner *netstack.Tunnel, physicalIfIndex uint32, haveIfIndex bool) {
	// No SetDNSUpstream call here, unlike mobile.go's Android setup - Windows
	// hands out real DNS servers (see wintun.go's configureInterface), so
	// there is no placeholder address for it to rewrite. See wintun.go for
	// why Windows deliberately doesn't use the same placeholder+rewrite trick.
	if !haveIfIndex {
		// Without a physical interface to bind to there is no way to send a
		// flow around the tunnel, so smart mode can't be honoured. Leaving the
		// hooks off means everything is tunnelled, which is the safe reading.
		log.Printf("[routing] no physical interface index - smart routing unavailable this session")
		return
	}
	inner.SetRouting(
		func(network, target string) netstack.RouteDecision {
			if engine.ShouldTunnel(network, target) {
				return netstack.RouteTunnel
			}
			return netstack.RouteDirect
		},
		func(network, target string) (io.ReadWriteCloser, error) {
			return dialDirect(network, target, physicalIfIndex)
		},
	)
	inner.SetDNSObserver(func(stream io.ReadWriteCloser) io.ReadWriteCloser {
		return routing.SniffDNS(stream, engine.Domains())
	})
	// The engine's own counters ride along on netstack's periodic summary, so
	// one line says both what flowed and why it went where it did.
	inner.SetDiagExtra(engine.DiagFields)
	// Split DNS - see internal/netstack/splitdns.go: in smart mode only
	// queries for listed names ride the tunnel; the rest go out the physical
	// interface to whichever resolver Windows asked (1.1.1.1/8.8.8.8, set on
	// the tunnel adapter), retried through the tunnel if that goes unanswered.
	inner.SetDNSRouter(func(qname string) netstack.RouteDecision {
		if engine.TunnelDNSQuery(qname) {
			return netstack.RouteTunnel
		}
		return netstack.RouteDirect
	})
}
