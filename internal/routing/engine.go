package routing

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"phantom/internal/diag"
)

// Mode is how the client decides what belongs in the tunnel.
type Mode string

const (
	// ModeAll is the classic full tunnel: every flow goes through Phantom.
	// The default, and what the client does when routing is untouched.
	ModeAll Mode = "all"

	// ModeSmart tunnels only flows whose destination matches the user's list
	// of sites, and sends everything else straight out. This is the
	// "unblock these specific services, leave the rest of my traffic alone"
	// mode - local services, banking, games and anything latency-sensitive
	// keep their normal path.
	ModeSmart Mode = "smart"
)

// Engine is the live routing state a running tunnel consults. One per tunnel;
// safe to reconfigure while flows are being routed, so toggling smart mode or
// editing the site list takes effect without reconnecting.
type Engine struct {
	mu      sync.RWMutex
	mode    Mode
	domains *DomainSet

	// Why each flow went where it went, since the last DiagFields. The
	// "internet went down in smart mode" question is mostly answered by
	// these: listed sites should be a small share, and anything counted
	// under emptyList means smart mode was tunnelling everything.
	whyListed, whyDNS, whyEmptyList, whyAllMode, whyBadTarget, whyDirect atomic.Int64
	// Split DNS: queries for listed names (tunnel) vs everything else (direct).
	dnsListed, dnsUnlisted atomic.Int64
	sites                  atomic.Int64
}

func NewEngine() *Engine {
	return &Engine{mode: ModeAll, domains: NewDomainSet()}
}

// Domains exposes the underlying set so the DNS sniffer can learn into it.
func (e *Engine) Domains() *DomainSet { return e.domains }

// SetMode switches routing strategy.
func (e *Engine) SetMode(m Mode) {
	e.mu.Lock()
	changed := e.mode != m
	e.mode = m
	e.mu.Unlock()
	if changed {
		diag.Event(diag.CatRoute, "mode", "mode", m, "sites", e.sites.Load())
	}
}

// Mode returns the current strategy.
func (e *Engine) Mode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// SetSites replaces the list of destinations that smart mode tunnels, and
// kicks off a background DNS resolution for every domain in the new list.
//
// Sniffing DNS as it crosses the tunnel (see SniffDNS) can't be the only
// source of IP<->domain knowledge: plenty of lookups never cross it. The OS
// resolver may already hold the answer from before the VPN came up, and a
// growing share of traffic uses DNS-over-HTTPS, which never reaches whatever
// resolver the tunnel is watching. Either way the flow reaches ShouldTunnel
// as a bare IP that matches nothing, and a listed site quietly behaves as
// though it isn't listed - this is what makes "add a site, it works right
// away" actually true instead of "works once something happens to trigger a
// fresh lookup through the tunnel". SniffDNS remains the live top-up for
// names not in the list yet, or CDN addresses picked up after this resolves.
func (e *Engine) SetSites(entries []string) {
	e.domains.Set(entries)
	e.sites.Store(int64(len(entries)))
	diag.Event(diag.CatRoute, "sites", "count", len(entries), "mode", e.Mode(), "empty", e.domains.Empty())
	seedDomainIPs(e.domains, entries)
}

// DiagFields returns (and resets) the per-flow decision counts, plus the
// engine's current state, as key/value pairs for a periodic summary line.
func (e *Engine) DiagFields() []any {
	return []any{
		"mode", e.Mode(),
		"sites", e.sites.Load(),
		"learnedIPs", e.domains.LearnedCount(),
		"whyListed", e.whyListed.Swap(0),
		"whyDNS", e.whyDNS.Swap(0),
		"whyEmptyList", e.whyEmptyList.Swap(0),
		"whyAllMode", e.whyAllMode.Swap(0),
		"whyBadTarget", e.whyBadTarget.Swap(0),
		"whyDirect", e.whyDirect.Swap(0),
		"dnsListed", e.dnsListed.Swap(0),
		"dnsUnlisted", e.dnsUnlisted.Swap(0),
	}
}

// TunnelDNSQuery decides where one DNS query goes under split DNS (see
// internal/netstack/splitdns.go): through the tunnel for a listed name - its
// answer is what teaches the engine the site's addresses, and resolving it
// locally is how it comes back poisoned - and directly for anything else, the
// way it would resolve with the VPN off.
//
// Outside smart mode, or with an empty list (where smart mode tunnels
// everything - see ShouldTunnel), every query rides the tunnel as before.
func (e *Engine) TunnelDNSQuery(qname string) bool {
	if e.Mode() != ModeSmart || e.domains.Empty() {
		return true
	}
	if e.domains.MatchesName(qname) {
		e.dnsListed.Add(1)
		return true
	}
	e.dnsUnlisted.Add(1)
	return false
}

// seedDomainIPs resolves each name-based entry once, immediately, and feeds
// the answers into set. Literal IPs and CIDRs already match without any DNS
// involved, so only names need this.
func seedDomainIPs(set *DomainSet, entries []string) {
	for _, raw := range entries {
		name := strings.ToLower(strings.TrimSpace(raw))
		name = strings.TrimPrefix(name, "https://")
		name = strings.TrimPrefix(name, "http://")
		if slash := strings.IndexByte(name, '/'); slash >= 0 {
			name = name[:slash]
		}
		name = strings.TrimSuffix(name, ".")
		name = strings.TrimPrefix(name, "*.")
		if name == "" || net.ParseIP(name) != nil {
			continue
		}
		go func(host string) {
			ips, err := net.LookupIP(host)
			if err != nil || len(ips) == 0 {
				// A listed site's own name - the user typed it, so logging it
				// reveals nothing they didn't choose to put in the list.
				diag.Event(diag.CatRoute, "seedResolveFail", "site", host, "err", err)
				return
			}
			set.Learn(host, ips)
		}(name)
	}
}

// ShouldTunnel decides one flow. target is "ip:port" as seen by netstack.
func (e *Engine) ShouldTunnel(network, target string) bool {
	e.mu.RLock()
	mode := e.mode
	e.mu.RUnlock()

	if mode != ModeSmart {
		e.whyAllMode.Add(1)
		return true
	}
	// An empty list in smart mode would send *everything* direct - i.e.
	// silently behave as if the VPN were off. Tunnelling instead is the safer
	// reading of "I turned a VPN on": the UI is what stops the user getting
	// here with nothing listed, and this is the backstop if they do.
	if e.domains.Empty() {
		e.whyEmptyList.Add(1)
		return true
	}

	host, port, err := net.SplitHostPort(target)
	if err != nil {
		e.whyBadTarget.Add(1)
		return true
	}
	// DNS that reaches this rule rides the tunnel. UDP DNS normally doesn't
	// get here at all: split DNS decides it per queried name instead (see
	// TunnelDNSQuery). What's left - DNS over TCP, or a platform without a
	// direct path - keeps the old rule: resolving a blocked domain locally is
	// how it comes back poisoned, and the tunnel is where the sniffer learns.
	if port == "53" {
		e.whyDNS.Add(1)
		return true
	}
	if e.domains.Match(host) {
		e.whyListed.Add(1)
		return true
	}
	e.whyDirect.Add(1)
	return false
}
