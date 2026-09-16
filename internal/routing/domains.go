// Package routing decides, per connection, whether traffic should go through
// the Phantom tunnel or straight out the physical interface - and, when the
// user has picked several configs, which one the tunnel should currently be
// built on.
//
// The two halves are deliberately independent:
//
//   - DomainSet (this file) answers "does this flow belong to a site the user
//     asked to route through the VPN?". It is consulted once per new flow by
//     internal/netstack, never per packet.
//   - Selector (selector.go) answers "which of the user's configs should the
//     tunnel be using right now?", probing them in the background and
//     switching only when it's genuinely worth it.
package routing

import (
	"net"
	"strings"
	"sync"
	"time"
)

// How long an IP learned from a DNS answer keeps counting as belonging to that
// domain. Long enough to outlive the short TTLs big sites hand out (often 60s,
// which would otherwise expire mid-session and drop the flow out of the
// tunnel), short enough that an address recycled to someone else stops being
// tunneled on the user's behalf.
const learnedTTL = 30 * time.Minute

// Ceiling on the learned map so a long uptime (or a hostile resolver spraying
// answers) can't grow it without bound. Well above what a normal session
// produces for a handful of listed domains.
const maxLearned = 8192

// DomainSet matches a connection's destination against the user's list of
// "route this through the VPN" entries.
//
// An entry is one of:
//
//   - a domain ("youtube.com") - matches that name and any subdomain of it,
//     via the IPs seen in DNS answers for those names (see Learn)
//   - a literal IP ("142.250.74.14")
//   - a CIDR block ("142.250.0.0/16")
//
// Domains can't be matched directly: by the time a TCP/UDP flow reaches the
// tunnel, the app has already resolved the name and is connecting to a bare
// IP. Learn bridges that gap by watching the DNS answers that flow through the
// tunnel first - see the sniffer in netstack.
//
// Safe for concurrent use: Match runs on every new flow, Learn on every DNS
// response, Set whenever the user edits the list.
type DomainSet struct {
	mu       sync.RWMutex
	suffixes []string
	nets     []*net.IPNet
	literals map[string]bool
	learned  map[string]time.Time
}

func NewDomainSet() *DomainSet {
	return &DomainSet{
		literals: map[string]bool{},
		learned:  map[string]time.Time{},
	}
}

// Set replaces the entry list. Unparseable entries are skipped rather than
// failing the whole update - the list is user-typed, and one bad line
// shouldn't silently disable routing for the good ones.
//
// Learned IPs are dropped on every update: they were derived from the previous
// list, and keeping them would leave a removed domain quietly tunneled until
// its entries aged out.
func (d *DomainSet) Set(entries []string) {
	var suffixes []string
	var nets []*net.IPNet
	literals := map[string]bool{}

	for _, raw := range entries {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" || strings.HasPrefix(e, "#") {
			continue
		}
		// Tolerate entries pasted straight out of a browser's address bar.
		e = strings.TrimPrefix(e, "https://")
		e = strings.TrimPrefix(e, "http://")
		if slash := strings.IndexByte(e, '/'); slash >= 0 {
			// Could be a CIDR ("10.0.0.0/8") or a pasted path
			// ("example.com/watch"). Only treat it as CIDR if it parses.
			if _, network, err := net.ParseCIDR(e); err == nil {
				nets = append(nets, network)
				continue
			}
			e = e[:slash]
		}
		e = strings.TrimSuffix(e, ".")
		e = strings.TrimPrefix(e, "*.")
		if e == "" {
			continue
		}
		if ip := net.ParseIP(e); ip != nil {
			literals[ip.String()] = true
			continue
		}
		suffixes = append(suffixes, e)
	}

	d.mu.Lock()
	d.suffixes = suffixes
	d.nets = nets
	d.literals = literals
	d.learned = map[string]time.Time{}
	d.mu.Unlock()
}

// Empty reports whether the set would match nothing at all. Callers use this
// to avoid switching into a routing mode that would send *everything* direct
// because the user hasn't listed anything yet.
func (d *DomainSet) Empty() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.suffixes) == 0 && len(d.nets) == 0 && len(d.literals) == 0
}

// MatchesName reports whether a DNS name is one the user listed - "youtube.com"
// matches "youtube.com" and "www.youtube.com", but not "notyoutube.com".
func (d *DomainSet) MatchesName(name string) bool {
	n := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if n == "" {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return matchesAnySuffix(n, d.suffixes)
}

func matchesAnySuffix(name string, suffixes []string) bool {
	for _, s := range suffixes {
		if name == s || strings.HasSuffix(name, "."+s) {
			return true
		}
	}
	return false
}

// Learn records that ips belong to name, if name is one of the listed domains.
// Called from the DNS sniffer for every answer that passes through the tunnel.
func (d *DomainSet) Learn(name string, ips []net.IP) {
	if len(ips) == 0 {
		return
	}
	n := strings.ToLower(strings.TrimSuffix(name, "."))

	d.mu.Lock()
	defer d.mu.Unlock()
	if !matchesAnySuffix(n, d.suffixes) {
		return
	}
	now := time.Now()
	if len(d.learned) >= maxLearned {
		d.pruneLocked(now)
		// Still full after pruning: drop the whole table rather than let it
		// grow. Worst case the next DNS answer re-learns what's actually in
		// use, which costs one flow going direct.
		if len(d.learned) >= maxLearned {
			d.learned = map[string]time.Time{}
		}
	}
	for _, ip := range ips {
		d.learned[ip.String()] = now
	}
}

func (d *DomainSet) pruneLocked(now time.Time) {
	for ip, at := range d.learned {
		if now.Sub(at) > learnedTTL {
			delete(d.learned, ip)
		}
	}
}

// Match reports whether a connection to host (a bare IP, as seen by netstack)
// should be routed through the tunnel.
func (d *DomainSet) Match(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		// netstack always hands us a literal IP; a name here would mean a
		// caller outside that path, in which case the suffix list is the only
		// thing that can answer.
		return d.MatchesName(host)
	}
	key := ip.String()

	d.mu.RLock()
	if d.literals[key] {
		d.mu.RUnlock()
		return true
	}
	for _, n := range d.nets {
		if n.Contains(ip) {
			d.mu.RUnlock()
			return true
		}
	}
	at, ok := d.learned[key]
	d.mu.RUnlock()

	return ok && time.Since(at) <= learnedTTL
}
