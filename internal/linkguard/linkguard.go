// Package linkguard is "Поддерживать соединение": it watches whether the
// connected configuration still carries internet and, when it stops, works
// out why and moves the tunnel to one that does.
//
// The failure it exists for: after a drop in the network (a lift, a tunnel
// under a river, Wi-Fi handing over to mobile data) the tunnel can come back
// looking connected while nothing at all gets through it. The pool's own
// redial and the "alive" flag only see the connection to the server, not
// whether that server still reaches the internet.
//
// Each round answers three questions, in this order and only as far as
// needed:
//
//  1. Is there internet through the current configuration? (a page fetched
//     through the live tunnel - see internal/netcheck)
//  2. If not: is there internet without the VPN at all? If not, the network
//     itself is down, and switching servers can't help - wait for it.
//  3. If the network is fine: reconnect the current configuration once (a
//     blip often leaves a dead session behind), and if that doesn't bring the
//     internet back, check which configurations carry internet right now and
//     connect to the fastest of them.
//
// Every check of a configuration goes the way its tunnel would - through the
// relay when one is in use - because the platform layer hands this package
// configs with the relay already applied.
//
// The package owns no tunnel: the platform (desktop/, mobile/) supplies the
// checks and does the connecting through Hooks, the same split as
// internal/routing.Selector.
package linkguard

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"phantom/internal/diag"
)

// Timing. Biased against acting on a single miss: reconnecting drops every
// connection on the device, so one failed check is confirmed before anything
// moves.
const (
	// How often a healthy tunnel is checked. One small HTTP request through
	// the tunnel per round - cheap, but not free on a metered connection.
	healthyInterval = 20 * time.Second

	// After a failed check, how soon it is repeated to confirm.
	confirmInterval = 4 * time.Second

	// Consecutive failed checks before the tunnel counts as carrying nothing.
	failsToAct = 2

	// How often the network is re-checked while there's no internet even
	// without the VPN.
	noNetworkInterval = 5 * time.Second

	// While no configuration carries internet: first retry after this, then
	// doubling up to allDownMax.
	allDownFirst = 15 * time.Second
	allDownMax   = 2 * time.Minute

	// Bounds on each check.
	tunnelCheckTimeout = 8 * time.Second
	configCheckTimeout = 12 * time.Second

	// How long a config switched away from is kept out of the smart
	// selector's choices - see Hooks.Switched.
	AvoidFor = 10 * time.Minute
)

// State is what the guard currently makes of the connection, for the UI.
type State string

const (
	StateOff          State = "off"          // the setting is off
	StateIdle         State = "idle"         // nothing is connected, nothing to keep up
	StateOK           State = "ok"           // internet goes through the current config
	StateChecking     State = "checking"     // a check failed, confirming
	StateNoNetwork    State = "no_network"   // no internet even without the VPN
	StateReconnecting State = "reconnecting" // reconnecting the current config
	StateSearching    State = "searching"    // checking which configs carry internet
	StateSwitching    State = "switching"    // connecting to another config
	StateAllDown      State = "all_down"     // no config carries internet right now
)

// Status is the guard's state as the UI shows it.
type Status struct {
	State State `json:"state"`
	// The config concerned: the one in use (ok, reconnecting), or the one
	// being switched to (switching).
	ConfigID string `json:"configId,omitempty"`
	// When the guard last moved the tunnel, and from/to which config -
	// so the UI can say "switched to X" for a while after it happened.
	SwitchedAt int64  `json:"switchedAt,omitempty"`
	SwitchedTo string `json:"switchedTo,omitempty"`
	// How many configs carried internet at the last search.
	Working int `json:"working,omitempty"`
}

// JSON is Status as one string - the form both UIs poll.
func (s Status) JSON() string {
	data, _ := json.Marshal(s)
	return string(data)
}

// Candidate is a saved config the guard may connect to.
type Candidate struct {
	ID   string
	YAML string
}

// Hooks is everything the guard needs from the platform.
type Hooks struct {
	// Active is the config that should be connected ("" when the user hasn't
	// connected anything) and whether a tunnel for it is up right now.
	Active func() (id string, up bool)

	// Busy reports a connect already in flight elsewhere (the user's own, a
	// network-change reconnect, the smart selector's switch). The round is
	// skipped rather than racing it. Optional.
	Busy func() bool

	// Tunnel checks internet through the live tunnel.
	Tunnel func(ctx context.Context) error

	// Direct checks internet around the VPN.
	Direct func(ctx context.Context) error

	// Config checks internet through one config dialed on its own - with
	// the relay applied when one is in use - returning how long it took.
	Config func(ctx context.Context, yaml string) (latencyMs int64, err error)

	// Candidates are the configs the guard may move the tunnel to: the smart
	// mode's or "connect to the best"'s own set while one of those is on,
	// otherwise every saved config.
	Candidates func() []Candidate

	// Connect (re)builds the tunnel on id and returns once it's up or has
	// failed.
	Connect func(id string) error

	// Switched is told the guard moved the tunnel from one config to
	// another, so the smart selector can adopt the new one and stay away
	// from the old (see routing.Selector.Adopt/Avoid). Optional.
	Switched func(from, to string)
}

