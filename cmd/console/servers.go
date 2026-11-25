package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"phantom/internal/config"
	"phantom/internal/geoip"
	"phantom/internal/handshake"
	"phantom/internal/pingcheck"
)

// slowMs is where a working server turns from "в сети" to "медленно". A
// check times one full disguised handshake (TCP + TLS + the upgrade), which
// is several round trips, so this is well above a plain ICMP ping.
const slowMs = 700

// checkParallel bounds how many handshakes "Проверить все" runs at once.
const checkParallel = 4

// savedConfig mirrors the desktop app's SavedConfig (desktop/configstore.go);
// the console reads that file and never writes it.
type savedConfig struct {
	ID          string `json:"id"`
	Yaml        string `json:"yaml"`
	IP          string `json:"ip,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"countryCode,omitempty"`

	Source string `json:"-"` // "app" or "console": where it was added, set on load
}

// serverView is what the page gets for one saved config. Deliberately
// without the yaml: the PSK and keys never leave this process.
type serverView struct {
	ID          string          `json:"id"`
	Domain      string          `json:"domain"`
	Endpoints   []string        `json:"endpoints"`
	IP          string          `json:"ip,omitempty"`
	Country     string          `json:"country,omitempty"`
	CountryCode string          `json:"countryCode,omitempty"`
	Geo         *geoip.Location `json:"geo,omitempty"`
	GeoOff      bool            `json:"geoOff,omitempty"` // the yaml sets its own country: no third-party lookup
	Check       *Check          `json:"check,omitempty"`
	Invalid     string          `json:"invalid,omitempty"` // the yaml doesn't parse
	Source      string          `json:"source"`            // "app" or "console"; only console ones can be deleted here
}

type app struct {
	configsPath string
	store       *store
	historyPath string
}

// loadConfigs returns the app's saved configs followed by the console's own.
func (a *app) loadConfigs() ([]savedConfig, error) {
	var cs []savedConfig
	data, err := os.ReadFile(a.configsPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &cs); err != nil {
			return nil, err
		}
	}
	for i := range cs {
		cs[i].Source = "app"
	}
	for _, c := range a.store.configs() {
		c.Source = "console"
		cs = append(cs, c)
	}
	return cs, nil
}

// addConfig validates a pasted client.yaml and keeps it in the console.
// A config pointing at a server already listed (same domain and endpoints,
// from the app or the console) is refused rather than shown twice.
func (a *app) addConfig(yaml string) (savedConfig, error) {
	cfg, err := config.ParseClientConfig([]byte(yaml))
	if err != nil {
		return savedConfig{}, fmt.Errorf("конфиг не читается: %w", err)
	}
	existing, err := a.loadConfigs()
	if err != nil {
		return savedConfig{}, err
	}
	key := cfg.Domain + "|" + strings.Join(cfg.ServerList(), ",")
	for _, c := range existing {
		if other, err := config.ParseClientConfig([]byte(c.Yaml)); err == nil &&
			other.Domain+"|"+strings.Join(other.ServerList(), ",") == key {
			return savedConfig{}, errDuplicate
		}
	}
	c := savedConfig{ID: "c" + strconv.FormatInt(time.Now().UnixNano(), 36), Yaml: yaml, Source: "console"}
	return c, a.store.addConfig(c)
}

var errDuplicate = errors.New("этот сервер уже есть в списке")

