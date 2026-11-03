package protocol

import (
	"bytes"
	"testing"
)

func TestFrameEncodeDecode(t *testing.T) {
	tests := []struct {
		name    string
		frame   *Frame
		wantErr bool
	}{
		{
			name: "data frame",
			frame: &Frame{
				Type:     FrameData,
				StreamID: 1,
				Flags:    0,
				Payload:  []byte("hello world"),
			},
		},
		{
			name: "open frame",
			frame: &Frame{
				Type:     FrameOpen,
				StreamID: 3,
				Flags:    0,
				Payload:  []byte("example.com:443"),
			},
		},
		{
			name: "close frame",
			frame: &Frame{
				Type:     FrameClose,
				StreamID: 5,
				Flags:    0,
				Payload:  []byte{0x00, 0x00},
			},
		},
		{
			name: "udp open frame",
			frame: &Frame{
				Type:     FrameOpen,
				StreamID: 7,
				Flags:    FlagUDP,
				Payload:  []byte("8.8.8.8:53"),
			},
		},
		{
			name: "empty payload",
			frame: &Frame{
				Type:     FramePing,
				StreamID: 0,
				Flags:    0,
				Payload:  []byte{},
			},
		},
		{
			name: "padding frame",
			frame: &Frame{
				Type:     FramePadding,
				StreamID: 0,
				Flags:    0,
				Payload:  []byte{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := tt.frame.Encode()
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}

			if len(encoded) == 0 {
				t.Fatal("Encode() returned empty data")
			}

			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}

			if decoded.Type != tt.frame.Type {
				t.Errorf("Type = %v, want %v", decoded.Type, tt.frame.Type)
			}
			if decoded.StreamID != tt.frame.StreamID {
				t.Errorf("StreamID = %v, want %v", decoded.StreamID, tt.frame.StreamID)
			}
			if !bytes.Equal(decoded.Payload, tt.frame.Payload) {
				t.Errorf("Payload = %v, want %v", decoded.Payload, tt.frame.Payload)
			}
		})
	}
}

func TestFrameEncodeSize(t *testing.T) {
	f := &Frame{
		Type:     FrameData,
		StreamID: 1,
		Payload:  make([]byte, 100),
	}

	encoded, err := f.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	expected := FrameHeaderSize + len(f.Payload)
	if len(encoded) != expected {
		t.Errorf("Encoded size = %d, want %d", len(encoded), expected)
	}
}

func TestFrameDecodeTooShort(t *testing.T) {
	_, err := Decode([]byte{0x00, 0x01})
	if err == nil {
		t.Error("Decode() should fail on too-short data")
	}
}

func TestPadUnpadPlaintext(t *testing.T) {
	cases := []struct {
		payload []byte
		pad     int
	}{
		{[]byte(""), 0},
		{[]byte("hi"), 5},
		{make([]byte, 300), 900},
		{make([]byte, 4096), 0},
		{make([]byte, 32*1024), 31},
		{[]byte("negative pad means none"), -7},
	}
	for _, tc := range cases {
		padded, err := PadPlaintext(tc.payload, tc.pad)
		if err != nil {
			t.Fatalf("PadPlaintext() error = %v", err)
		}
		pad := tc.pad
		if pad < 0 {
			pad = 0
		}
		if want := lengthPrefixSize + len(tc.payload) + pad; len(padded) != want {
			t.Errorf("payload %d pad %d: padded to %d, want %d", len(tc.payload), tc.pad, len(padded), want)
		}
		unpadded, err := UnpadPlaintext(padded)
		if err != nil {
			t.Fatalf("UnpadPlaintext() error = %v", err)
		}
		if !bytes.Equal(unpadded, tc.payload) {
			t.Errorf("round trip mismatch: got %d bytes, want %d bytes", len(unpadded), len(tc.payload))
		}
	}
}

// Padding is clamped so the sealed frame still fits: a sender asking for more
// than the frame has room for gets as much as fits, never an overflow.
func TestPadPlaintextClampsToTheFrame(t *testing.T) {
	padded, err := PadPlaintext(make([]byte, DataChunkSize), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(padded) != maxPaddedPlaintext {
		t.Fatalf("padded to %d, want the ceiling %d", len(padded), maxPaddedPlaintext)
	}
}
