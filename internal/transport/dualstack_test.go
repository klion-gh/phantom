package transport

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type familyLookup struct {
	v4, v6  []net.IP
	v6Delay time.Duration
	v4Err   error
}

func (f familyLookup) LookupIP(ctx context.Context, network, _ string) ([]net.IP, error) {
	if network == "ip6" {
		select {
		case <-time.After(f.v6Delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return f.v6, nil
	}
	return f.v4, f.v4Err
}

var (
	v4a = net.ParseIP("203.0.113.1")
	v4b = net.ParseIP("203.0.113.2")
	v6a = net.ParseIP("2001:db8::1")
)

func TestLookupDualInterleavesIPv4First(t *testing.T) {
	ips, err := LookupDual(context.Background(), familyLookup{v4: []net.IP{v4a, v4b}, v6: []net.IP{v6a}}, "s.example")
	if err != nil {
		t.Fatal(err)
	}
	want := []net.IP{v4a, v6a, v4b}
	if len(ips) != len(want) {
		t.Fatalf("got %v", ips)
	}
	for i := range want {
		if !ips[i].Equal(want[i]) {
			t.Fatalf("got %v, want %v", ips, want)
		}
	}
}

func TestLookupDualDoesNotWaitOutASlowAAAA(t *testing.T) {
	start := time.Now()
	ips, err := LookupDual(context.Background(), familyLookup{v4: []net.IP{v4a}, v6Delay: time.Hour}, "s.example")
	if err != nil || len(ips) != 1 || !ips[0].Equal(v4a) {
		t.Fatalf("got %v, %v", ips, err)
	}
	if took := time.Since(start); took > 2*v6Grace {
		t.Fatalf("took %s waiting for AAAA", took)
	}
}

func TestLookupDualUsesIPv6WhenIPv4Fails(t *testing.T) {
	ips, err := LookupDual(context.Background(), familyLookup{v4Err: errors.New("nx"), v6: []net.IP{v6a}, v6Delay: 50 * time.Millisecond}, "s.example")
	if err != nil || len(ips) != 1 || !ips[0].Equal(v6a) {
		t.Fatalf("got %v, %v", ips, err)
	}
}

// The case that matters: IPv4 blackholed (connect hangs), IPv6 answers - the
// race must reach IPv6 after the stagger, not after IPv4's timeout.
func TestDialRaceReachesIPv6PastABlackholedIPv4(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	dial := func(ctx context.Context, target string) (net.Conn, error) {
		if target == "blackhole" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := dialRace(ctx, dial, []string{"blackhole", ln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if took := time.Since(start); took > raceStagger+time.Second {
		t.Fatalf("took %s - the second address should start after the stagger", took)
	}
}

func TestDialRaceMovesOnAtOnceWhenAnAttemptFails(t *testing.T) {
	old := raceStagger
	raceStagger = time.Hour
	defer func() { raceStagger = old }()
	calls := 0
	dial := func(ctx context.Context, target string) (net.Conn, error) {
		calls++
		if target == "refused" {
			return nil, errors.New("refused")
		}
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}
	conn, err := dialRace(context.Background(), dial, []string{"refused", "ok"})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestStaticResolverFiltersByFamily(t *testing.T) {
	r := StaticResolver{"s.example": {v4a, v6a}}
	if ips, _ := r.LookupIP(context.Background(), "ip6", "s.example"); len(ips) != 1 || !ips[0].Equal(v6a) {
		t.Fatalf("ip6: %v", ips)
	}
	if _, err := r.LookupIP(context.Background(), "ip", "other.example"); err == nil {
		t.Fatal("unknown host should fail")
	}
}
