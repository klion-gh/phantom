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
	cases := [][]byte{
		[]byte(""),
		[]byte("hi"),
		make([]byte, 300),
		make([]byte, 4096),
		make([]byte, 32*1024), // io.Copy's default buffer size - past the largest bucket
	}

	for _, payload := range cases {
		padded, err := PadPlaintext(payload)
		if err != nil {
			t.Fatalf("PadPlaintext() error = %v", err)
		}

		// wire size must not reveal the real size: same-bucket payloads should
		// produce a padded size from the fixed bucket list (or a multiple of
		// the largest bucket beyond that).
		foundBucket := false
		for _, b := range BucketSizes {
			if len(padded) == b {
				foundBucket = true
			}
		}
		largest := BucketSizes[len(BucketSizes)-1]
		if !foundBucket && len(padded)%largest != 0 {
			t.Errorf("padded size %d is neither a bucket nor a multiple of %d", len(padded), largest)
		}

		unpadded, err := UnpadPlaintext(padded)
		if err != nil {
			t.Fatalf("UnpadPlaintext() error = %v", err)
		}
		if !bytes.Equal(unpadded, payload) {
			t.Errorf("round trip mismatch: got %d bytes, want %d bytes", len(unpadded), len(payload))
		}
	}
}

func TestPadPlaintextSameSizeDifferentPayloads(t *testing.T) {
	a, err := PadPlaintext([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := PadPlaintext(make([]byte, 200))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Errorf("expected same bucket size for both small payloads, got %d and %d", len(a), len(b))
	}
}
