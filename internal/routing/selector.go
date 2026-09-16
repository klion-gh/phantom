package routing

import (
	"sort"
	"sync"
	"time"
)

// Tuning for the "pick the config that actually works" logic. These are the
// knobs that decide how eagerly the tunnel moves between the user's configs.
//
// The whole design here is biased *against* switching. A switch is not free -
// it tears down live connections, so every flow in flight dies and has to be
// re-established. A selector that chases the lowest latency would leave the
// user with a connection that technically always has the best ping and never
// finishes a download. So: a working config is kept even when a better one
// exists, and only a genuinely large, sustained advantage (or the current one
// actually failing) is allowed to move it.
const (
	// How often each candidate is probed. A full handshake per probe per
	// config, so this is deliberately not aggressive.
	probeInterval = 30 * time.Second

	// Consecutive failed probes before a *currently selected* config is
	// considered dead. One failure is usually a blip; two in a row across a
	// minute is a pattern.
	failsBeforeDrop = 2

	// Minimum time to stay on a config before an "it's merely better"
	// switch is allowed. Does not apply when the current config is dead -
	// a user with no connectivity should not wait this out.
	minDwell = 90 * time.Second

	// A candidate must beat the current one by *both* of these to justify
	// switching away from something that works: a ratio (so it doesn't
	// trigger on fast links where everything is close) and an absolute
	// margin (so it doesn't trigger on slow links where the ratio is easy
	// to hit but the real-world difference is tiny).
	betterRatio    = 2.0
	betterMarginMs = 150
)

// Candidate is one of the user's saved configs, as offered to the selector.
type Candidate struct {
	ID   string
	YAML string
}

// Health is the last known state of one candidate, for the UI to show.
type Health struct {
	ID        string `json:"id"`
	Alive     bool   `json:"alive"`
	LatencyMs int64  `json:"latency_ms"`
	Probed    bool   `json:"probed"`
}

// ProbeFunc measures one candidate, returning its latency. An error means
// unreachable. In production this is a real Phantom handshake
// (internal/pingcheck), which is the only honest signal - a plain TCP connect
// would call a blocked-but-listening port healthy.
type ProbeFunc func(configYAML string) (latencyMs int64, err error)

type candidateState struct {
	Candidate
	alive     bool
	latencyMs int64
	fails     int
	probed    bool
}

// Selector keeps the tunnel pointed at whichever of the user's chosen configs
// is actually usable, without flapping between them - see the tuning
// constants above for the policy.
//
// It owns no connection itself: when it decides the tunnel should move, it
// calls onSwitch and lets the platform layer do the reconnect.
type Selector struct {
	mu         sync.Mutex
	states     []*candidateState
	currentID  string
	lastSwitch time.Time

	probe    ProbeFunc
	onSwitch func(Candidate)

	stop    chan struct{}
	stopped bool
	now     func() time.Time // injectable for tests
}

// NewSelector builds a selector over probe. onSwitch is called (off the
// caller's goroutine) whenever the selected config should change, including
// the very first selection.
func NewSelector(probe ProbeFunc, onSwitch func(Candidate)) *Selector {
	return &Selector{
		probe:    probe,
		onSwitch: onSwitch,
		stop:     make(chan struct{}),
		now:      time.Now,
	}
}

// SetCandidates replaces the pool the selector chooses from, preserving what
// it already knows about configs that are still in the list. If the current
// selection is no longer among them, the next evaluation picks a replacement.
func (s *Selector) SetCandidates(candidates []Candidate) {
	s.mu.Lock()
	previous := map[string]*candidateState{}
	for _, st := range s.states {
		previous[st.ID] = st
	}
	states := make([]*candidateState, 0, len(candidates))
	for _, c := range candidates {
		if old, ok := previous[c.ID]; ok {
			old.Candidate = c // yaml may have been edited
			states = append(states, old)
			continue
		}
		states = append(states, &candidateState{Candidate: c})
	}
	s.states = states
	s.mu.Unlock()
}

// Current returns the currently selected config id, or "" if none has been
// picked yet.
func (s *Selector) Current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentID
}

// Snapshot returns per-candidate health for the UI.
func (s *Selector) Snapshot() []Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Health, 0, len(s.states))
	for _, st := range s.states {
		out = append(out, Health{ID: st.ID, Alive: st.alive, LatencyMs: st.latencyMs, Probed: st.probed})
	}
	return out
}

