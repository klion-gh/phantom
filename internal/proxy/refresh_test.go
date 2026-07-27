package proxy

import (
	"net"
	"testing"
	"time"

	"phantom/internal/protocol"
	"phantom/internal/tunnel"
)

// deadSession builds a session whose multiplexer is already closed, i.e. exactly
// what a proxy is left holding after its connection drops.
func deadSession(t *testing.T) *tunnel.Session {
	t.Helper()
	s := liveSession(t)
	s.Close()
	return s
}

func liveSession(t *testing.T) *tunnel.Session {
	t.Helper()
	c1, c2 := net.Pipe()
	t.Cleanup(func() { c1.Close(); c2.Close() })

	crypto, err := protocol.DeriveSessionKeys(
		[]byte("0123456789abcdef0123456789abcdef"),
		[]byte("shared-psk-bytes-1234567890abcdef"),
		[]byte("client-ephemeral-pub-1234567890ab"),
		[]byte("server-static-pub-1234567890abcd"),
		protocol.RoleClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	return tunnel.NewSessionFromMux(tunnel.NewMultiplexer(c1, crypto))
}

// TestHTTPProxyRecoversFromDeadSession covers the gap that left the desktop
// client permanently broken after a dropped connection: the CONNECT proxy held
// one session forever, so every request answered 502 for the rest of the
// process's life even though the pool had already redialed.
func TestHTTPProxyRecoversFromDeadSession(t *testing.T) {
	dead := deadSession(t)
	srv := NewHTTPProxyServer("127.0.0.1:0", dead)

	if got := srv.currentSession(); got != dead {
		t.Fatal("without a refresher the proxy should keep its original session")
	}

	fresh := liveSession(t)
	t.Cleanup(func() { fresh.Close() })

	calls := 0
	srv.SetSessionRefresher(func() (*tunnel.Session, error) {
		calls++
		return fresh, nil
	})

	if got := srv.currentSession(); got != fresh {
		t.Fatal("proxy did not swap in the fresh session after the old one died")
	}
	if calls != 1 {
		t.Fatalf("refresher called %d times, want 1", calls)
	}

	// A live session must be reused as-is, without consulting the refresher.
	if got := srv.currentSession(); got != fresh || calls != 1 {
		t.Fatalf("live session was not reused (session swapped=%v, calls=%d)", got != fresh, calls)
	}
}

// TestHTTPProxyRefreshCooldown pins the throttle: a server that stays unreachable
// must not turn every single request into its own redial attempt.
func TestHTTPProxyRefreshCooldown(t *testing.T) {
	srv := NewHTTPProxyServer("127.0.0.1:0", deadSession(t))

	calls := 0
	srv.SetSessionRefresher(func() (*tunnel.Session, error) {
		calls++
		return nil, net.UnknownNetworkError("still down")
	})

	for i := 0; i < 5; i++ {
		srv.currentSession()
	}
	if calls != 1 {
		t.Fatalf("refresher called %d times within the cooldown, want 1", calls)
	}

	// Past the cooldown it may try again.
	srv.sessionMu.Lock()
	srv.lastRefreshAttempt = time.Now().Add(-sessionRefreshCooldown - time.Second)
	srv.sessionMu.Unlock()

	srv.currentSession()
	if calls != 2 {
		t.Fatalf("refresher called %d times after the cooldown elapsed, want 2", calls)
	}
}

// TestSOCKS5RecoversFromDeadSession is the same check for the SOCKS5 side, whose
// refresher existed but was never installed by cmd/client.
func TestSOCKS5RecoversFromDeadSession(t *testing.T) {
	srv := NewSOCKS5Server("127.0.0.1:0", deadSession(t))

	fresh := liveSession(t)
	t.Cleanup(func() { fresh.Close() })

	srv.SetSessionRefresher(func() (*tunnel.Session, error) { return fresh, nil })

	if got := srv.currentSession(); got != fresh {
		t.Fatal("SOCKS5 server did not swap in the fresh session after the old one died")
	}
}
