package pingcheck

import (
	"net"
	"testing"
)

func TestPingRelay(t *testing.T) {
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
	r, err := PingRelay(ln.Addr().String(), Options{})
	if err != nil || r.IP != "127.0.0.1" {
		t.Fatalf("PingRelay = %+v, %v", r, err)
	}

	addr := ln.Addr().String()
	ln.Close()
	if _, err := PingRelay(addr, Options{}); err == nil {
		t.Fatal("a closed port answered")
	}
}
