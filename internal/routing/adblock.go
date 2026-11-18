package routing

import (
	"net"
)

// Ad blocking ("Блокировать рекламу YouTube"): names that serve Google's and
// YouTube's ads are answered with NXDOMAIN at the DNS level, on the device,
// before any query leaves it. This is what an ad-blocking DNS does, built into
// the client so it works on any server and in both modes.
//
// What it does and doesn't reach: the ad *requests* - the YouTube apps' ad
// decisions (googleads.g.doubleclick.net, pubads), the IMA SDK, banner and
// companion ads, ad tracking - go to these hosts, and with them unresolvable
// many ads never load. Video ads that YouTube stitches into the same
// googlevideo.com stream as the video itself can't be told apart by name and
// still play; nothing short of an in-browser blocker removes those.
var adDomains = []string{
	"doubleclick.net",
	"googleadservices.com",
	"googlesyndication.com",
	"googletagservices.com",
	"adservice.google.com",
	"imasdk.googleapis.com",
	"2mdn.net",
	"googleads.g.doubleclick.net",
	"ads.youtube.com",
	"youtube.cleverads.vn",
	"adsense.google.com",
	"partnerad.l.doubleclick.net",
}

// Public resolvers that browsers upgrade to DNS-over-HTTPS by themselves when
// they see them configured as the system's DNS (Chrome's "secure DNS:
// automatic") - which is what the Windows client sets on its adapter. A query
// sent that way never reaches the blocklist, so while blocking is on their
// DoH/DoT ports are refused and the browser falls back to ordinary DNS.
var dohResolvers = map[string]bool{
	"1.1.1.1": true, "1.0.0.1": true, "8.8.8.8": true, "8.8.4.4": true,
	"2606:4700:4700::1111": true, "2606:4700:4700::1001": true,
	"2001:4860:4860::8888": true, "2001:4860:4860::8844": true,
}

// SetAdBlock turns ad blocking on or off.
func (e *Engine) SetAdBlock(on bool) {
	if e.adBlock.Swap(on) != on {
		e.adsBlocked.Store(0)
	}
}

// AdBlock reports whether ad blocking is on.
func (e *Engine) AdBlock() bool { return e.adBlock.Load() }

// BlockDNSQuery reports whether a DNS query for qname is answered NXDOMAIN on
// the device instead of being resolved.
func (e *Engine) BlockDNSQuery(qname string) bool {
	if !e.adBlock.Load() || !e.ads.MatchesName(qname) {
		return false
	}
	e.adsBlocked.Add(1)
	return true
}

// BlockFlow reports whether a new flow is refused outright: DoH/DoT to the
// public resolvers above while ad blocking is on (see dohResolvers).
func (e *Engine) BlockFlow(network, target string) bool {
	if !e.adBlock.Load() || network != "tcp" && network != "udp" {
		return false
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil || (port != "443" && port != "853") {
		return false
	}
	return dohResolvers[host]
}
