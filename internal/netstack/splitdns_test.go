package netstack

import (
	"encoding/binary"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// query builds a minimal DNS query for name with the given id.
func query(id uint16, name string) []byte {
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:2], id)
	binary.BigEndian.PutUint16(msg[4:6], 1) // QDCOUNT
	for _, label := range strings.Split(name, ".") {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	return append(msg, 0, 0, 1, 0, 1) // root, QTYPE=A, QCLASS=IN
}

func TestDNSQueryNameParsesQueriesAndRejectsTheRest(t *testing.T) {
	if got := dnsQueryName(query(7, "WWW.YouTube.com")); got != "www.youtube.com" {
		t.Fatalf("got %q", got)
	}
	resp := query(7, "example.com")
	resp[2] |= 0x80 // QR bit: a response
	if got := dnsQueryName(resp); got != "" {
		t.Fatalf("a response must not parse as a query, got %q", got)
	}
	if got := dnsQueryName([]byte{1, 2, 3}); got != "" {
		t.Fatalf("garbage must not parse, got %q", got)
	}
	truncated := query(7, "example.com")
	if got := dnsQueryName(truncated[:15]); got != "" {
		t.Fatalf("a truncated name must not parse, got %q", got)
	}
}

// fakeUpstream records queries written to it and answers when told to.
type fakeUpstream struct {
	mu      sync.Mutex
	writes  [][]byte
	answers chan []byte
}

func newFakeUpstream() *fakeUpstream { return &fakeUpstream{answers: make(chan []byte, 8)} }

func (f *fakeUpstream) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.writes = append(f.writes, append([]byte(nil), p...))
	f.mu.Unlock()
	return len(p), nil
}

func (f *fakeUpstream) Read(p []byte) (int, error) {
	b, ok := <-f.answers
	if !ok {
		return 0, io.EOF
	}
	return copy(p, b), nil
}

func (f *fakeUpstream) Close() error {
	defer func() { recover() }()
	close(f.answers)
	return nil
}

func (f *fakeUpstream) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func newTestSplitter(direct, tunnel *fakeUpstream, listed string, fallback time.Duration) (*dnsSplitter, *[][]byte, *sync.Mutex) {
	var mu sync.Mutex
	var replies [][]byte
	s := &dnsSplitter{
		route: func(name string) RouteDecision {
			if name == listed || strings.HasSuffix(name, "."+listed) {
				return RouteTunnel
			}
			return RouteDirect
		},
		fallbackAfter: fallback,
		stats:         &flowStats{},
		reply: func(b []byte) error {
			mu.Lock()
			replies = append(replies, append([]byte(nil), b...))
			mu.Unlock()
			return nil
		},
		openDirect: func() (io.ReadWriteCloser, error) {
			if direct == nil {
				return nil, errNoSession
			}
			return direct, nil
		},
		openTunnel: func() (io.ReadWriteCloser, error) {
			if tunnel == nil {
				return nil, errNoSession
			}
			return tunnel, nil
		},
	}
	return s, &replies, &mu
}

// The fix itself: a name that isn't on the list resolves directly, so a
// tunnel that has stopped answering no longer takes every name down with it.
func TestUnlistedNameGoesDirectListedNameGoesThroughTunnel(t *testing.T) {
	direct, tunnel := newFakeUpstream(), newFakeUpstream()
	s, _, _ := newTestSplitter(direct, tunnel, "youtube.com", 0)
	defer s.close()

	s.handleQuery(query(1, "example.org"))
	s.handleQuery(query(2, "www.youtube.com"))

	if direct.count() != 1 || tunnel.count() != 1 {
		t.Fatalf("expected one query each way, got direct=%d tunnel=%d", direct.count(), tunnel.count())
	}
	if dnsQueryName(tunnel.writes[0]) != "www.youtube.com" {
		t.Fatalf("the listed name should have gone through the tunnel, got %q", dnsQueryName(tunnel.writes[0]))
	}
}

// A listed name must never be sent to the local network, even with no tunnel
// to send it through: that's how it would come back poisoned, and it would
// tell the network which listed sites are in use.
func TestListedNameNeverLeaksDirect(t *testing.T) {
	direct := newFakeUpstream()
	s, _, _ := newTestSplitter(direct, nil, "youtube.com", 0)
	defer s.close()

	s.handleQuery(query(1, "youtube.com"))
	if direct.count() != 0 {
		t.Fatal("a listed name was sent direct")
	}
}

// Networks that block outside resolvers: a direct query that gets no answer
// is retried through the tunnel instead of failing.
func TestUnansweredDirectQueryFallsBackToTunnel(t *testing.T) {
	direct, tunnel := newFakeUpstream(), newFakeUpstream()
	s, _, _ := newTestSplitter(direct, tunnel, "youtube.com", 40*time.Millisecond)
	defer s.close()

	s.handleQuery(query(9, "example.org"))
	time.Sleep(150 * time.Millisecond)
	if tunnel.count() != 1 {
		t.Fatalf("expected the unanswered direct query to be retried through the tunnel, got %d", tunnel.count())
	}
	if s.stats.dnsDirectFallback.Load() != 1 {
		t.Fatal("the fallback was not counted")
	}
}

// ...but an answered one is not, and the answer reaches the device.
func TestAnsweredDirectQueryDoesNotFallBack(t *testing.T) {
	direct, tunnel := newFakeUpstream(), newFakeUpstream()
	s, replies, mu := newTestSplitter(direct, tunnel, "youtube.com", 60*time.Millisecond)
	defer s.close()

	q := query(5, "example.org")
	s.handleQuery(q)
	answer := append([]byte(nil), q...)
	answer[2] |= 0x80
	direct.answers <- answer
	time.Sleep(150 * time.Millisecond)

	if tunnel.count() != 0 {
		t.Fatal("an answered direct query was retried through the tunnel")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*replies) != 1 {
		t.Fatalf("expected the answer to reach the device, got %d replies", len(*replies))
	}
}

// The placeholder DNS address answers on 53 (rewritten to the real resolver)
// and is refused at once on anything else - Android's Private DNS probe on
// 853 used to hang for the full dial timeout every time the tunnel came up.
func TestPlaceholderDNSRefusedExceptOnPort53(t *testing.T) {
	tun := &Tunnel{}
	if tun.refuseFakeDNS("10.10.0.1:853") {
		t.Fatal("nothing should be refused before a placeholder is configured")
	}
	tun.SetDNSUpstream("10.10.0.1", "1.1.1.1:53")
	if !tun.refuseFakeDNS("10.10.0.1:853") {
		t.Fatal("the DoT probe to the placeholder should be refused")
	}
	if tun.refuseFakeDNS("10.10.0.1:53") {
		t.Fatal("plain DNS to the placeholder must go through")
	}
	if tun.refuseFakeDNS("1.1.1.1:853") {
		t.Fatal("only the placeholder address is refused")
	}
}
