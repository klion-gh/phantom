package transport

import (
	"context"
	"errors"
	"net"
	"testing"

	"phantom/internal/protocol"
)

// fakeDial builds a dialOneFn that succeeds only for addrs in ok, recording the
// order of attempts into *tried.
func fakeDial(ok map[string]bool, tried *[]string) dialOneFn {
	return func(_ context.Context, addr string) (net.Conn, *protocol.SessionCrypto, error) {
		*tried = append(*tried, addr)
		if ok[addr] {
			return nil, nil, nil // success (conn/crypto nil is fine for these assertions)
		}
		return nil, nil, errors.New("refused")
	}
}

func TestFailoverPrefersFirstWorkingAndRemembersIt(t *testing.T) {
	addrs := []string{"a:1", "b:2", "c:3"}

	// a is dead, b works, c works.
	var tried []string
	dial := newFailoverDialer(addrs, fakeDial(map[string]bool{"b:2": true, "c:3": true}, &tried))

	// First call: a fails, b succeeds -> tried [a, b].
	if _, _, err := dial(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := tried; len(got) != 2 || got[0] != "a:1" || got[1] != "b:2" {
		t.Fatalf("first call order = %v, want [a:1 b:2]", got)
	}

	// Second call must start from the last good (b), not re-try the dead a.
	tried = nil
	if _, _, err := dial(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := tried; len(got) != 1 || got[0] != "b:2" {
		t.Fatalf("second call order = %v, want just [b:2] (last-good remembered)", got)
	}
}

func TestFailoverRotatesThroughAllAndReturnsLastErrorWhenAllDead(t *testing.T) {
	addrs := []string{"a:1", "b:2", "c:3"}
	var tried []string
	dial := newFailoverDialer(addrs, fakeDial(map[string]bool{}, &tried))

	_, _, err := dial(context.Background())
	if err == nil {
		t.Fatal("expected an error when every endpoint is dead")
	}
	if len(tried) != 3 {
		t.Fatalf("expected all 3 endpoints tried, got %v", tried)
	}
}

func TestFailoverSingleEndpoint(t *testing.T) {
	var tried []string
	dial := newFailoverDialer([]string{"only:1"}, fakeDial(map[string]bool{"only:1": true}, &tried))
	if _, _, err := dial(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tried) != 1 || tried[0] != "only:1" {
		t.Fatalf("tried = %v, want [only:1]", tried)
	}
}
