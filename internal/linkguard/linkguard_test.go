package linkguard

import (
	"context"
	"errors"
	"testing"
)

// fake is a platform with a scripted network: which configs carry internet,
// whether the device has any without the VPN, and what's connected.
type fake struct {
	active    string
	up        bool
	direct    bool
	tunnelOK  map[string]bool // whether a tunnel on this config carries internet
	configOK  map[string]bool // whether a fresh check of this config passes
	latency   map[string]int64
	cands     []Candidate
	connects  []string
	switched  [][2]string
	failDial  map[string]bool
	tunnelErr int  // tunnel checks to fail before honouring tunnelOK, for blips
	stale     bool // the live session is dead; a Connect replaces it
}

var errDown = errors.New("down")

func (f *fake) hooks() Hooks {
	return Hooks{
		Active: func() (string, bool) { return f.active, f.up },
		Tunnel: func(context.Context) error {
			if f.tunnelErr > 0 {
				f.tunnelErr--
				return errDown
			}
			if f.up && !f.stale && f.tunnelOK[f.active] {
				return nil
			}
			return errDown
		},
		Direct: func(context.Context) error {
			if f.direct {
				return nil
			}
			return errDown
		},
		Config: func(_ context.Context, yaml string) (int64, error) {
			if f.configOK[yaml] {
				return f.latency[yaml], nil
			}
			return 0, errDown
		},
		Candidates: func() []Candidate { return f.cands },
		Connect: func(id string) error {
			f.connects = append(f.connects, id)
			if f.failDial[id] {
				f.up = false
				return errDown
			}
			f.active, f.up, f.stale = id, true, false
			return nil
		},
		Switched: func(from, to string) { f.switched = append(f.switched, [2]string{from, to}) },
	}
}

func newFake() *fake {
	return &fake{
		active: "a", up: true, direct: true,
		tunnelOK: map[string]bool{"a": true, "b": true, "c": true},
		configOK: map[string]bool{"a": true, "b": true, "c": true},
		latency:  map[string]int64{"a": 50, "b": 80, "c": 30},
		cands:    []Candidate{{"a", "a"}, {"b", "b"}, {"c", "c"}},
		failDial: map[string]bool{},
	}
}

func TestHealthyTunnelIsLeftAlone(t *testing.T) {
	f := newFake()
	g := New(f.hooks())
	if d := g.Round(); d != healthyInterval {
		t.Fatalf("next round in %s, want %s", d, healthyInterval)
	}
	if len(f.connects) != 0 || g.Status().State != StateOK {
		t.Fatalf("connects=%v state=%s", f.connects, g.Status().State)
	}
}

func TestOneMissIsOnlyConfirmed(t *testing.T) {
	f := newFake()
	f.tunnelErr = 1
	g := New(f.hooks())
	if d := g.Round(); d != confirmInterval || g.Status().State != StateChecking {
		t.Fatalf("after one miss: next=%s state=%s", d, g.Status().State)
	}
	if g.Round(); g.Status().State != StateOK || len(f.connects) != 0 {
		t.Fatalf("a blip that passed moved the tunnel: connects=%v state=%s", f.connects, g.Status().State)
	}
}

func TestNoNetworkDoesNotSwitch(t *testing.T) {
	f := newFake()
	f.stale = true
	f.direct = false
	g := New(f.hooks())
	g.Round()
	g.Round()
	if g.Status().State != StateNoNetwork || len(f.connects) != 0 {
		t.Fatalf("with no network: state=%s connects=%v", g.Status().State, f.connects)
	}
	// The network comes back but the tunnel doesn't: acted on at once.
	f.direct = true
	g.Round()
	if len(f.connects) != 1 || f.connects[0] != "a" || g.Status().State != StateOK {
		t.Fatalf("after the network returned: connects=%v state=%s", f.connects, g.Status().State)
	}
}

func TestReconnectFixesIt(t *testing.T) {
	f := newFake()
	f.stale = true // the old session is dead; a rebuilt one works
	g := New(f.hooks())
	g.Round()
	g.Round()
	if len(f.connects) != 1 || f.connects[0] != "a" {
		t.Fatalf("want one reconnect of a, got %v", f.connects)
	}
	if len(f.switched) != 0 {
		t.Fatalf("a reconnect reported as a switch: %v", f.switched)
	}
}

