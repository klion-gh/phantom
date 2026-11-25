package relaypath

import (
	"errors"
	"testing"
	"time"

	"phantom/internal/pingcheck"
)

const directCfg = `server: "203.0.113.1:443"
domain: "s.example"
psk: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
server_public_key: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
`
const relayedCfg = directCfg + `relay: "198.51.100.1:443"
`

func reset() {
	mu.Lock()
	direct = map[string]time.Time{}
	mu.Unlock()
}

func TestPingFallsBackToDirectAndRemembers(t *testing.T) {
	reset()
	old := HeadStart
	HeadStart = time.Hour
	defer func() { HeadStart = old }()
	if domainOf(directCfg) != "s.example" {
		t.Fatalf("test config does not parse")
	}
	calls := []string{}
	fn := func(y string) (pingcheck.Result, error) {
		calls = append(calls, y)
		if y == relayedCfg {
			return pingcheck.Result{}, errors.New("relay can't reach it")
		}
		return pingcheck.Result{LatencyMs: 42}, nil
	}
	r, err := Ping(directCfg, relayedCfg, fn)
	if err != nil || r.LatencyMs != 42 {
		t.Fatalf("got %v, %v", r, err)
	}
	if len(calls) != 2 || calls[0] != relayedCfg {
		t.Fatalf("relay should be tried first, then direct: %d calls", len(calls))
	}
	if o := Order(directCfg, relayedCfg); o[0] != directCfg {
		t.Fatal("direct should now be preferred")
	}
}

func TestPingRacesASilentRelay(t *testing.T) {
	reset()
	old := HeadStart
	HeadStart = 50 * time.Millisecond
	defer func() { HeadStart = old }()
	block := make(chan struct{})
	defer close(block)
	fn := func(y string) (pingcheck.Result, error) {
		if y == relayedCfg {
			<-block
			return pingcheck.Result{}, errors.New("timeout")
		}
		return pingcheck.Result{LatencyMs: 7}, nil
	}
	start := time.Now()
	r, err := Ping(directCfg, relayedCfg, fn)
	if err != nil || r.LatencyMs != 7 {
		t.Fatalf("got %v, %v", r, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("direct should join after the head start, not wait for the relay")
	}
}

func TestRelayWinClearsDirect(t *testing.T) {
	reset()
	record("s.example", true)
	fn := func(y string) (pingcheck.Result, error) {
		if y == directCfg {
			return pingcheck.Result{}, errors.New("direct broke")
		}
		return pingcheck.Result{LatencyMs: 1}, nil
	}
	if _, err := Ping(directCfg, relayedCfg, fn); err != nil {
		t.Fatal(err)
	}
	if o := Order(directCfg, relayedCfg); o[0] != relayedCfg {
		t.Fatal("relay should be preferred again")
	}
}

func TestNoRelayIsOnePath(t *testing.T) {
	reset()
	if o := Order(directCfg, directCfg); len(o) != 1 {
		t.Fatalf("got %d", len(o))
	}
}
