package geoip

import "testing"

func TestProviderName(t *testing.T) {
	for in, want := range map[string]string{
		"AS396982 Google LLC":                "Google LLC",
		"  AS206134 NEKOBYTE INTERNATIONAL ": "NEKOBYTE INTERNATIONAL",
		"Hetzner Online GmbH":                "Hetzner Online GmbH",
		"ASUS Cloud":                         "ASUS Cloud",
		"":                                   "",
	} {
		if got := ProviderName(in); got != want {
			t.Errorf("ProviderName(%q) = %q, want %q", in, got, want)
		}
	}
}
