package geoip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// All of these drive the parsers directly or through a local test server: the
// point is to pin the parsing and the failover behaviour, not to depend on a
// third-party service being up while tests run.

func TestParsersHandleEachProvidersShape(t *testing.T) {
	for _, tc := range []struct {
		provider string
		body     string
		want     Result
	}{
		{
			"db-ip.com",
			`{"ipAddress":"1.1.1.1","countryCode":"AU","countryName":"Australia","city":"Sydney"}`,
			Result{Country: "Australia", CountryCode: "AU"},
		},
		{
			"ipwho.is",
			`{"ip":"1.1.1.1","success":true,"country":"Australia","country_code":"AU"}`,
			Result{Country: "Australia", CountryCode: "AU"},
		},
		{
			"ipinfo.io",
			"AU\n",
			Result{CountryCode: "AU"},
		},
	} {
		p := providerByName(t, tc.provider)
		got, err := p.parse([]byte(tc.body))
		if err != nil {
			t.Errorf("%s: %v", tc.provider, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.provider, got, tc.want)
		}
	}
}

// ipwho.is reports failure in the body while still answering HTTP 200, so a
// parser that only looked at the status would pin a blank label.
func TestIPWhoIsBodyLevelFailureIsAnError(t *testing.T) {
	p := providerByName(t, "ipwho.is")
	if _, err := p.parse([]byte(`{"ip":"10.0.0.1","success":false,"message":"reserved range"}`)); err == nil {
		t.Error("a body-level failure was accepted")
	}
}

func TestLookupRejectsNonIP(t *testing.T) {
	if _, err := Lookup(context.Background(), "example.com"); err == nil {
		t.Error("a hostname was accepted where an IP is required")
	}
}

// A provider that answers 200 with something unusable must not end the search -
// it has to count as a failure so the next provider gets a turn.
func TestUnusableAnswerFailsOver(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"countryCode":""}`))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"countryCode":"NL","countryName":"Netherlands"}`))
	}))
	defer good.Close()

	restore := providers
	providers = []provider{
		{name: "bad", url: func(string) string { return bad.URL }, parse: restore[0].parse},
		{name: "good", url: func(string) string { return good.URL }, parse: restore[0].parse},
	}
	defer func() { providers = restore }()

	got, err := Lookup(context.Background(), "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if got.CountryCode != "NL" || got.Country != "Netherlands" {
		t.Fatalf("got %+v, want NL/Netherlands", got)
	}
}

// Every provider failing must surface as an error, so the caller retries later
// instead of pinning an empty label forever.
func TestAllProvidersFailingIsAnError(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer down.Close()

	restore := providers
	providers = []provider{
		{name: "down", url: func(string) string { return down.URL }, parse: restore[0].parse},
	}
	defer func() { providers = restore }()

	if _, err := Lookup(context.Background(), "203.0.113.10"); err == nil {
		t.Error("all providers failed but Lookup reported success")
	}
}

// The code goes straight to the flag renderer, which maps each letter to a
// regional-indicator character - junk in means junk glyphs rather than a visible
// error, so it is checked before being pinned.
func TestValidCode(t *testing.T) {
	for _, ok := range []string{"NL", "nl", "De", "RU"} {
		if !validCode(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "N", "NLD", "N1", "  ", "🇳🇱", "N-"} {
		if validCode(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLookupUppercasesTheCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"countryCode":"nl","countryName":"Netherlands"}`))
	}))
	defer srv.Close()

	restore := providers
	providers = []provider{{name: "t", url: func(string) string { return srv.URL }, parse: restore[0].parse}}
	defer func() { providers = restore }()

	got, err := Lookup(context.Background(), "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if got.CountryCode != "NL" {
		t.Fatalf("code came back as %q, want NL - the flag renderer needs it uppercase", got.CountryCode)
	}
}

func providerByName(t *testing.T, name string) provider {
	t.Helper()
	for _, p := range providers {
		if p.name == name {
			return p
		}
	}
	t.Fatalf("no provider named %q - the list changed without the tests", name)
	return provider{}
}

// A cheap guard that the URLs are at least HTTPS: a plaintext geo query would
// hand the server's address to anyone on the path, not just the provider.
func TestAllProvidersUseHTTPS(t *testing.T) {
	for _, p := range providers {
		if u := p.url("1.1.1.1"); !strings.HasPrefix(u, "https://") {
			t.Errorf("%s builds a non-HTTPS URL: %s", p.name, u)
		}
	}
}
