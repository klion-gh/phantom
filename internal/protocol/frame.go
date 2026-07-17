package protocol

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
)

type FrameType uint8

// Session authentication happens out of band during the disguised
// HTTP/WS-upgrade handshake (internal/handshake, see PROTOCOL.md), before the
// Multiplexer is even constructed - so there is no in-band auth frame type.
// The first bytes on the wire are ordinary application frames, which avoids
// the "fixed-size frame right after the TLS handshake" signature v1 had.
const (
	FrameData     FrameType = 0x00
	FrameOpen     FrameType = 0x01
	FrameClose    FrameType = 0x02
	FramePing     FrameType = 0x03
	FrameSettings FrameType = 0x04
	FramePadding  FrameType = 0x05
)

type Flags uint8

const (
	FlagUDP Flags = 0x04
)

// BucketSizes are the fixed sizes DATA frame plaintexts get padded to before
// encryption, so the wire size of a frame doesn't leak the real payload size.
// Chosen to cover typical small control-ish writes up to a full TCP/UDP MTU-ish
// chunk; anything bigger (e.g. io.Copy's default 32KB buffer) is rounded up to
// the next multiple of the largest bucket instead of being sent unpadded.
var BucketSizes = []int{256, 512, 1024, 2048, 4096}

// maxPadJitter is the random extra added on top of the bucket floor (see
// chooseSize). Bucketing alone made every DATA frame's wire size exactly one of
// a few discrete values - itself a distinguisher, since real HTTPS record sizes
// are continuously distributed. The jitter is larger than the smallest
// inter-bucket gap (256) so adjacent low buckets' jittered ranges overlap and a
// given observed size no longer maps back to a single bucket, and it's
// byte-granular (not quantized) so the size carries no alignment tell. Bounded
// so overhead stays small.
const maxPadJitter = 512

// maxPaddedPlaintext is the largest padded plaintext that still fits a single
// frame once encrypted: the encrypted frame body is nonce(24) + padded +
// poly1305 tag(16), and the frame header's length field is a uint16, so
// padded + 40 must be <= 65535. Padding is clamped to this; a payload already
// at/above it (only near-max-size UDP datagrams) simply can't be padded.
const maxPaddedPlaintext = 65535 - (24 + 16)

const FrameHeaderSize = 6

// lengthPrefixSize is the 2-byte real-length prefix inside a padded DATA frame
// plaintext: [2-byte real length][real payload][random padding].
const lengthPrefixSize = 2

type Frame struct {
	Type     FrameType
	StreamID uint16
	Flags    Flags
	Payload  []byte
}

func (f *Frame) Encode() ([]byte, error) {
	totalLen := FrameHeaderSize + len(f.Payload)
	buf := make([]byte, totalLen)

	buf[0] = byte(f.Type)
	binary.BigEndian.PutUint16(buf[1:3], f.StreamID)
	buf[3] = byte(f.Flags)
	binary.BigEndian.PutUint16(buf[4:6], uint16(len(f.Payload)))

	copy(buf[FrameHeaderSize:], f.Payload)

	return buf, nil
}

func Decode(data []byte) (*Frame, error) {
	if len(data) < FrameHeaderSize {
		return nil, errors.New("frame too short")
	}

	payloadLen := binary.BigEndian.Uint16(data[4:6])
	if int(payloadLen)+FrameHeaderSize > len(data) {
		return nil, errors.New("payload length exceeds data")
	}

	f := &Frame{
		Type:     FrameType(data[0]),
		StreamID: binary.BigEndian.Uint16(data[1:3]),
		Flags:    Flags(data[3]),
		Payload:  make([]byte, payloadLen),
	}
	copy(f.Payload, data[FrameHeaderSize:FrameHeaderSize+payloadLen])

	return f, nil
}

// PadPlaintext wraps a real DATA frame payload as
// [2-byte real length][real payload][random padding] sized to hit one of
// BucketSizes (or, past the largest bucket, the next multiple of it), so the
// encrypted frame's wire size doesn't reveal the real payload size.
func PadPlaintext(payload []byte) ([]byte, error) {
	total := lengthPrefixSize + len(payload)
	size, err := chooseSize(total)
	if err != nil {
		return nil, err
	}

	padded := make([]byte, size)
	binary.BigEndian.PutUint16(padded[0:lengthPrefixSize], uint16(len(payload)))
	copy(padded[lengthPrefixSize:total], payload)

	if size > total {
		if _, err := rand.Read(padded[total:]); err != nil {
			return nil, err
		}
	}

	return padded, nil
}

// UnpadPlaintext reverses PadPlaintext.
func UnpadPlaintext(padded []byte) ([]byte, error) {
	if len(padded) < lengthPrefixSize {
		return nil, errors.New("padded plaintext too short")
	}
	realLen := int(binary.BigEndian.Uint16(padded[0:lengthPrefixSize]))
	if lengthPrefixSize+realLen > len(padded) {
		return nil, errors.New("real length exceeds padded plaintext")
	}
	return padded[lengthPrefixSize : lengthPrefixSize+realLen], nil
}

func chooseBucket(n int) int {
	for _, b := range BucketSizes {
		if n <= b {
			return b
		}
	}
	largest := BucketSizes[len(BucketSizes)-1]
	return ((n + largest - 1) / largest) * largest
}

// chooseSize is chooseBucket (the size floor that hides a frame's magnitude)
// plus a random jitter (that breaks the discrete-bucket fingerprint - see
// maxPadJitter), clamped so the padded plaintext plus AEAD overhead still fits
// one frame's 16-bit length (see maxPaddedPlaintext). A payload already at or
// past that ceiling (only near-max-size UDP datagrams) can't be padded and is
// returned as-is.
func chooseSize(n int) (int, error) {
	base := chooseBucket(n)

	room := maxPaddedPlaintext - base
	if room <= 0 {
		return n, nil
	}
	maxJ := maxPadJitter
	if room < maxJ {
		maxJ = room
	}

	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	jitter := int(binary.BigEndian.Uint16(b[:])) % (maxJ + 1)
	return base + jitter, nil
}
