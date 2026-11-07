package routing

import (
	"context"
	"net"
	"testing"
	"time"
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

// Every catalogue entry must be something the site list actually matches on:
// Telegram's address ranges in particular only work if each one parses as a
// CIDR, since its app never looks a name up for them to be learned from.
func TestCatalogueEntriesAreAllMatchable(t *testing.T) {
	for _, r := range PopularResources() {
		for _, entry := range r.Domains {
			set := NewDomainSet()
			set.Set([]string{entry})
			if set.Empty() {
				t.Errorf("%s: entry %q was dropped as unparseable", r.Name, entry)
			}
		}
	}
	set := NewDomainSet()
	for _, r := range PopularResources() {
		if r.Name == "Telegram" {
			set.Set(r.Domains)
		}
	}
	for _, ip := range []string{"149.154.167.51", "91.108.56.130", "2001:67c:4e8:f004::a"} {
		if !set.Match(ip) {
			t.Errorf("Telegram data-centre address %s is not routed", ip)
		}
	}
}

// Setting the same list again - every (re)connect does - keeps what was
// learned for it; an app holding its addresses in the OS's DNS cache would
// otherwise reconnect straight past the tunnel.
func TestDomainSetKeepsLearnedForTheSameList(t *testing.T) {
	d := NewDomainSet()
	d.Set([]string{"discord.com", "youtube.com"})
	d.Learn("gateway.discord.com", []net.IP{net.ParseIP("162.159.130.235")})
	d.Learn("youtube.com", []net.IP{net.ParseIP("142.250.74.14")})

	d.Set([]string{"discord.com", "youtube.com"}) // reconnect
	if !d.Match("162.159.130.235") || !d.Match("142.250.74.14") {
		t.Fatal("addresses learned for listed names were forgotten on a reconnect")
	}

	d.Set([]string{"discord.com"}) // youtube.com removed
	if !d.Match("162.159.130.235") {
		t.Error("discord.com's address was forgotten though it is still listed")
	}
	if d.Match("142.250.74.14") {
		t.Error("youtube.com's address kept matching after it was removed")
	}
}

// Up-front lookups go through the resolver the platform hands in (the
// tunnel) and nowhere else: without one, nothing is looked up.
func TestSeedsUseOnlyTheGivenResolver(t *testing.T) {
	e := NewEngine()
	<-e.SetSites([]string{"discord.com"})
	if e.Domains().LearnedCount() != 0 {
		t.Fatal("something was learned with no resolver - the device's own DNS was asked")
	}
	asked := make(chan string, 4)
	e.SetResolver(func(_ context.Context, host string) ([]net.IP, error) {
		asked <- host
		return []net.IP{net.ParseIP("162.159.137.232")}, nil
	})
	select {
	case h := <-asked:
		if h != "discord.com" {
			t.Fatalf("looked up %q", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("setting a resolver didn't look the list up")
	}
	time.Sleep(50 * time.Millisecond)
	if !e.Domains().Match("162.159.137.232") {
		t.Fatal("the answer through the resolver wasn't learned")
	}
}

// A list that already carries Discord gets the domains and ranges Discord
// gained since; a list without it is left alone.
func TestUpgradeSitesAddsWhatAServiceGained(t *testing.T) {
	old := []string{"youtube.com", "discord.com", "discordapp.com", "discord.gg", "discordapp.net", "example.org"}
	got, changed := UpgradeSites(old)
	if !changed {
		t.Fatal("nothing was added to an old Discord list")
	}
	want := map[string]bool{"discord.media": false, "66.22.192.0/18": false, "162.159.128.0/20": false}
	for _, s := range got {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for s, found := range want {
		if !found {
			t.Errorf("%s missing after the upgrade: %v", s, got)
		}
	}
	if again, changed := UpgradeSites(got); changed || len(again) != len(got) {
		t.Error("an up-to-date list changed again")
	}
	if _, changed := UpgradeSites([]string{"example.org"}); changed {
		t.Error("a list without any catalogue service was changed")
	}
	d := NewDomainSet()
	d.Set(got)
	if !d.Match("66.22.200.15") || !d.Match("162.159.130.235") {
		t.Error("Discord's address ranges don't match")
	}
}
