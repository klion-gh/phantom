package main

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"phantom/internal/pingcheck"
)

// While a tunnel is up, pingOptions pins every ping socket to the physical
// interface with IP_UNICAST_IF. This checks the mechanism end to end on real
// sockets: with an interface index set, the ping's handshake socket must
// still connect (the setsockopt succeeded on the socket Go's dialer created)
// rather than fail protection - and with none set, no binding is attempted.
func TestPingOptionsBindsToInterfaceAndStillConnects(t *testing.T) {
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

	ifIndex, err := bestInterfaceIndex("127.0.0.1")
	if err != nil {
		t.Skipf("no loopback interface index on this machine: %v", err)
	}
	setPingInterface(ifIndex)
	defer setPingInterface(0)

	if pingOptions().ProtectFD == nil {
		t.Fatal("with an interface set, pings must carry a ProtectFD")
	}

	yaml := "server: \"" + ln.Addr().String() + "\"\n" +
		"domain: example.com\n" +
		"fingerprint: chrome133\n" +
		"psk: " + strings.Repeat("11", 32) + "\n" +
		"server_public_key: " + strings.Repeat("22", 32) + "\n"

	_, err = pingcheck.PingWith(yaml, pingOptions())
	// The listener drops the connection, so the handshake fails - what
	// matters is that the TCP connect happened on the bound socket.
	if err != nil && strings.Contains(err.Error(), "protect") {
		t.Fatalf("binding the ping socket to interface %d failed: %v", ifIndex, err)
	}
	if accepted.Load() == 0 {
		t.Fatalf("the bound ping socket never connected (err: %v)", err)
	}

	setPingInterface(0)
	if pingOptions().ProtectFD != nil {
		t.Fatal("with no tunnel up, pings must use the plain default route")
	}
}
