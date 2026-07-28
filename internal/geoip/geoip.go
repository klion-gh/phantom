// Package geoip resolves the country a server IP sits in, for the location label
// on a saved-config tile. Shared by both GUI clients so the provider list, the
// parsing and the timeouts exist once rather than twice (Kotlin and JavaScript
// each had their own before).
//
// # What this costs
//
// This is a deliberate, operator-visible privacy trade-off and it should not be
// buried. Asking a third party "which country is 203.0.113.10 in?" tells that
// third party the address of the user's VPN server, and links it to whoever
// asked. An earlier version of this app did exactly that on a repeating timer,
// which is why it was removed; it is back by explicit choice, under two
// constraints that address the part that actually mattered:
//
//   - once per saved config, never on a timer. The result is pinned in the
//     config store and the lookup is not repeated - a server's country does not
//     change, so there is nothing to refresh;
//   - the operator can still set country/country_code in the config, and those
//     win. A deployment that wants no third-party contact at all simply fills
//     them in, and Lookup is never called.
//
// The lookup deliberately does NOT go through the tunnel: it runs before there
// is one, when a config is first saved.
package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Result is what a tile needs: a human-readable country name and its two-letter
// ISO code. Name may be empty if the provider that answered only returns a code,
// in which case callers fall back to showing the code.
type Result struct {
	Country     string
	CountryCode string
}

// perProviderTimeout bounds one provider attempt. Short on purpose: the point of
// the list below is to move on to the next provider quickly when one is
// unreachable, and from Russia any given geo service may well be.
const perProviderTimeout = 8 * time.Second

// provider is one geolocation service: a URL template and a parser for whatever
// shape it answers with.
type provider struct {
	name  string
	url   func(ip string) string
	parse func(body []byte) (Result, error)
}

// providers are tried in order, first usable answer wins.
//
// Ordered by how much they give us, not by preference: the first two return a
// country name as well as a code, the last returns only a code and exists
// because a bare two-letter response from a large, widely-reachable host is the
// most likely thing to still work when the others are blocked or rate-limited.
//
// All three are HTTPS with no API key. Rate limits are irrelevant here - a user
// has a handful of configs and each is resolved exactly once, ever.
var providers = []provider{
	{
		name: "db-ip.com",
		url:  func(ip string) string { return "https://api.db-ip.com/v2/free/" + ip },
		parse: func(body []byte) (Result, error) {
			var v struct {
				CountryCode string `json:"countryCode"`
				CountryName string `json:"countryName"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return Result{}, err
			}
			return Result{Country: v.CountryName, CountryCode: v.CountryCode}, nil
		},
	},
	{
		name: "ipwho.is",
		url:  func(ip string) string { return "https://ipwho.is/" + ip },
		parse: func(body []byte) (Result, error) {
			var v struct {
				Success     *bool  `json:"success"`
				Country     string `json:"country"`
				CountryCode string `json:"country_code"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return Result{}, err
			}
			// This one reports failure in the body with HTTP 200.
			if v.Success != nil && !*v.Success {
				return Result{}, fmt.Errorf("provider reported failure")
			}
			return Result{Country: v.Country, CountryCode: v.CountryCode}, nil
		},
	},
	{
		name: "ipinfo.io",
		url:  func(ip string) string { return "https://ipinfo.io/" + ip + "/country" },
		parse: func(body []byte) (Result, error) {
			code := strings.TrimSpace(string(body))
			return Result{CountryCode: code}, nil
		},
	},
}

// Lookup resolves ip's country, trying each provider until one answers usefully.
// Returns an error if every provider failed, so the caller can retry later rather
// than pinning an empty label.
func Lookup(ctx context.Context, ip string) (Result, error) {
	if net.ParseIP(ip) == nil {
		return Result{}, fmt.Errorf("not an IP address: %q", ip)
	}

	var lastErr error
	for _, p := range providers {
		res, err := query(ctx, p, ip)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", p.name, err)
			continue
		}
		return res, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no providers configured")
	}
	return Result{}, lastErr
}

func query(ctx context.Context, p provider, ip string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, perProviderTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url(ip), nil)
	if err != nil {
		return Result{}, err
	}
	// Plain and unremarkable: no custom User-Agent identifying this as a VPN
	// client, which would tell the provider more than it already learns.
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("status %d", resp.StatusCode)
	}

	// Capped: this is untrusted input parsed before anything validates it.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Result{}, err
	}

	res, err := p.parse(body)
	if err != nil {
		return Result{}, err
	}
	if !validCode(res.CountryCode) {
		return Result{}, fmt.Errorf("no usable country code in response")
	}
	res.CountryCode = strings.ToUpper(res.CountryCode)
	return res, nil
}

// validCode rejects anything that isn't a two-letter ISO code. Worth checking:
// the code is fed to the flag renderer, which turns each letter into a
// regional-indicator character, so junk in produces junk glyphs rather than a
// visible error.
func validCode(code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 2 {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}