// Guard runs the rounds. Build with New, then Start; Stop when the setting is
// turned off.
type Guard struct {
	h Hooks

	mu     sync.Mutex
	status Status

	// Round state - touched only by the loop goroutine (or a test calling
	// Round directly).
	fails       int    // consecutive failed tunnel checks
	incidentID  string // the config the current incident is about
	reconnected bool   // the current config was already reconnected once this incident
	allDownWait time.Duration

	kick     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
}

// New builds a guard over h. It does nothing until Start.
func New(h Hooks) *Guard {
	return &Guard{
		h:      h,
		status: Status{State: StateIdle},
		kick:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
	}
}

// Start runs rounds in the background until Stop.
func (g *Guard) Start() {
	go func() {
		next := confirmInterval // let a fresh tunnel settle before the first check
		for {
			timer := time.NewTimer(next)
			select {
			case <-g.stop:
				timer.Stop()
				return
			case <-g.kick:
				timer.Stop()
			case <-timer.C:
			}
			next = g.Round()
		}
	}()
}

// Stop ends the rounds. Idempotent. A round in flight finishes its current
// step; it won't start another connect after Stop.
func (g *Guard) Stop() {
	g.stopOnce.Do(func() { close(g.stop) })
	g.setStatus(Status{State: StateOff})
}

// Kick runs a round now - after the device's network changed, or a connect
// the guard should look at straight away.
func (g *Guard) Kick() {
	select {
	case g.kick <- struct{}{}:
	default:
	}
}

// Status returns the guard's current state.
func (g *Guard) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.status
}

func (g *Guard) setStatus(s Status) {
	g.mu.Lock()
	// The last switch stays on the record until the next one.
	if s.SwitchedAt == 0 {
		s.SwitchedAt, s.SwitchedTo = g.status.SwitchedAt, g.status.SwitchedTo
	}
	changed := s.State != g.status.State || s.ConfigID != g.status.ConfigID
	g.status = s
	g.mu.Unlock()
	if changed {
		diag.Event(diag.CatVPN, "guardState", "state", string(s.State), "config", short(s.ConfigID), "working", s.Working)
	}
}

func (g *Guard) stopped() bool {
	select {
	case <-g.stop:
		return true
	default:
		return false
	}
}

// Round runs one round and returns how long to wait before the next.
// Exported for tests; Start calls it in its loop.
func (g *Guard) Round() time.Duration {
	if g.stopped() {
		return healthyInterval
	}
	id, up := g.h.Active()
	if id == "" {
		g.resetIncident("")
		g.setStatus(Status{State: StateIdle})
		return healthyInterval
	}
	if g.h.Busy != nil && g.h.Busy() {
		return confirmInterval
	}
	if id != g.incidentID {
		// The user (or the selector) moved to another config: what was
		// counted against the old one doesn't apply to it.
		g.resetIncident(id)
	}

	if up {
		ctx, cancel := context.WithTimeout(context.Background(), tunnelCheckTimeout)
		err := g.h.Tunnel(ctx)
		cancel()
		if err == nil {
			g.resetIncident(id)
			g.setStatus(Status{State: StateOK, ConfigID: id})
			return healthyInterval
		}
		g.fails++
		diag.Event(diag.CatVPN, "guardTunnelFail", "config", short(id), "fails", g.fails, "err", err)
		if g.fails < failsToAct {
			g.setStatus(Status{State: StateChecking, ConfigID: id})
			return confirmInterval
		}
	} else {
		// Wanted but down: a reconnect that gave up, a connect that failed.
		// Nothing to confirm - there's no tunnel to check.
		g.fails = failsToAct
	}

	ctx, cancel := context.WithTimeout(context.Background(), tunnelCheckTimeout)
	err := g.h.Direct(ctx)
	cancel()
	if err != nil {
		// Switching servers can't fix the network itself. The failure count
		// stays, so the round after the network returns acts at once if the
		// tunnel doesn't come back with it.
		diag.Event(diag.CatVPN, "guardNoNetwork", "config", short(id), "err", err)
		g.setStatus(Status{State: StateNoNetwork, ConfigID: id})
		return noNetworkInterval
	}
	return g.recover(id)
}

