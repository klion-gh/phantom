package pingcheck

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"
)

// fixtureYAML is a syntactically valid client config pointed at addr. The
// keys are throwaway - these tests never get as far as a real handshake.
func fixtureYAML(server string) string {
	return "server: \"" + server + "\"\n" +
		"domain: example.com\n" +
		"fingerprint: chrome133\n" +
		"psk: " + strings.Repeat("11", 32) + "\n" +
		"server_public_key: " + strings.Repeat("22", 32) + "\n"
}

// The point of Options.ProtectFD: while a tunnel is up, the socket a ping
// times must be taken out of the VPN, or the ping measures the path through
// the current server instead of the direct one. A local listener that drops
// the connection is enough to see whether the handshake socket went through
// protect before connecting.
func TestPingWithProtectsHandshakeSocket(t *testing.T) {
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

	var protected atomic.Int32
	_, err = PingWith(fixtureYAML(ln.Addr().String()), Options{ProtectFD: func(fd int) bool {
		protected.Add(1)
		return true
	}})
	if err == nil {
		t.Fatal("expected the handshake against a closing listener to fail")
	}
	if protected.Load() == 0 {
		t.Fatal("the handshake socket was dialed without going through ProtectFD")
	}
}

// A failed protect must fail the ping rather than let the socket quietly
// connect through the tunnel anyway - a latency measured on the wrong path is
// worse than none, because it looks trustworthy.
func TestPingWithRefusesWhenProtectFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()

	_, err = PingWith(fixtureYAML(ln.Addr().String()), Options{ProtectFD: func(int) bool { return false }})
	if err == nil {
		t.Fatal("expected a failed protect to fail the ping")
	}
	if accepted.Load() != 0 {
		t.Fatal("the socket connected even though it could not be protected")
	}
}

// A server named by IP must not go near any resolver: the literal is used as-is.
func TestProtectedResolverPassesLiteralIPThrough(t *testing.T) {
	r := &protectedResolver{protect: func(int) bool {
		t.Fatal("a literal IP should not open any socket")
		return false
	}}
	ips, err := r.LookupIP(t.Context(), "ip4", "203.0.113.7")
	if err != nil || len(ips) != 1 || ips[0].String() != "203.0.113.7" {
		t.Fatalf("got %v, %v", ips, err)
	}
}