func TestSwitchesToFastestWorking(t *testing.T) {
	f := newFake()
	f.tunnelOK["a"] = false
	f.configOK["a"] = false
	f.configOK["c"] = false // fastest, but carries nothing
	g := New(f.hooks())
	g.Round()
	g.Round()
	// a reconnected first, then b.
	if len(f.connects) != 2 || f.connects[0] != "a" || f.connects[1] != "b" {
		t.Fatalf("connects=%v, want [a b]", f.connects)
	}
	if f.active != "b" || len(f.switched) != 1 || f.switched[0] != [2]string{"a", "b"} {
		t.Fatalf("active=%s switched=%v", f.active, f.switched)
	}
	st := g.Status()
	if st.State != StateOK || st.SwitchedTo != "b" || st.SwitchedAt == 0 {
		t.Fatalf("status=%+v", st)
	}
}

func TestPrefersAnotherConfigOverTheFailedOne(t *testing.T) {
	f := newFake()
	f.tunnelOK["a"] = false // a's tunnel carries nothing, though a fresh check passes
	f.latency["a"] = 10     // and it's the fastest
	f.configOK["c"] = false
	g := New(f.hooks())
	g.Round()
	g.Round()
	if f.active != "b" {
		t.Fatalf("active=%s connects=%v, want b", f.active, f.connects)
	}
}

func TestAllDownBacksOffAndKeepsCurrent(t *testing.T) {
	f := newFake()
	for id := range f.tunnelOK {
		f.tunnelOK[id] = false
		f.configOK[id] = false
	}
	g := New(f.hooks())
	g.Round()
	d1 := g.Round()
	if g.Status().State != StateAllDown || d1 != allDownFirst {
		t.Fatalf("state=%s next=%s", g.Status().State, d1)
	}
	if len(f.connects) != 1 || f.connects[0] != "a" {
		t.Fatalf("connects=%v, want only the reconnect of a", f.connects)
	}
	d2 := g.Round()
	if d2 != 2*allDownFirst {
		t.Fatalf("second wait %s, want %s", d2, 2*allDownFirst)
	}
}

func TestWantedButDownIsRecovered(t *testing.T) {
	f := newFake()
	f.up = false // a network-change reconnect gave up
	g := New(f.hooks())
	g.Round()
	if !f.up || f.active != "a" || len(f.connects) != 1 {
		t.Fatalf("up=%v active=%s connects=%v", f.up, f.active, f.connects)
	}
}

func TestNothingWantedDoesNothing(t *testing.T) {
	f := newFake()
	f.active, f.up = "", false
	g := New(f.hooks())
	g.Round()
	if len(f.connects) != 0 || g.Status().State != StateIdle {
		t.Fatalf("connects=%v state=%s", f.connects, g.Status().State)
	}
}

func TestNeverConnectsAfterUserDisconnected(t *testing.T) {
	f := newFake()
	f.tunnelOK["a"] = false
	h := f.hooks()
	calls := 0
	inner := h.Direct
	h.Direct = func(ctx context.Context) error {
		calls++
		f.active, f.up = "", false // the user disconnects while the guard is checking
		return inner(ctx)
	}
	g := New(h)
	g.Round()
	g.Round()
	if calls == 0 || len(f.connects) != 0 {
		t.Fatalf("connected behind the user's back: %v", f.connects)
	}
}

func TestBusySkipsTheRound(t *testing.T) {
	f := newFake()
	f.tunnelOK["a"] = false
	h := f.hooks()
	h.Busy = func() bool { return true }
	g := New(h)
	g.Round()
	g.Round()
	if len(f.connects) != 0 {
		t.Fatalf("raced a connect in flight: %v", f.connects)
	}
}

func TestStopEndsWithOff(t *testing.T) {
	g := New(newFake().hooks())
	g.Stop()
	g.Stop()
	if g.Status().State != StateOff {
		t.Fatalf("state=%s", g.Status().State)
	}
}

func TestBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	block := make(chan struct{})
	defer close(block)
	if _, err := Bound(ctx, func() (int64, error) { <-block; return 0, nil }); err == nil {
		t.Fatal("Bound ignored a finished context")
	}
	if ms, err := Bound(context.Background(), func() (int64, error) { return 7, nil }); err != nil || ms != 7 {
		t.Fatalf("ms=%d err=%v", ms, err)
	}
}
