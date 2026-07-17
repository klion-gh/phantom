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
