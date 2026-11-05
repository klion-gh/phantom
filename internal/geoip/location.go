package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Location is a server IP placed on a map: coordinates plus the labels a
// person reads next to the dot. The apps use it through LookupPlace, for the
// city and provider on a tile.
//
// The privacy trade-off described in the package comment applies unchanged,
// and the caller keeps to the same rule: one lookup per IP, cached, never on a
// timer.
type Location struct {
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	City        string  `json:"city,omitempty"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"countryCode,omitempty"`
	Org         string  `json:"org,omitempty"` // hosting provider / ASN, as the provider names it
	Source      string  `json:"source"`
}

type locProvider struct {
	name  string
	url   func(ip string) string
	parse func(body []byte) (Location, error)
}

// locProviders are the services that return coordinates, tried in order.
// db-ip's free tier gives no coordinates, so it is not among them.
var locProviders = []locProvider{
	{
		name: "ipwho.is",
		url:  func(ip string) string { return "https://ipwho.is/" + ip },
		parse: func(body []byte) (Location, error) {
			var v struct {
				Success     *bool   `json:"success"`
				Latitude    float64 `json:"latitude"`
				Longitude   float64 `json:"longitude"`
				City        string  `json:"city"`
				Country     string  `json:"country"`
				CountryCode string  `json:"country_code"`
				Connection  struct {
					ASN int    `json:"asn"`
					Org string `json:"org"`
					ISP string `json:"isp"`
				} `json:"connection"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return Location{}, err
			}
			if v.Success != nil && !*v.Success {
				return Location{}, fmt.Errorf("provider reported failure")
			}
			org := v.Connection.Org
			if org == "" {
				org = v.Connection.ISP
			}
			if v.Connection.ASN != 0 {
				org = strings.TrimSpace(fmt.Sprintf("AS%d %s", v.Connection.ASN, org))
			}
			return Location{Lat: v.Latitude, Lon: v.Longitude, City: v.City, Country: v.Country, CountryCode: v.CountryCode, Org: org}, nil
		},
	},
	{
		name: "ipinfo.io",
		url:  func(ip string) string { return "https://ipinfo.io/" + ip + "/json" },
		parse: func(body []byte) (Location, error) {
			var v struct {
				Loc     string `json:"loc"` // "52.3740,4.8897"
				City    string `json:"city"`
				Country string `json:"country"` // two-letter code only
				Org     string `json:"org"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return Location{}, err
			}
			lat, lon, ok := strings.Cut(v.Loc, ",")
			if !ok {
				return Location{}, fmt.Errorf("no coordinates in response")
			}
			la, err1 := strconv.ParseFloat(lat, 64)
			lo, err2 := strconv.ParseFloat(lon, 64)
			if err1 != nil || err2 != nil {
				return Location{}, fmt.Errorf("bad coordinates %q", v.Loc)
			}
			return Location{Lat: la, Lon: lo, City: v.City, CountryCode: v.Country, Org: v.Org}, nil
		},
	},
}

// LookupLocation places ip on the map, trying each provider until one returns
// usable coordinates.
func LookupLocation(ctx context.Context, ip string) (Location, error) {
	if net.ParseIP(ip) == nil {
		return Location{}, fmt.Errorf("not an IP address: %q", ip)
	}
	var lastErr error
	for _, p := range locProviders {
		body, err := fetch(ctx, p.url(ip))
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", p.name, err)
			continue
		}
		loc, err := p.parse(body)
		if err == nil && !validCoords(loc.Lat, loc.Lon) {
			err = fmt.Errorf("no usable coordinates in response")
		}
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", p.name, err)
			continue
		}
		if !validCode(loc.CountryCode) {
			loc.CountryCode = ""
		}
		loc.CountryCode = strings.ToUpper(loc.CountryCode)
		loc.Source = p.name
		return loc, nil
	}
	return Location{}, lastErr
}

// validCoords rejects out-of-range values and the 0,0 some services return
// for "unknown" - a VPS in the Gulf of Guinea is not a real answer.
func validCoords(lat, lon float64) bool {
	if lat == 0 && lon == 0 {
		return false
	}
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// Place is what a config or relay tile shows about where its server is:
// country, city and the hosting provider.
type Place struct {
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	City        string `json:"city,omitempty"`
	Provider    string `json:"provider,omitempty"`
}

// LookupPlace finds ip's country, city and provider: LookupLocation first,
// which knows all three, and Lookup for the country when it has no answer
// or names only the code.
func LookupPlace(ctx context.Context, ip string) (Place, error) {
	var p Place
	if loc, err := LookupLocation(ctx, ip); err == nil {
		p = Place{Country: loc.Country, CountryCode: loc.CountryCode, City: loc.City, Provider: ProviderName(loc.Org)}
	}
	if p.Country == "" || p.CountryCode == "" {
		res, err := Lookup(ctx, ip)
		if err != nil {
			if p.CountryCode == "" {
				return Place{}, err
			}
		} else {
			p.Country, p.CountryCode = res.Country, res.CountryCode
		}
	}
	return p, nil
}

// ProviderName turns a provider's organisation label ("AS396982 Google LLC")
// into the name a person reads ("Google LLC").
func ProviderName(org string) string {
	org = strings.TrimSpace(org)
	if rest, ok := strings.CutPrefix(org, "AS"); ok {
		if i := strings.IndexByte(rest, ' '); i > 0 {
			if _, err := strconv.Atoi(rest[:i]); err == nil {
				return strings.TrimSpace(rest[i+1:])
			}
		}
	}
	return org
}
