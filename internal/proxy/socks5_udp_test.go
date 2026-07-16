package proxy

import (
	"bytes"
	"testing"
)

func TestParseUDPDatagram(t *testing.T) {
	tests := []struct {
		name       string
		pkt        []byte
		wantTarget string
		wantHdr    []byte
		wantData   []byte
		wantOK     bool
	}{
		{
			name:       "ipv4",
			pkt:        []byte{0, 0, 0, 0x01, 1, 2, 3, 4, 0x00, 0x35, 'd', 'n', 's'},
			wantTarget: "1.2.3.4:53",
			wantHdr:    []byte{0x01, 1, 2, 3, 4, 0x00, 0x35},
			wantData:   []byte("dns"),
			wantOK:     true,
		},
		{
			name:       "domain",
			pkt:        append([]byte{0, 0, 0, 0x03, 0x0b}, append([]byte("example.com"), 0x01, 0xbb, 'h', 'i')...),
			wantTarget: "example.com:443",
			wantHdr:    append([]byte{0x03, 0x0b}, append([]byte("example.com"), 0x01, 0xbb)...),
			wantData:   []byte("hi"),
			wantOK:     true,
		},
		{
			name:       "ipv6",
			pkt:        []byte{0, 0, 0, 0x04, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0x00, 0x35, 'x'},
			wantTarget: "[::1]:53",
			wantHdr:    []byte{0x04, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0x00, 0x35},
			wantData:   []byte("x"),
			wantOK:     true,
		},
		{
			name:   "fragmented rejected",
			pkt:    []byte{0, 0, 0x01, 0x01, 1, 2, 3, 4, 0x00, 0x35},
			wantOK: false,
		},
		{
			name:   "too short",
			pkt:    []byte{0, 0, 0},
			wantOK: false,
		},
		{
			name:   "truncated domain",
			pkt:    []byte{0, 0, 0, 0x03, 0x40, 'a', 'b'},
			wantOK: false,
		},
		{
			name:   "unknown atyp",
			pkt:    []byte{0, 0, 0, 0x09, 1, 2, 3, 4, 0, 0},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, hdr, payload, ok := parseUDPDatagram(tt.pkt)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if target != tt.wantTarget {
				t.Errorf("target = %q, want %q", target, tt.wantTarget)
			}
			if !bytes.Equal(hdr, tt.wantHdr) {
				t.Errorf("hdr = %v, want %v", hdr, tt.wantHdr)
			}
			if !bytes.Equal(payload, tt.wantData) {
				t.Errorf("payload = %q, want %q", payload, tt.wantData)
			}
		})
	}
}
