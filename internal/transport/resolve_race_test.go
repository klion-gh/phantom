package transport

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// fakeLookup answers after delay with ips, or with err; a negative delay
// means it never answers on its own and only returns when ctx ends - a
// resolver whose UDP is silently dropped.
type fakeLookup struct {
	delay time.Duration
	ips   []net.IP
	err   error
	calls atomic.Int32
}

func (f *fakeLookup) LookupIP(ctx context.Context, _, _ string) ([]net.IP, error) {
	f.calls.Add(1)
	if f.delay < 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	select {
	case <-time.After(f.delay):
		return f.ips, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func withHeadStart(t *testing.T, d time.Duration) {
	old := systemHeadStart
	systemHeadStart = d
	t.Cleanup(func() { systemHeadStart = old })
}

var (
	publicIP = []net.IP{net.ParseIP("203.0.113.1")}
	systemIP = []net.IP{net.ParseIP("203.0.113.2")}
)

func TestProtectedResolverFallsBackWhenPublicDNSIsSilent(t *testing.T) {
	withHeadStart(t, 50*time.Millisecond)
	r := &protectedResolver{
		direct: []IPLookuper{&fakeLookup{delay: -1}, &fakeLookup{delay: -1}},
		system: &fakeLookup{ips: systemIP},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	ips, err := r.LookupIP(ctx, "ip", "server.example")
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if !ips[0].Equal(systemIP[0]) {
		t.Fatalf("got %v, want the system resolver's answer", ips)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %s - the system resolver should be asked after the head start, not at the deadline", took)
	}
}

func TestProtectedResolverAsksSystemAtOnceWhenPublicDNSFails(t *testing.T) {
	withHeadStart(t, time.Hour)
	r := &protectedResolver{
		direct: []IPLookuper{&fakeLookup{err: errors.New("refused")}, &fakeLookup{err: errors.New("refused")}},
		system: &fakeLookup{ips: systemIP},
	}
	ips, err := r.LookupIP(context.Background(), "ip", "server.example")
	if err != nil || !ips[0].Equal(systemIP[0]) {
		t.Fatalf("got %v, %v - want the system answer without waiting out the head start", ips, err)
	}
}

func TestProtectedResolverPrefersPublicDNS(t *testing.T) {
	withHeadStart(t, 200*time.Millisecond)
	system := &fakeLookup{ips: systemIP}
	r := &protectedResolver{
		direct: []IPLookuper{&fakeLookup{delay: -1}, &fakeLookup{delay: 10 * time.Millisecond, ips: publicIP}},
		system: system,
	}
	ips, err := r.LookupIP(context.Background(), "ip", "server.example")
	if err != nil || !ips[0].Equal(publicIP[0]) {
		t.Fatalf("got %v, %v - want the public answer", ips, err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := system.calls.Load(); n != 0 {
		t.Fatalf("system resolver asked %d times though a public one answered first", n)
	}
}

func TestProtectedResolverFailsOnlyWhenEveryResolverDoes(t *testing.T) {
	withHeadStart(t, 10*time.Millisecond)
	r := &protectedResolver{
		direct: []IPLookuper{&fakeLookup{err: errors.New("a")}, &fakeLookup{ips: nil}},
		system: &fakeLookup{err: errors.New("c")},
	}
	if ips, err := r.LookupIP(context.Background(), "ip", "server.example"); err == nil {
		t.Fatalf("got %v with no error, want a failure", ips)
	}
}