func (a *app) findConfigs(ids []string) ([]savedConfig, error) {
	all, err := a.loadConfigs()
	if err != nil || len(ids) == 0 {
		return all, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []savedConfig
	for _, c := range all {
		if want[c.ID] {
			out = append(out, c)
		}
	}
	return out, nil
}

// views builds the page's server list from the app's configs plus what the
// console has learned about each.
func (a *app) views() ([]serverView, error) {
	cs, err := a.loadConfigs()
	if err != nil {
		return nil, err
	}
	out := make([]serverView, 0, len(cs))
	for _, c := range cs {
		out = append(out, a.view(c))
	}
	return out, nil
}

func (a *app) view(c savedConfig) serverView {
	v := serverView{ID: c.ID, IP: c.IP, Country: c.Country, CountryCode: c.CountryCode, Source: c.Source}
	cfg, err := config.ParseClientConfig([]byte(c.Yaml))
	if err != nil {
		v.Invalid = err.Error()
		return v
	}
	v.Domain = cfg.Domain
	v.Endpoints = cfg.ServerList()
	if cfg.Country != "" || cfg.CountryCode != "" {
		v.GeoOff = true
		v.Country, v.CountryCode = cfg.Country, cfg.CountryCode
	}
	if chk, ok := a.store.check(c.ID); ok {
		v.Check = &chk
		if chk.IP != "" {
			v.IP = chk.IP
		}
	}
	if v.IP == "" {
		v.IP = literalIP(v.Endpoints)
	}
	if v.IP != "" {
		if loc, ok := a.store.geo(v.IP); ok {
			v.Geo = &loc
		}
	}
	return v
}

// literalIP returns the first endpoint's address when it's written as an IP,
// so such configs land on the map before their first check.
func literalIP(endpoints []string) string {
	if len(endpoints) == 0 {
		return ""
	}
	host, _, err := net.SplitHostPort(endpoints[0])
	if err != nil || net.ParseIP(host) == nil {
		return ""
	}
	return host
}

// locate looks up every server IP that has no location yet. Each IP is
// looked up once and kept; a failed lookup is not cached, so it is retried
// the next time the page asks, never on a timer.
func (a *app) locate(ctx context.Context) {
	views, err := a.views()
	if err != nil {
		return
	}
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for _, v := range views {
		if v.GeoOff || v.IP == "" || v.Geo != nil || seen[v.IP] {
			continue
		}
		seen[v.IP] = true
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			if loc, err := geoip.LookupLocation(ctx, ip); err == nil {
				a.store.setGeo(ip, loc)
			}
		}(v.IP)
	}
	wg.Wait()
}

// check runs one real disguised handshake per config - the same probe the
// apps' tiles use - and records the result.
func (a *app) check(cs []savedConfig) {
	sem := make(chan struct{}, checkParallel)
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Add(1)
		sem <- struct{}{}
		go func(c savedConfig) {
			defer func() { <-sem; wg.Done() }()
			res, err := pingcheck.Ping(c.Yaml)
			chk := Check{At: time.Now().UTC().Format(time.RFC3339)}
			switch {
			case err != nil:
				chk.Status = "fail"
				chk.Kind = classify(err)
				chk.Error = err.Error()
			case res.LatencyMs > slowMs:
				chk.Status, chk.LatencyMs, chk.IP = "slow", res.LatencyMs, res.IP
			default:
				chk.Status, chk.LatencyMs, chk.IP = "ok", res.LatencyMs, res.IP
			}
			a.store.setCheck(c.ID, chk)
		}(c)
	}
	wg.Wait()
}

// classify turns a failed handshake into the few cases that mean different
// things to whoever is reading the map. Kinds, not verdicts: a reset is what
// DPI interference typically looks like, but a single probe can't prove it.
func classify(err error) string {
	if errors.Is(err, handshake.ErrAuthFailed) {
		return "auth"
	}
	msg := strings.ToLower(err.Error())
	var ne net.Error
	switch {
	case strings.Contains(msg, "resolve server address"):
		return "dns"
	case strings.Contains(msg, "auth failed"):
		return "auth"
	case strings.Contains(msg, "refused"):
		return "refused"
	case strings.Contains(msg, "reset"), strings.Contains(msg, "forcibly closed"),
		strings.Contains(msg, "eof"), strings.Contains(msg, "broken pipe"):
		return "reset"
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded),
		strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	}
	return "other"
}
