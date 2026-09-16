package routing

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeProbe lets a test dictate each config's latency/reachability per round.
type fakeProbe struct {
	mu      sync.Mutex
	results map[string]int64 // yaml -> latency; absent means unreachable
}

func (f *fakeProbe) set(yaml string, latency int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[yaml] = latency
}

func (f *fakeProbe) kill(yaml string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.results, yaml)
}

func (f *fakeProbe) probe(yaml string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if latency, ok := f.results[yaml]; ok {
		return latency, nil
	}
	return 0, errors.New("unreachable")
}

func newTestSelector(t *testing.T, initial map[string]int64) (*Selector, *fakeProbe, *[]string) {
	t.Helper()
	fake := &fakeProbe{results: map[string]int64{}}
	for yaml, latency := range initial {
		fake.results[yaml] = latency
	}
	var switches []string
	s := NewSelector(fake.probe, func(c Candidate) {
		switches = append(switches, c.ID)
	})
	return s, fake, &switches
}

func candidates(ids ...string) []Candidate {
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, Candidate{ID: id, YAML: id}) // yaml==id keeps the fake simple
	}
	return out
}

func TestSelectorPicksLowestLatencyFirst(t *testing.T) {
	s, _, switches := newTestSelector(t, map[string]int64{"a": 200, "b": 50, "c": 120})
	s.SetCandidates(candidates("a", "b", "c"))

	s.Probe()

	if got := s.Current(); got != "b" {
		t.Fatalf("expected the fastest config to be chosen, got %q", got)
	}
	if len(*switches) != 1 || (*switches)[0] != "b" {
		t.Fatalf("expected exactly one switch to b, got %v", *switches)
	}
}

// The core anti-flap guarantee: a working config is not abandoned just because
// something faster showed up.
func TestSelectorStaysOnWorkingConfigDespiteFasterRival(t *testing.T) {
	s, fake, switches := newTestSelector(t, map[string]int64{"a": 100, "b": 400})
	s.SetCandidates(candidates("a", "b"))
	s.Probe()
	if s.Current() != "a" {
		t.Fatalf("setup: expected a, got %q", s.Current())
	}

	// b becomes dramatically faster, and enough time passes that dwell is not
	// what's holding the switch back.
	fake.set("b", 10)
	s.mu.Lock()
	s.lastSwitch = time.Now().Add(-10 * minDwell)
	s.mu.Unlock()
	s.Probe()

	// a (100ms) vs b (10ms) clears the ratio but the absolute margin is what
	// decides it: 90ms < betterMarginMs, so this is not worth killing live
	// connections for.
	if s.Current() != "a" {
		t.Fatalf("expected to stay on a for a sub-margin gain, moved to %q", s.Current())
	}
	if len(*switches) != 1 {
		t.Fatalf("expected no extra switch, got %v", *switches)
	}
}

func TestSelectorSwitchesForLargeSustainedWin(t *testing.T) {
	s, fake, _ := newTestSelector(t, map[string]int64{"a": 600, "b": 900})
	s.SetCandidates(candidates("a", "b"))
	s.Probe()
	if s.Current() != "a" {
		t.Fatalf("setup: expected a, got %q", s.Current())
	}

	fake.set("b", 50) // 600 vs 50: clears both the ratio and the 150ms margin
	s.mu.Lock()
	s.lastSwitch = time.Now().Add(-10 * minDwell)
	s.mu.Unlock()
	s.Probe()

	if s.Current() != "b" {
		t.Fatalf("expected a switch to the much faster config, still on %q", s.Current())
	}
}

// Even a huge win must respect the dwell window, so a flapping server can't
// drag the tunnel back and forth every probe round.
func TestSelectorRespectsDwellWindow(t *testing.T) {
	s, fake, _ := newTestSelector(t, map[string]int64{"a": 600, "b": 900})
	s.SetCandidates(candidates("a", "b"))
	s.Probe() // selects a, sets lastSwitch = now

	fake.set("b", 10)
	s.Probe() // immediately after: inside the dwell window

	if s.Current() != "a" {
		t.Fatalf("expected the dwell window to hold the selection on a, got %q", s.Current())
	}
}

// A dead config is different: the user has no connectivity, so the dwell
// window must not apply.
func TestSelectorLeavesDeadConfigIgnoringDwell(t *testing.T) {
	s, fake, _ := newTestSelector(t, map[string]int64{"a": 50, "b": 300})
	s.SetCandidates(candidates("a", "b"))
	s.Probe()
	if s.Current() != "a" {
		t.Fatalf("setup: expected a, got %q", s.Current())
	}

	fake.kill("a")

	// One failure is a blip - still not enough to move.
	s.Probe()
	if s.Current() != "a" {
		t.Fatalf("expected one failure to be tolerated, already moved to %q", s.Current())
	}

	// Two in a row is a pattern, and this happens well inside minDwell.
	s.Probe()
	if s.Current() != "b" {
		t.Fatalf("expected a move to the surviving config, still on %q", s.Current())
	}
}

func TestSelectorKeepsSelectionWhenEverythingIsDown(t *testing.T) {
	s, fake, _ := newTestSelector(t, map[string]int64{"a": 50})
	s.SetCandidates(candidates("a", "b"))
	s.Probe()

	fake.kill("a")
	s.Probe()
	s.Probe()
	s.Probe()

	// Nothing reachable to move to: hold the selection rather than clearing
	// it, so recovery reuses the same config instead of starting from nothing.
	if s.Current() != "a" {
		t.Fatalf("expected the selection to be held during a total outage, got %q", s.Current())
	}
}

func TestSelectorReselectsWhenCurrentRemovedFromList(t *testing.T) {
	s, _, _ := newTestSelector(t, map[string]int64{"a": 50, "b": 60})
	s.SetCandidates(candidates("a", "b"))
	s.Probe()
	if s.Current() != "a" {
		t.Fatalf("setup: expected a, got %q", s.Current())
	}

	s.SetCandidates(candidates("b")) // user unticked a
	s.Probe()

	if s.Current() != "b" {
		t.Fatalf("expected reselection after the current config left the list, got %q", s.Current())
	}
}

func TestSelectorPreservesHealthAcrossCandidateEdits(t *testing.T) {
	s, _, _ := newTestSelector(t, map[string]int64{"a": 50, "b": 60})
	s.SetCandidates(candidates("a", "b"))
	s.Probe()

	s.SetCandidates(candidates("a", "b", "c")) // user added one

	probed := map[string]bool{}
	for _, h := range s.Snapshot() {
		probed[h.ID] = h.Probed
	}
	if !probed["a"] || !probed["b"] {
		t.Fatalf("expected known health to survive a candidate edit: %v", s.Snapshot())
	}
	if probed["c"] {
		t.Fatalf("expected the newly added config to start unprobed")
	}
}
