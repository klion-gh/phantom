//go:build !windows

package mobile

import (
	"context"
	"errors"
	"sync"
	"time"

	"phantom/internal/linkguard"
	"phantom/internal/pingcheck"
)

// KeepAliveListener is implemented by the app. OnConnect asks it to connect
// configID through its usual path (the VPN service's connect) - a reconnect
// of the current config as much as a switch to another one. It must not
// block; the outcome comes back through KeepAlive.SetTunnel or
// KeepAlive.ConnectFailed.
type KeepAliveListener interface {
	OnConnect(configID string)
}

// How long a connect asked of the app may take before it counts as failed.
const keepAliveConnectWait = 40 * time.Second

// KeepAlive is "Поддерживать соединение" for the mobile app: the shared
// internal/linkguard logic, wired to the app's tunnel and its connect path.
// The app tells it what's connected (SetWanted, SetTunnel, ConnectFailed)
// and which configs it may move to (AddCandidate + Apply); it checks the
// tunnel on its own and asks the app to connect when the tunnel stops
// carrying internet. Every check of a config goes through the relay while
// one is set (see SetRelay), the same way a connect would.
//
// The API is flat - strings, ints, errors - to stay gomobile-safe.
type KeepAlive struct {
	listener KeepAliveListener
	guard    *linkguard.Guard

	mu         sync.Mutex
	wanted     string
	tunnelID   string
	tunnel     *Tunnel
	connecting time.Time // when a connect started; zero when none is in flight
	gen        int       // bumped on every tunnel that comes up
	failGen    int       // bumped on every connect that fails
	cands      []linkguard.Candidate
	pending    []linkguard.Candidate
	changed    chan struct{}
}

// NewKeepAlive builds the guard. It does nothing until Start.
func NewKeepAlive(listener KeepAliveListener) *KeepAlive {
	k := &KeepAlive{listener: listener, changed: make(chan struct{})}
	k.guard = linkguard.New(linkguard.Hooks{
		Active: k.active,
		Busy:   k.busy,
		Tunnel: func(ctx context.Context) error {
			k.mu.Lock()
			t := k.tunnel
			k.mu.Unlock()
			if t == nil {
				return errors.New("no tunnel")
			}
			deadline, _ := ctx.Deadline()
			return t.CheckInternet(int(time.Until(deadline).Milliseconds()))
		},
		Direct: func(ctx context.Context) error {
			_, err := linkguard.Bound(ctx, func() (int64, error) { return 0, pingcheck.Direct(pingOptions()) })
			return err
		},
		Config: func(ctx context.Context, yaml string) (int64, error) {
			return linkguard.Bound(ctx, func() (int64, error) {
				r, err := pingcheck.Internet(withRelay(yaml), pingOptions())
				return r.LatencyMs, err
			})
		},
		Candidates: func() []linkguard.Candidate {
			k.mu.Lock()
			defer k.mu.Unlock()
			return append([]linkguard.Candidate(nil), k.cands...)
		},
		Connect:  k.connect,
		Switched: adoptInSelectors,
	})
	return k
}

// Start begins checking.
func (k *KeepAlive) Start() { k.guard.Start() }

// Stop ends checking. Idempotent; a stopped KeepAlive isn't restarted - make
// a new one.
func (k *KeepAlive) Stop() { k.guard.Stop() }

// Kick checks now - worth calling right after the device's network changed.
func (k *KeepAlive) Kick() { k.guard.Kick() }

// StatusJSON is the guard's state for the UI - see linkguard.Status.
func (k *KeepAlive) StatusJSON() string { return k.guard.Status().JSON() }

// SetWanted records the config the user wants connected - called when any
// connect starts - or "" when the user disconnects.
func (k *KeepAlive) SetWanted(configID string) {
	k.mu.Lock()
	k.wanted = configID
	if configID == "" {
		k.tunnel, k.tunnelID = nil, ""
		k.connecting = time.Time{}
	} else {
		k.connecting = time.Now()
	}
	k.signalLocked()
	k.mu.Unlock()
}

// SetTunnel records the tunnel that just came up for configID, or that the
// tunnel went away (t nil) without the user disconnecting.
func (k *KeepAlive) SetTunnel(configID string, t *Tunnel) {
	k.mu.Lock()
	k.tunnel, k.tunnelID = t, configID
	if t != nil {
		k.wanted = configID
		k.connecting = time.Time{}
		k.gen++
	}
	k.signalLocked()
	k.mu.Unlock()
}

// ConnectFailed records that connecting configID failed. The config stays
// wanted: bringing it back is the guard's job.
func (k *KeepAlive) ConnectFailed(configID string) {
	k.mu.Lock()
	k.tunnel, k.tunnelID = nil, ""
	k.connecting = time.Time{}
	k.failGen++
	k.signalLocked()
	k.mu.Unlock()
	k.guard.Kick()
}

// AddCandidate stages a config the guard may move the tunnel to; Apply
// replaces the set with everything staged since the last Apply.
func (k *KeepAlive) AddCandidate(id string, configYAML string) {
	k.mu.Lock()
	k.pending = append(k.pending, linkguard.Candidate{ID: id, YAML: configYAML})
	k.mu.Unlock()
}

// Apply - see AddCandidate.
func (k *KeepAlive) Apply() {
	k.mu.Lock()
	k.cands, k.pending = k.pending, nil
	k.mu.Unlock()
}

func (k *KeepAlive) signalLocked() {
	close(k.changed)
	k.changed = make(chan struct{})
}

func (k *KeepAlive) active() (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.wanted, k.tunnel != nil && k.tunnelID == k.wanted
}

func (k *KeepAlive) busy() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return !k.connecting.IsZero() && time.Since(k.connecting) < keepAliveConnectWait
}

// connect asks the app to connect id and waits for it to come up or fail.
func (k *KeepAlive) connect(id string) error {
	if k.listener == nil {
		return errors.New("no listener")
	}
	k.mu.Lock()
	gen, failGen := k.gen, k.failGen
	k.mu.Unlock()

	k.listener.OnConnect(id)

	deadline := time.After(keepAliveConnectWait)
	for {
		k.mu.Lock()
		switch {
		case k.gen != gen:
			ok := k.tunnelID == id && k.tunnel != nil
			k.mu.Unlock()
			if ok {
				return nil
			}
			return errors.New("another config came up")
		case k.failGen != failGen:
			k.mu.Unlock()
			return errors.New("connect failed")
		case k.wanted == "":
			k.mu.Unlock()
			return errors.New("disconnected")
		}
		changed := k.changed
		k.mu.Unlock()
		select {
		case <-changed:
		case <-deadline:
			return errors.New("connect timed out")
		}
	}
}
