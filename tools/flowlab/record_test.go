package main

import "testing"

// Records split across chunks at every position still come out whole.
func TestRecordParserAcrossChunks(t *testing.T) {
	stream := []byte{22, 3, 1, 0, 2, 0xAA, 0xBB, 23, 3, 3, 0, 3, 1, 2, 3, 23, 3, 3, 0, 1, 9}
	for split := 1; split < len(stream); split++ {
		var got []int
		var rp recordParser
		emit := func(typ byte, n int) { got = append(got, int(typ)*1000+n) }
		rp.feed(stream[:split], emit)
		rp.feed(stream[split:], emit)
		want := []int{22002, 23003, 23001}
		if len(got) != len(want) {
			t.Fatalf("split %d: got %v, want %v", split, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("split %d: got %v, want %v", split, got, want)
			}
		}
	}
}

// Anything that isn't TLS stops the parser instead of producing nonsense.
func TestRecordParserStopsOnNonTLS(t *testing.T) {
	var rp recordParser
	n := 0
	rp.feed([]byte("GET / HTTP/1.1\r\n"), func(byte, int) { n++ })
	if n != 0 || !rp.broken {
		t.Fatalf("parsed %d records from plain HTTP, broken=%v", n, rp.broken)
	}
}
