//go:build !windows

package mobile

import (
	"encoding/json"
	"time"

	"phantom/internal/pingcheck"
	"phantom/internal/routing"
)

// How long a single candidate's probe is allowed to run before it's counted
// as unreachable this round - see routing.WithProbeTimeout.
const probeTimeout = 6 * time.Second

// PopularResourcesJSON returns the built-in catalogue of commonly-blocked
// services for the picker, as
// [{"name":..,"icon":..,"domains":[..]}, ...]. Shared with the Windows client
// (see internal/routing) so both show the same list.
func PopularResourcesJSON() string { return routing.PopularResourcesJSON() }

// SwitchListener is implemented on the Kotlin/Swift side. OnSwitch is called
// whenever the selector decides the tunnel should move to a different config -
// including the first pick - and the app is expected to (re)connect to that id.
//
// Called from a background goroutine, so implementations must marshal to the
// UI thread themselves.
type SwitchListener interface {
	OnSwitch(configID string)
}

// AutoSelector keeps a set of the user's configs under observation and tells
// the app which one to be connected to, so a server that stops working is
// replaced without the user noticing it happened.
//
// This is the shared machinery behind both "Автоматически" (whole-device
// traffic) and the smart-VPN mode's own config choice: the difference between
// them is only *what* gets tunnelled, not *how* the server is chosen.
//
// The switching policy - when moving is worth killing live connections for -
// lives in internal/routing.Selector and is deliberately conservative; see the
// tuning constants there.
//
// The API is deliberately flat (no slices of structs) to stay gomobile-safe.
type AutoSelector struct {
	inner *routing.Selector

	// Candidates are staged here and pushed in one go by Apply, since
	// gomobile can't pass a list of (id, yaml) pairs across the boundary.
	pending []routing.Candidate
}

// NewAutoSelector creates a selector that reports its decisions to listener.
func NewAutoSelector(listener SwitchListener) *AutoSelector {
	a := &AutoSelector{}
	a.inner = routing.NewSelector(
		routing.WithProbeTimeout(func(configYAML string) (int64, error) {
			// A full Phantom handshake, not a TCP connect: a blocked server
			// whose port still accepts connections would otherwise look
			// perfectly healthy and keep being selected.
			result, err := pingcheck.PingWith(configYAML, pingOptions())
			if err != nil {
				return 0, err
			}
			return result.LatencyMs, nil
		}, probeTimeout),
		func(c routing.Candidate) {
			if listener != nil {
				listener.OnSwitch(c.ID)
			}
		},
	)
	return a
}

// AddCandidate stages one config. Call Apply when the whole set is staged.
func (a *AutoSelector) AddCandidate(id string, configYAML string) {
	a.pending = append(a.pending, routing.Candidate{ID: id, YAML: configYAML})
}

// Apply replaces the observed set with everything staged since the last Apply,
// preserving what's already known about configs that are still in it.
func (a *AutoSelector) Apply() {
	a.inner.SetCandidates(a.pending)
	a.pending = nil
}

// Start begins background probing.
func (a *AutoSelector) Start() { a.inner.Start() }

// Stop ends background probing.
func (a *AutoSelector) Stop() { a.inner.Stop() }

// Probe forces an immediate round. Worth calling right after the device's
// network changes, where waiting for the next tick would leave the user on a
// config that just became unreachable.
func (a *AutoSelector) Probe() { a.inner.Probe() }

// Current returns the currently selected config id, or "" before the first
// probe has completed.
func (a *AutoSelector) Current() string { return a.inner.Current() }

// HealthJSON returns per-config health for the UI as
// [{"id":..,"alive":..,"latency_ms":..,"probed":..}, ...] - a plain string
// keeps this gomobile-safe, same pattern as Stats/Ping.
func (a *AutoSelector) HealthJSON() string {
	data, err := json.Marshal(a.inner.Snapshot())
	if err != nil {
		return "[]"
	}
	return string(data)
}
