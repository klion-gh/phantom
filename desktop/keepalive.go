package main

import (
	"context"
	"errors"
	"log"
	"sync"

	"phantom/internal/linkguard"
	"phantom/internal/routing"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// "Поддерживать соединение" - the switch under "Использовать ретранслятор".
// While it's on, internal/linkguard checks that the connected config still
// carries internet and, when it stops, reconnects it or moves to a config
// that does. Works the same for a config connected by hand, Умный VPN and
// "Подключиться к лучшей"; every check goes through the relay when one is in
// use. The decisions live in internal/linkguard, shared with Android; this is
// the glue to the App.

const keepAliveFileName = "keep_alive"

func loadKeepAlive() bool { return loadSetting(keepAliveFileName, "0", "1", "0") == "1" }

func saveKeepAlive(on bool) {
	saveSetting(keepAliveFileName, boolToSetting(on), "0", "1", "0")
}

var (
	guardMu sync.Mutex
	guard   *linkguard.Guard
)

// syncKeepAlive starts or stops the guard to match the setting.
func (a *App) syncKeepAlive() {
	guardMu.Lock()
	defer guardMu.Unlock()
	on := loadKeepAlive()
	switch {
	case on && guard == nil:
		guard = linkguard.New(a.guardHooks())
		guard.Start()
		log.Println("keep-alive: on")
	case !on && guard != nil:
		guard.Stop()
		guard = nil
		log.Println("keep-alive: off")
	}
}

// kickKeepAlive runs a round now, if the guard is on.
func kickKeepAlive() {
	guardMu.Lock()
	g := guard
	guardMu.Unlock()
	if g != nil {
		g.Kick()
	}
}

// keepAliveStatusJSON is the guard's state for the UI, {"state":"off"} while
// the setting is off.
func keepAliveStatusJSON() string {
	guardMu.Lock()
	g := guard
	guardMu.Unlock()
	if g == nil {
		return linkguard.Status{State: linkguard.StateOff}.JSON()
	}
	return g.Status().JSON()
}

func (a *App) guardHooks() linkguard.Hooks {
	return linkguard.Hooks{
		Active: func() (string, bool) {
			a.mu.Lock()
			defer a.mu.Unlock()
			return a.wantedID, a.tunnel != nil && a.activeConfigID == a.wantedID
		},
		Busy: func() bool { return a.connecting.Load() > 0 },
		Tunnel: func(ctx context.Context) error {
			a.mu.Lock()
			t := a.tunnel
			a.mu.Unlock()
			if t == nil {
				return errors.New("no tunnel")
			}
			return t.CheckInternet(ctx)
		},
		Direct: func(ctx context.Context) error {
			_, err := linkguard.Bound(ctx, func() (int64, error) { return 0, checkDirect() })
			return err
		},
		Config: func(ctx context.Context, yaml string) (int64, error) {
			return linkguard.Bound(ctx, func() (int64, error) { return checkConfigInternet(yaml) })
		},
		Candidates: keepAliveCandidates,
		Connect: func(id string) error {
			configs, err := loadConfigs()
			if err != nil {
				return err
			}
			for _, c := range configs {
				if c.ID == id {
					if msg := a.Connect(c.ID, c.Yaml); msg != "" {
						return errors.New(msg)
					}
					// The UI moves its "connected" mark without waiting for
					// its next poll - same as a smart-selector switch.
					runtime.EventsEmit(a.ctx, "routing:switched", id)
					return nil
				}
			}
			return errors.New("config no longer exists")
		},
		Switched: func(from, to string) {
			log.Printf("keep-alive: moved the tunnel from %s to %s", from, to)
			selectorMu.Lock()
			s := selector
			selectorMu.Unlock()
			if s != nil {
				s.Avoid(from, linkguard.AvoidFor)
				s.Adopt(to)
			}
		},
	}
}

// keepAliveCandidates: while Умный VPN or "Подключиться к лучшей" picks the
// config, the guard stays inside that mode's own set; otherwise any saved
// config will do - the user connected one by hand and wants internet.
func keepAliveCandidates() []linkguard.Candidate {
	var src []routing.Candidate
	if loadSmartEnabled() || loadAutoEnabled() {
		src = selectorCandidates()
	}
	if len(src) == 0 {
		configs, err := loadConfigs()
		if err != nil {
			return nil
		}
		for _, c := range configs {
			src = append(src, routing.Candidate{ID: c.ID, YAML: c.Yaml})
		}
	}
	out := make([]linkguard.Candidate, len(src))
	for i, c := range src {
		out[i] = linkguard.Candidate{ID: c.ID, YAML: c.YAML}
	}
	return out
}

// GetKeepAlive returns whether "Поддерживать соединение" is on.
func (a *App) GetKeepAlive() bool { return loadKeepAlive() }

// SetKeepAlive turns "Поддерживать соединение" on or off.
func (a *App) SetKeepAlive(on bool) {
	saveKeepAlive(on)
	diag(diagCatVPN, "setKeepAlive", "enabled", on)
	a.syncKeepAlive()
}
