package netstack

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// A site added to the list must stop going direct over connections the
// browser already had open to it - those are closed so the browser opens
// fresh ones, which are then routed through the tunnel. Connections to
// anything still unlisted are left alone.
func TestResetMisroutedFlowsClosesOnlyNewlyListedDirectFlows(t *testing.T) {
	var listed atomic.Value
	listed.Store("")
	tun := &Tunnel{}
	servers := map[string]net.Conn{}
	tun.SetRouting(
		func(network, target string) RouteDecision {
			if target == listed.Load().(string) {
				return RouteTunnel
			}
			return RouteDirect
		},
		func(network, target string) (io.ReadWriteCloser, error) {
			c1, c2 := net.Pipe()
			servers[target] = c2
			return c1, nil
		},
	)

	site := tun.openRemote("tcp", "203.0.113.10:443")
	other := tun.openRemote("tcp", "198.51.100.7:443")
	if site == nil || other == nil {
		t.Fatal("direct flows did not open")
	}
	if n := tun.ResetMisroutedFlows(); n != 0 {
		t.Fatalf("nothing is misrouted yet, but %d flows were reset", n)
	}

	listed.Store("203.0.113.10:443") // the user adds the site
	if n := tun.ResetMisroutedFlows(); n != 1 {
		t.Fatalf("expected the one flow to the newly listed site to be reset, got %d", n)
	}
	assertClosed(t, servers["203.0.113.10:443"], true)
	assertClosed(t, servers["198.51.100.7:443"], false)

	// A flow closed on its own no longer counts.
	other.Close()
	listed.Store("198.51.100.7:443")
	if n := tun.ResetMisroutedFlows(); n != 0 {
		t.Fatalf("a closed flow was reset again: %d", n)
	}
}

func assertClosed(t *testing.T, server net.Conn, want bool) {
	t.Helper()
	server.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err := server.Read(make([]byte, 1))
	closed := err == io.EOF
	if closed != want {
		t.Fatalf("connection closed=%v, want %v (err %v)", closed, want, err)
	}
}
