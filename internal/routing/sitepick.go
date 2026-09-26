package routing

import (
	"net"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// SiteMatch describes one host (the site open in a browser tab) against the
// smart-VPN list: whether it is already covered, and what adding it would add.
// Used by the browser extension bridge, which only ever knows a page's host
// and has to turn that into a sensible list entry.
type SiteMatch struct {
	// Host is the normalised host the question was asked about.
	Host string `json:"host"`
	// Listed reports whether the list already routes Host, and ListedBy the
	// entry that does (which may be a parent domain: "youtube.com" for
	// "www.youtube.com").
	Listed   bool   `json:"listed"`
	ListedBy string `json:"listedBy,omitempty"`
	// Service is the Популярные ресурсы entry Host belongs to, if any. Adding
	// such a host adds the whole service - every domain it needs, not just
	// the one the tab happens to show - the same way the picker does.
	Service        string   `json:"service,omitempty"`
	ServiceDomains []string `json:"serviceDomains,omitempty"`
	// Options are the entries Host could be added as, broadest first: the
	// registrable domain ("example.co.uk"), then each longer suffix down to
	// Host itself. Suggested is the one offered by default - the registrable
	// domain, since sites routinely spread over subdomains of it.
	Options   []string `json:"options"`
	Suggested string   `json:"suggested"`
}

// NormalizeHost lowercases a host and strips what a browser URL might carry
// around it (a port, IPv6 brackets, a trailing dot). Returns "" for anything
// that isn't a usable host.
func NormalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if hp, _, err := net.SplitHostPort(h); err == nil {
		h = hp
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	if h == "" || strings.ContainsAny(h, "/ @:") && net.ParseIP(h) == nil {
		return ""
	}
	return h
}

// entryName reduces a list entry to the domain name it matches, the same way
// DomainSet.Set reads it, or "" for an entry that isn't a name (a CIDR, an IP
// literal, a comment).
func entryName(raw string) string {
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "" || strings.HasPrefix(e, "#") {
		return ""
	}
	e = strings.TrimPrefix(e, "https://")
	e = strings.TrimPrefix(e, "http://")
	if slash := strings.IndexByte(e, '/'); slash >= 0 {
		if _, _, err := net.ParseCIDR(e); err == nil {
			return ""
		}
		e = e[:slash]
	}
	e = strings.TrimPrefix(strings.TrimSuffix(e, "."), "*.")
	if net.ParseIP(e) != nil {
		return ""
	}
	return e
}

// entryCovers reports whether list entry raw routes host.
func entryCovers(raw, host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		e := strings.ToLower(strings.TrimSpace(raw))
		if _, network, err := net.ParseCIDR(e); err == nil {
			return network.Contains(ip)
		}
		if lit := net.ParseIP(e); lit != nil {
			return lit.Equal(ip)
		}
		return false
	}
	name := entryName(raw)
	return name != "" && (host == name || strings.HasSuffix(host, "."+name))
}

// serviceFor returns the catalogue service whose domains cover host.
func serviceFor(host string) (PopularResource, bool) {
	for _, r := range PopularResources() {
		for _, d := range r.Domains {
			if entryName(d) != "" && entryCovers(d, host) {
				return r, true
			}
		}
	}
	return PopularResource{}, false
}

// MatchSite answers "is this host routed, and what would adding it add?"
func MatchSite(host string, sites []string) SiteMatch {
	h := NormalizeHost(host)
	m := SiteMatch{Host: h, Options: []string{}}
	if h == "" {
		return m
	}
	for _, s := range sites {
		if entryCovers(s, h) {
			m.Listed, m.ListedBy = true, s
			break
		}
	}
	if r, ok := serviceFor(h); ok {
		m.Service, m.ServiceDomains = r.Name, r.Domains
	}
	m.Options = hostOptions(h)
	m.Suggested = m.Options[0]
	return m
}

// hostOptions lists the entries h could be added as, broadest first.
func hostOptions(h string) []string {
	if net.ParseIP(h) != nil {
		return []string{h}
	}
	base, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil {
		// A bare public suffix or a single-label name: nothing broader to
		// offer than the host itself.
		return []string{h}
	}
	opts := []string{base}
	rest := strings.TrimSuffix(h, "."+base)
	if rest == h || rest == "" {
		return opts
	}
	labels := strings.Split(rest, ".")
	for i := len(labels) - 1; i >= 0; i-- {
		opts = append(opts, strings.Join(labels[i:], ".")+"."+base)
	}
	return opts
}

// AddSite returns sites with host added. entry picks which of the host's
// options to add ("" for the suggested one); a host that belongs to a
// catalogue service adds the whole service instead, since half a service
// (the page but not its media hosts) is exactly the failure the catalogue
// exists to prevent. entry must be one of the host's options - anything else
// is refused, so a caller can't use this to add arbitrary entries.
func AddSite(sites []string, host, entry string) ([]string, bool) {
	m := MatchSite(host, sites)
	if m.Host == "" {
		return sites, false
	}
	var add []string
	switch {
	case m.Service != "" && entry == "":
		add = m.ServiceDomains
	case entry == "":
		add = []string{m.Suggested}
	default:
		entry = strings.ToLower(strings.TrimSpace(entry))
		ok := false
		for _, o := range m.Options {
			if o == entry {
				ok = true
				break
			}
		}
		if !ok {
			return sites, false
		}
		add = []string{entry}
	}
	out := append([]string{}, sites...)
	for _, a := range add {
		if !containsFold(out, a) {
			out = append(out, a)
		}
	}
	return out, true
}

// RemoveSite returns sites without whatever routes host. Where the entry
// doing so is part of a fully listed catalogue service, the whole service
// goes with it - it shows as one tile in the apps, and removing half of it
// would leave a service that neither works nor shows up as a service.
func RemoveSite(sites []string, host string) []string {
	h := NormalizeHost(host)
	if h == "" {
		return sites
	}
	drop := map[string]bool{}
	for _, s := range sites {
		if !entryCovers(s, h) {
			continue
		}
		drop[strings.ToLower(s)] = true
		for _, r := range PopularResources() {
			if containsFold(r.Domains, s) && allListed(sites, r.Domains) {
				for _, d := range r.Domains {
					drop[strings.ToLower(d)] = true
				}
			}
		}
	}
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		if !drop[strings.ToLower(s)] {
			out = append(out, s)
		}
	}
	return out
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func allListed(sites, domains []string) bool {
	for _, d := range domains {
		if !containsFold(sites, d) {
			return false
		}
	}
	return true
}
