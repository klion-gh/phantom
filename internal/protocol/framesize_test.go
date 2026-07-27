package protocol

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// TestLargestBucketMatchesBucketSizes pins the constant the padding-headroom
// arithmetic is built on to the actual bucket list, so growing BucketSizes can't
// quietly invalidate DataChunkSize.
func TestLargestBucketMatchesBucketSizes(t *testing.T) {
	if got := BucketSizes[len(BucketSizes)-1]; got != largestBucket {
		t.Fatalf("largestBucket = %d, but BucketSizes ends at %d", largestBucket, got)
	}
}

// TestSealedDataFrameAlwaysFitsOneFrame is the regression test for the header
// length overflow: for every interesting plaintext size up to MaxDataPlaintext,
// the sealed frame body must fit MaxFramePayload, and Encode must accept it.
// Sizes at and just below the limit are exactly where a 65536-byte body used to
// be announced as length 0.
func TestSealedDataFrameAlwaysFitsOneFrame(t *testing.T) {
	sizes := []int{0, 1, 255, 256, 257, 4095, 4096, 4097, 32 * 1024}
	for _, b := range BucketSizes {
		sizes = append(sizes, b-1, b, b+1)
	}
	sizes = append(sizes,
		DataChunkSize-1, DataChunkSize,
		DataChunkSize+1, // first size that can no longer be padded
		MaxDataPlaintext-1, MaxDataPlaintext,
	)

	sc := &SessionCrypto{}
	for _, n := range sizes {
		if n < 0 || n > MaxDataPlaintext {
			continue
		}
		plaintext := make([]byte, n)
		if _, err := rand.Read(plaintext); err != nil {
			t.Fatal(err)
		}

		body, err := sc.EncryptFrame([]byte{0, 1}, plaintext)
		if err != nil {
			t.Fatalf("EncryptFrame(%d bytes): %v", n, err)
		}
		if len(body) > MaxFramePayload {
			t.Fatalf("plaintext %d sealed to %d bytes, over the %d frame limit",
				n, len(body), MaxFramePayload)
		}

		f := &Frame{Type: FrameData, StreamID: 1, Payload: body}
		encoded, err := f.Encode()
		if err != nil {
			t.Fatalf("Encode(%d-byte plaintext, %d-byte body): %v", n, len(body), err)
		}

		// The decoded length must match what was written - the overflow's symptom
		// was a header that announced a different (wrapped) length than the body.
		decoded, err := Decode(encoded)
		if err != nil {
			t.Fatalf("Decode(%d-byte plaintext): %v", n, err)
		}
		if !bytes.Equal(decoded.Payload, body) {
			t.Fatalf("plaintext %d: round-tripped body differs (%d vs %d bytes)",
				n, len(decoded.Payload), len(body))
		}
	}
}

// TestDataChunkSizeIsAlwaysPadded asserts the reason DataChunkSize is lower than
// MaxDataPlaintext: at or below it, padding always actually happens.
func TestDataChunkSizeIsAlwaysPadded(t *testing.T) {
	for _, n := range []int{DataChunkSize - 1, DataChunkSize} {
		padded, err := PadPlaintext(make([]byte, n))
		if err != nil {
			t.Fatalf("PadPlaintext(%d): %v", n, err)
		}
		if len(padded) <= lengthPrefixSize+n {
			t.Fatalf("plaintext %d was not padded (padded to %d)", n, len(padded))
		}
	}
}

func TestEncodeRejectsOversizedPayload(t *testing.T) {
	f := &Frame{Type: FrameData, StreamID: 1, Payload: make([]byte, MaxFramePayload+1)}
	if _, err := f.Encode(); err == nil {
		t.Fatal("Encode accepted a payload past MaxFramePayload; it would have wrapped the uint16 length")
	}
}

// TestDecodeMaxLengthDoesNotPanic covers the uint16 wrap in Decode's copy bounds:
// a frame declaring a length of 65530 or more used to slice data[6:5] and panic,
// killing the process. Any frame header off the wire could trigger it.
func TestDecodeMaxLengthDoesNotPanic(t *testing.T) {
	for _, payloadLen := range []int{65529, 65530, 65534, MaxFramePayload} {
		frame := make([]byte, FrameHeaderSize+payloadLen)
		frame[0] = byte(FrameData)
		frame[4] = byte(payloadLen >> 8)
		frame[5] = byte(payloadLen)

		decoded, err := Decode(frame)
		if err != nil {
			t.Fatalf("Decode(payloadLen=%d): %v", payloadLen, err)
		}
		if len(decoded.Payload) != payloadLen {
			t.Fatalf("payloadLen=%d: decoded %d bytes", payloadLen, len(decoded.Payload))
		}
	}

	// A header that over-claims relative to the buffer must still be rejected
	// cleanly rather than panicking.
	short := make([]byte, FrameHeaderSize+10)
	short[4], short[5] = 0xff, 0xff
	if _, err := Decode(short); err == nil {
		t.Fatal("Decode accepted a header claiming more payload than the buffer holds")
	}
}

func TestPadPlaintextRejectsOversizedPlaintext(t *testing.T) {
	if _, err := PadPlaintext(make([]byte, MaxDataPlaintext+1)); err == nil {
		t.Fatal("PadPlaintext accepted a plaintext past MaxDataPlaintext")
	}
}