// Start begins probing in the background until Stop. Safe to call once.
func (s *Selector) Start() {
	go func() {
		s.Probe() // don't make the user wait a full interval for the first pick
		ticker := time.NewTicker(probeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.Probe()
			}
		}
	}()
}

// Stop ends background probing. Idempotent.
func (s *Selector) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		s.stopped = true
		close(s.stop)
	}
}

// Probe measures every candidate once and re-evaluates the selection. Exposed
// so the platform layer can force a round immediately after a network change
// (Wi-Fi <-> cellular), where waiting for the next tick would leave the user
// on a config that just became unreachable.
func (s *Selector) Probe() {
	// The yaml is copied out under the lock, not read from the state inside
	// the probe goroutines: SetCandidates rewrites that field in place when
	// the user edits a config, so reading it concurrently would be a genuine
	// data race even though the pointer itself stays valid.
	s.mu.Lock()
	states := make([]*candidateState, len(s.states))
	copy(states, s.states)
	yamls := make([]string, len(states))
	for i, st := range states {
		yamls[i] = st.YAML
	}
	s.mu.Unlock()

	// Probed concurrently: with several configs, doing this serially would
	// make one unreachable server (which only fails after its dial timeout)
	// delay the health of every config behind it.
	var wg sync.WaitGroup
	results := make([]struct {
		latency int64
		err     error
	}, len(states))
	for i := range states {
		wg.Add(1)
		go func(i int, yaml string) {
			defer wg.Done()
			results[i].latency, results[i].err = s.probe(yaml)
		}(i, yamls[i])
	}
	wg.Wait()

	s.mu.Lock()
	for i, st := range states {
		st.probed = true
		if results[i].err != nil {
			st.alive = false
			st.fails++
			continue
		}
		st.alive = true
		st.fails = 0
		st.latencyMs = results[i].latency
	}
	choice, changed := s.evaluateLocked()
	s.mu.Unlock()

	if changed && s.onSwitch != nil {
		s.onSwitch(choice)
	}
}

// evaluateLocked applies the switching policy. Caller holds s.mu.
func (s *Selector) evaluateLocked() (Candidate, bool) {
	best := bestLocked(s.states)
	if best == nil {
		return Candidate{}, false // nothing reachable; keep whatever we had
	}

	current := s.findLocked(s.currentID)

	// Nothing selected yet, or the selection vanished from the list.
	if current == nil {
		return s.selectLocked(best), true
	}

	// Current one is failing: move as soon as it's confirmed dead, ignoring
	// the dwell time - the user has no working connection to protect.
	if !current.alive {
		if current.fails >= failsBeforeDrop && best.ID != current.ID {
			return s.selectLocked(best), true
		}
		return Candidate{}, false
	}

	// Current one works. Only a big, sustained win moves us.
	if best.ID == current.ID || s.now().Sub(s.lastSwitch) < minDwell {
		return Candidate{}, false
	}
	if float64(current.latencyMs) > betterRatio*float64(best.latencyMs) &&
		current.latencyMs-best.latencyMs >= betterMarginMs {
		return s.selectLocked(best), true
	}
	return Candidate{}, false
}

func (s *Selector) selectLocked(st *candidateState) Candidate {
	s.currentID = st.ID
	s.lastSwitch = s.now()
	return st.Candidate
}

func (s *Selector) findLocked(id string) *candidateState {
	if id == "" {
		return nil
	}
	for _, st := range s.states {
		if st.ID == id {
			return st
		}
	}
	return nil
}

// bestLocked returns the lowest-latency reachable candidate, or nil. Ties
// break on id so the choice is stable rather than depending on map/probe
// ordering - an unstable "best" would make the dwell logic fight itself.
func bestLocked(states []*candidateState) *candidateState {
	alive := make([]*candidateState, 0, len(states))
	for _, st := range states {
		if st.alive {
			alive = append(alive, st)
		}
	}
	if len(alive) == 0 {
		return nil
	}
	sort.Slice(alive, func(i, j int) bool {
		if alive[i].latencyMs != alive[j].latencyMs {
			return alive[i].latencyMs < alive[j].latencyMs
		}
		return alive[i].ID < alive[j].ID
	})
	return alive[0]
}
