package netstack

import (
	"encoding/binary"
	"testing"
)

func TestDNSNXDomainEchoesQuestion(t *testing.T) {
	// id 0xabcd, RD, one question: ads.example.com A IN
	q := []byte{0xab, 0xcd, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	q = append(q, 3, 'a', 'd', 's', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1)
	r := dnsNXDomain(q)
	if r == nil || len(r) != len(q) {
		t.Fatalf("bad answer: %v", r)
	}
	if r[0] != 0xab || r[1] != 0xcd {
		t.Fatal("id not echoed")
	}
	flags := binary.BigEndian.Uint16(r[2:4])
	if flags&0x8000 == 0 || flags&0x000f != 3 || flags&0x0100 == 0 {
		t.Fatalf("flags %04x", flags)
	}
	if dnsQueryName(r) != "" {
		t.Fatal("answer still reads as a query")
	}
}
