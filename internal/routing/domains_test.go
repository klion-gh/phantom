package routing

import (
	"net"
	"testing"
)

func TestDomainSetMatchesLiteralsAndCIDR(t *testing.T) {
	d := NewDomainSet()
	d.Set([]string{"142.250.74.14", "10.20.0.0/16", "youtube.com"})

	if !d.Match("142.250.74.14") {
		t.Error("expected a literal IP entry to match")
	}
	if !d.Match("10.20.5.7") {
		t.Error("expected an address inside the CIDR to match")
	}
	if d.Match("10.21.5.7") {
		t.Error("expected an address outside the CIDR not to match")
	}
	if d.Match("8.8.8.8") {
		t.Error("expected an unrelated address not to match")
	}
}

func TestDomainSetSuffixMatching(t *testing.T) {
	d := NewDomainSet()
	d.Set([]string{"youtube.com"})

	for _, name := range []string{"youtube.com", "www.youtube.com", "a.b.youtube.com", "YouTube.com", "youtube.com."} {
		if !d.MatchesName(name) {
			t.Errorf("expected %q to match the listed domain", name)
		}
	}
	// The classic suffix-matching trap: a different domain that merely ends
	// with the same text must not match.
	for _, name := range []string{"notyoutube.com", "youtube.com.evil.net", "example.com"} {
		if d.MatchesName(name) {
			t.Errorf("expected %q not to match", name)
		}
	}
}

func TestDomainSetNormalizesPastedEntries(t *testing.T) {
	d := NewDomainSet()
	d.Set([]string{
		"  https://www.YouTube.com/watch  ", // pasted from an address bar
		"*.example.org",                     // wildcard notation
		"# a comment",
		"",
	})

	if !d.MatchesName("www.youtube.com") {
		t.Error("expected a pasted URL to be reduced to its host")
	}
	if !d.MatchesName("cdn.example.org") {
		t.Error("expected a wildcard entry to behave as a suffix")
	}
	if !d.MatchesName("example.org") {
		t.Error("expected a wildcard entry to match the bare domain too")
	}
}

func TestDomainSetLearnsOnlyListedNames(t *testing.T) {
	d := NewDomainSet()
	d.Set([]string{"youtube.com"})

	d.Learn("www.youtube.com", []net.IP{net.ParseIP("142.250.74.14")})
	if !d.Match("142.250.74.14") {
		t.Error("expected an IP learned for a listed domain to match")
	}

	d.Learn("example.com", []net.IP{net.ParseIP("93.184.216.34")})
	if d.Match("93.184.216.34") {
		t.Error("expected an IP learned for an unlisted domain to be ignored")
	}
}

// Editing the list must not leave addresses learned under the old one still
// being tunnelled.
func TestDomainSetDropsLearnedOnEdit(t *testing.T) {
	d := NewDomainSet()
	d.Set([]string{"youtube.com"})
	d.Learn("youtube.com", []net.IP{net.ParseIP("142.250.74.14")})
	if !d.Match("142.250.74.14") {
		t.Fatal("setup: expected the address to be learned")
	}

	d.Set([]string{"example.com"}) // user removed youtube.com

	if d.Match("142.250.74.14") {
		t.Error("expected addresses learned under the previous list to be forgotten")
	}
}

func TestDomainSetEmpty(t *testing.T) {
	d := NewDomainSet()
	if !d.Empty() {
		t.Error("expected a fresh set to be empty")
	}
	d.Set([]string{"   ", "# only a comment"})
	if !d.Empty() {
		t.Error("expected blank and comment-only entries to leave the set empty")
	}
	d.Set([]string{"example.com"})
	if d.Empty() {
		t.Error("expected a real entry to make the set non-empty")
	}
}

func TestEngineSmartModeRouting(t *testing.T) {
	e := NewEngine()
	e.SetSites([]string{"youtube.com"})
	e.SetMode(ModeSmart)
	e.Domains().Learn("youtube.com", []net.IP{net.ParseIP("142.250.74.14")})

	if !e.ShouldTunnel("tcp", "142.250.74.14:443") {
		t.Error("expected a listed site to be tunnelled")
	}
	if e.ShouldTunnel("tcp", "93.184.216.34:443") {
		t.Error("expected an unlisted destination to go direct in smart mode")
	}
	// DNS has to stay in the tunnel or the matching above can't work.
	if !e.ShouldTunnel("udp", "1.1.1.1:53") {
		t.Error("expected DNS to be tunnelled in smart mode")
	}
}

func TestEngineAllModeTunnelsEverything(t *testing.T) {
	e := NewEngine()
	e.SetSites([]string{"youtube.com"})
	e.SetMode(ModeAll)

	if !e.ShouldTunnel("tcp", "93.184.216.34:443") {
		t.Error("expected every destination to be tunnelled in all mode")
	}
}

// Smart mode with nothing listed must not silently amount to "VPN off".
func TestEngineSmartModeWithEmptyListTunnels(t *testing.T) {
	e := NewEngine()
	e.SetMode(ModeSmart)

	if !e.ShouldTunnel("tcp", "93.184.216.34:443") {
		t.Error("expected an empty site list to fall back to tunnelling, not to bypass everything")
	}
}

// Split DNS decides per queried name: listed names (and their subdomains)
// through the tunnel, the rest direct - but only in smart mode with a
// non-empty list; otherwise every query rides the tunnel as it always did.
func TestTunnelDNSQueryFollowsTheSiteList(t *testing.T) {
	e := NewEngine()
	e.SetSites([]string{"198.51.100.1"}) // a literal, so nothing resolves in the background
	if !e.TunnelDNSQuery("example.org") {
		t.Fatal("outside smart mode every query must ride the tunnel")
	}

	e.SetMode(ModeSmart)
	e.domains.Set([]string{"youtube.com"})
	if !e.TunnelDNSQuery("youtube.com") || !e.TunnelDNSQuery("www.youtube.com") {
		t.Fatal("a listed name and its subdomains must ride the tunnel")
	}
	if e.TunnelDNSQuery("example.org") {
		t.Fatal("an unlisted name must resolve directly")
	}

	e.domains.Set(nil)
	if !e.TunnelDNSQuery("example.org") {
		t.Fatal("smart mode with an empty list tunnels everything, DNS included")
	}
}