// recover runs once the tunnel is known to carry nothing while the network
// itself works.
func (g *Guard) recover(id string) time.Duration {
	if now, _ := g.h.Active(); now != id {
		return confirmInterval // disconnected or moved meanwhile: nothing to recover
	}
	// A blip usually leaves the connection to the server dead in a way the
	// pool hasn't noticed: rebuilding the tunnel on the same config is the
	// cheapest fix, and keeps the user where they chose to be.
	if !g.reconnected {
		g.reconnected = true
		g.setStatus(Status{State: StateReconnecting, ConfigID: id})
		if g.connect(id, id) {
			return healthyInterval
		}
	}

	g.setStatus(Status{State: StateSearching, ConfigID: id})
	working := g.working()
	if len(working) == 0 {
		// Nothing carries internet. Keep what there is (a later round may
		// find the current one back) and look again, less and less often.
		if g.allDownWait == 0 {
			g.allDownWait = allDownFirst
		} else if g.allDownWait < allDownMax {
			g.allDownWait *= 2
			if g.allDownWait > allDownMax {
				g.allDownWait = allDownMax
			}
		}
		// The next round starts over: reconnecting the current config is
		// worth another try once time has passed.
		g.reconnected = false
		g.setStatus(Status{State: StateAllDown, ConfigID: id})
		return g.allDownWait
	}

	// The fastest config that isn't the one that just failed; the current
	// one only if it's the only config left that works (it answered on a
	// fresh connection, so one more rebuild is worth it).
	target := working[0]
	for _, w := range working {
		if w.ID != id {
			target = w
			break
		}
	}
	g.setStatus(Status{State: StateSwitching, ConfigID: target.ID, Working: len(working)})
	if g.connect(id, target.ID) {
		return healthyInterval
	}
	return confirmInterval
}

// connect moves the tunnel to "to" and checks it carries internet. Reports
// whether it does. Refuses when the user has disconnected (or picked another
// config) meanwhile: the guard must never connect behind their back.
func (g *Guard) connect(from, to string) bool {
	if g.stopped() {
		return false
	}
	if id, _ := g.h.Active(); id != from {
		return false
	}
	diag.Event(diag.CatVPN, "guardConnect", "from", short(from), "to", short(to))
	if err := g.h.Connect(to); err != nil {
		diag.Event(diag.CatVPN, "guardConnectFail", "to", short(to), "err", err)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), tunnelCheckTimeout)
	err := g.h.Tunnel(ctx)
	cancel()
	if err != nil {
		diag.Event(diag.CatVPN, "guardStillDown", "config", short(to), "err", err)
		return false
	}
	if from != to && g.h.Switched != nil {
		g.h.Switched(from, to)
	}
	g.resetIncident(to)
	st := Status{State: StateOK, ConfigID: to}
	if from != to {
		st.SwitchedAt, st.SwitchedTo = time.Now().Unix(), to
	}
	g.setStatus(st)
	return true
}

type result struct {
	Candidate
	latencyMs int64
}

// working checks every candidate at once and returns those that carry
// internet, fastest first.
func (g *Guard) working() []result {
	cands := g.h.Candidates()
	results := make([]*result, len(cands))
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Add(1)
		go func(i int, c Candidate) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), configCheckTimeout)
			defer cancel()
			ms, err := g.h.Config(ctx, c.YAML)
			if err == nil {
				results[i] = &result{Candidate: c, latencyMs: ms}
			}
		}(i, c)
	}
	wg.Wait()
	out := make([]result, 0, len(results))
	for _, r := range results {
		if r != nil {
			out = append(out, *r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].latencyMs < out[j].latencyMs })
	diag.Event(diag.CatVPN, "guardSearch", "candidates", len(cands), "working", len(out))
	return out
}

func (g *Guard) resetIncident(id string) {
	g.incidentID = id
	g.fails = 0
	g.reconnected = false
	g.allDownWait = 0
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "none"
	}
	return id
}

// Bound runs fn and returns its result, or ctx's error once ctx ends first -
// for checks (a ping, a config probe) that take no context of their own.
// fn keeps running in the background past that; its result is dropped.
func Bound(ctx context.Context, fn func() (int64, error)) (int64, error) {
	type res struct {
		ms  int64
		err error
	}
	done := make(chan res, 1)
	go func() {
		ms, err := fn()
		done <- res{ms, err}
	}()
	select {
	case r := <-done:
		return r.ms, r.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
