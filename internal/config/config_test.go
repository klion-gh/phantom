package config

import (
	"reflect"
	"testing"
)

func TestServerList(t *testing.T) {
	tests := []struct {
		name string
		cfg  ClientConfig
		want []string
	}{
		{
			name: "servers takes precedence over server",
			cfg:  ClientConfig{Server: "primary:443", Servers: []string{"a:443", "b:8443"}},
			want: []string{"a:443", "b:8443"},
		},
		{
			name: "falls back to single server",
			cfg:  ClientConfig{Server: "primary:443"},
			want: []string{"primary:443"},
		},
		{
			name: "empty when neither set",
			cfg:  ClientConfig{},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ServerList(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ServerList() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClientValidateRequiresAnEndpoint(t *testing.T) {
	base := ClientConfig{Domain: "d.example", PSK: "x", ServerPublicKey: "y"}

	// No server and no servers -> rejected.
	if err := base.Validate(); err == nil {
		t.Error("Validate() with no endpoint should fail")
	}

	// Either one alone -> accepted (past the endpoint check).
	withServer := base
	withServer.Server = "host:443"
	if err := withServer.Validate(); err != nil {
		t.Errorf("Validate() with server should pass the endpoint check, got %v", err)
	}

	withServers := base
	withServers.Servers = []string{"host:443"}
	if err := withServers.Validate(); err != nil {
		t.Errorf("Validate() with servers should pass the endpoint check, got %v", err)
	}
}

func TestRelayReplacesEndpoints(t *testing.T) {
	cfg := ClientConfig{Server: "primary:8443", Servers: []string{"a:8443"}, Relay: "relay:443"}
	if got := cfg.ServerList(); !reflect.DeepEqual(got, []string{"relay:443"}) {
		t.Fatalf("ServerList with a relay = %v, want only the relay", got)
	}
	if got := cfg.DirectServerList(); !reflect.DeepEqual(got, []string{"a:8443"}) {
		t.Fatalf("DirectServerList = %v, want the server's own endpoints", got)
	}
}

const relayTestYAML = `# my server
server: "vpn.example:8443"
domain: "vpn.example"
psk: "aa"
server_public_key: "bb"
`

func TestWithRelaySetsReplacesAndRemoves(t *testing.T) {
	withRelay, err := WithRelay(relayTestYAML, "203.0.113.7:443", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseClientConfig([]byte(withRelay))
	if err != nil {
		t.Fatalf("config with relay does not parse: %v\n%s", err, withRelay)
	}
	if cfg.Relay != "203.0.113.7:443" || cfg.Server != "vpn.example:8443" || cfg.Domain != "vpn.example" {
		t.Fatalf("got relay=%q server=%q domain=%q", cfg.Relay, cfg.Server, cfg.Domain)
	}

	// Setting it again replaces rather than duplicates (a duplicate key would not parse).
	again, err := WithRelay(withRelay, "198.51.100.1:8443", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err = ParseClientConfig([]byte(again)); err != nil || cfg.Relay != "198.51.100.1:8443" {
		t.Fatalf("replace: relay=%q err=%v", cfg.Relay, err)
	}

	removed, err := WithRelay(again, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err = ParseClientConfig([]byte(removed)); err != nil || cfg.Relay != "" {
		t.Fatalf("remove: relay=%q err=%v", cfg.Relay, err)
	}
}

func TestRelayMustBeHostPort(t *testing.T) {
	bad, err := WithRelay(relayTestYAML, "203.0.113.7", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseClientConfig([]byte(bad)); err == nil {
		t.Fatal("a relay without a port was accepted")
	}
}

func TestNormalizeRelayAddr(t *testing.T) {
	for in, want := range map[string]string{
		"89.125.34.9":        "89.125.34.9:443",
		" 89.125.34.9:8443 ": "89.125.34.9:8443",
		"relay.example":      "relay.example:443",
		"2a12:b3c0::1":       "[2a12:b3c0::1]:443",
		"[2a12:b3c0::1]:80":  "[2a12:b3c0::1]:80",
	} {
		if got, err := NormalizeRelayAddr(in); err != nil || got != want {
			t.Errorf("NormalizeRelayAddr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "1.2.3.4:0", "1.2.3.4:99999", "host:abc", "http://x/y", ":443"} {
		if got, err := NormalizeRelayAddr(bad); err == nil {
			t.Errorf("NormalizeRelayAddr(%q) = %q, want an error", bad, got)
		}
	}
}

func TestRelayPrefix(t *testing.T) {
	y, err := WithRelay(relayTestYAML, "203.0.113.7:443", "msk")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseClientConfig([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.RelayDomain(); got != "msk.vpn.example" {
		t.Fatalf("RelayDomain = %q", got)
	}
	// Off again: both keys go.
	y, _ = WithRelay(y, "", "")
	if cfg, _ = ParseClientConfig([]byte(y)); cfg.Relay != "" || cfg.RelayPrefix != "" || cfg.RelayDomain() != "" {
		t.Fatalf("relay left behind: %+v", cfg)
	}
	for in, want := range map[string]string{"msk": "msk", " .MSK. ": "msk", "": "", "ru-1": "ru-1"} {
		if got, err := NormalizeRelayPrefix(in); err != nil || got != want {
			t.Errorf("NormalizeRelayPrefix(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"a.b", "-x", "x-", "мск", "a b"} {
		if _, err := NormalizeRelayPrefix(bad); err == nil {
			t.Errorf("NormalizeRelayPrefix(%q) accepted", bad)
		}
	}
}
