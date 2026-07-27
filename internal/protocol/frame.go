package protocol

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
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

// IsEncrypted reports whether this frame type's payload is sealed with the
// session keys.
//
// FrameOpen joined FrameData in WireVersion 2. Its payload is the destination
// host:port, and leaving it in the clear meant the inner encryption layer
// protected the traffic while publishing where that traffic was going - so
// anything that terminated the outer TLS (a corporate middlebox with its root
// installed, a compromised certificate key) recovered a full browsing history
// even though it couldn't read a byte of content.
//
// The remaining types carry no payload worth hiding: CLOSE and SETTINGS are
// empty, PING is a fixed token, PADDING is random bytes by definition.
func (t FrameType) IsEncrypted() bool {
	return t == FrameData || t == FrameOpen
}

// FrameAAD builds the additional authenticated data a frame's ciphertext is bound
// to: its type, flags and stream id - every header field except the length, which
// is only known after sealing.
//
// Previously only the stream id was covered, which left type and flags
// unauthenticated: an attacker able to modify the byte stream could flip
// FlagUDP, or relabel an OPEN as a DATA, and the AEAD would still verify. The
// outer TLS makes that unreachable today; this closes it at the layer that
// claims to authenticate the frame.
func FrameAAD(t FrameType, flags Flags, streamID uint16) []byte {
	aad := make([]byte, 4)
	aad[0] = byte(t)
	aad[1] = byte(flags)
	binary.BigEndian.PutUint16(aad[2:4], streamID)
	return aad
}

// BucketSizes are the fixed sizes DATA frame plaintexts get padded to before
// encryption, so the wire size of a frame doesn't leak the real payload size.
// Chosen to cover typical small control-ish writes up to a full TCP/UDP MTU-ish
// chunk; anything bigger (e.g. io.Copy's default 32KB buffer) is rounded up to
// the next multiple of the largest bucket instead of being sent unpadded.
var BucketSizes = []int{256, 512, 1024, 2048, 4096}

// largestBucket must stay equal to the last element of BucketSizes; it exists
// separately only because the padding-headroom arithmetic below needs to be
// constant-expressible (BucketSizes is a slice and can't be indexed in a const).
// TestLargestBucketMatchesBucketSizes pins the two together.
const largestBucket = 4096

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
const maxPaddedPlaintext = MaxFramePayload - (24 + 16)

const FrameHeaderSize = 6

// lengthPrefixSize is the 2-byte real-length prefix inside a padded DATA frame
// plaintext: [2-byte real length][real payload][random padding].
const lengthPrefixSize = 2

// MaxFramePayload is the largest payload one frame can carry, because Encode
// writes its length into a uint16 header field. Exceeding it used to wrap
// silently: a 65536-byte body was announced as length 0, and the peer then
// parsed the body itself as a stream of frame headers - unrecoverable
// desynchronisation of every stream sharing that connection, from one oversized
// write. Encode now refuses instead, and the two limits below keep callers from
// ever getting there.
const MaxFramePayload = 65535

// MaxDataPlaintext is the largest DATA-frame plaintext that still fits one frame
// once sealed, even with no padding at all: the frame body is
// [2-byte real length][plaintext] + nonce(24) + poly1305 tag(16).
const MaxDataPlaintext = MaxFramePayload - (lengthPrefixSize + 24 + 16)

// DataChunkSize is the largest plaintext that can still be *fully* padded - i.e.
// where chooseSize's bucket floor leaves room for an unclamped jitter draw. It's
// the size byte-stream writes are split into (see tunnel.Stream.Write), so every
// DATA frame on a TCP stream is padded, rather than the largest ones silently
// falling through unpadded and leaking their exact length.
//
// MaxDataPlaintext is deliberately the looser limit of the two: it's what an
// indivisible UDP datagram is checked against, so datagrams between the two
// limits still go through (unpadded, as before) instead of being dropped.
const DataChunkSize = ((maxPaddedPlaintext - maxPadJitter) / largestBucket) * largestBucket - lengthPrefixSize

type Frame struct {
	Type     FrameType
	StreamID uint16
	Flags    Flags
	Payload  []byte
}

func (f *Frame) Encode() ([]byte, error) {
	if len(f.Payload) > MaxFramePayload {
		return nil, fmt.Errorf("frame payload %d exceeds maximum %d", len(f.Payload), MaxFramePayload)
	}

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

	// int(), not uint16 arithmetic: FrameHeaderSize+payloadLen used to be computed
	// in uint16 (payloadLen's type), so any frame declaring a length of 65530 or
	// more wrapped past zero and produced a backwards slice - data[6:5] - which
	// panics and kills the whole process. The bounds check above escaped it by
	// converting to int, the copy below did not. Reachable from readLoop with
	// nothing but a frame header claiming a large length, so it was a remote
	// crash of the server for every connected user at once.
	payloadLen := int(binary.BigEndian.Uint16(data[4:6]))
	if payloadLen+FrameHeaderSize > len(data) {
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
	if len(payload) > MaxDataPlaintext {
		return nil, fmt.Errorf("data plaintext %d exceeds maximum %d", len(payload), MaxDataPlaintext)
	}

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
// one frame's 16-bit length (see maxPaddedPlaintext).
//
// A plaintext whose bucket floor is already past that ceiling can't be padded at
// all and is returned as-is. Callers keep byte streams below DataChunkSize so
// that never happens to them; the only inputs that still land here are single
// UDP datagrams in the band between DataChunkSize and MaxDataPlaintext, which
// can't be split. Those go out unpadded (leaking their own length, as they
// always did) - what they no longer do is overflow the frame header, since
// PadPlaintext rejects anything past MaxDataPlaintext outright.
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
