package routing

import "testing"

// A listed app's flows go through the tunnel even to unlisted sites, and an
// app list on its own (no sites) no longer means "tunnel everything".
func TestEngineTunnelsListedAppsOnly(t *testing.T) {
	e := NewEngine()
	e.SetMode(ModeSmart)
	e.SetApps(1, func(network, source, target string) bool {
		return source == "10.10.0.2:40000"
	})

	if e.ShouldTunnel("tcp", "198.51.100.7:443") {
		t.Fatal("with only apps listed, an unlisted site must go direct")
	}
	if !e.TunnelApp("tcp", "10.10.0.2:40000", "198.51.100.7:443") {
		t.Fatal("the listed app's flow must be tunnelled")
	}
	if e.TunnelApp("tcp", "10.10.0.2:40001", "198.51.100.7:443") {
		t.Fatal("another app's flow must stay direct")
	}

	e.SetMode(ModeAll)
	if e.TunnelApp("tcp", "10.10.0.2:40000", "198.51.100.7:443") {
		t.Fatal("outside smart mode the app check has nothing to decide")
	}

	e.SetMode(ModeSmart)
	e.SetApps(0, nil)
	if !e.ShouldTunnel("tcp", "198.51.100.7:443") {
		t.Fatal("nothing listed at all must still tunnel everything")
	}
}

func TestAdBlockOnlyWhenOn(t *testing.T) {
	e := NewEngine()
	if e.BlockDNSQuery("googleads.g.doubleclick.net") {
		t.Fatal("off by default")
	}
	e.SetAdBlock(true)
	for _, name := range []string{"googleads.g.doubleclick.net", "pagead2.googlesyndication.com.", "imasdk.googleapis.com"} {
		if !e.BlockDNSQuery(name) {
			t.Fatalf("%s should be blocked", name)
		}
	}
	for _, name := range []string{"www.youtube.com", "rr1---sn-abc.googlevideo.com", "googleapis.com", "i.ytimg.com"} {
		if e.BlockDNSQuery(name) {
			t.Fatalf("%s must not be blocked", name)
		}
	}
	if !e.BlockFlow("tcp", "1.1.1.1:443") || e.BlockFlow("tcp", "1.1.1.1:53") || e.BlockFlow("tcp", "142.250.1.1:443") {
		t.Fatal("DoH refusal is for the public resolvers' 443/853 only")
	}
}
