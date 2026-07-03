package protocol

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
)

type FrameType uint8

// FrameAuth is kept only because internal/tunnel/multiplexer.go (ported
// unchanged from v1) still has an in-band auth code path; v2 never actually
// exercises it - the real session authentication now happens before the
// Multiplexer is even constructed, during the disguised HTTP/WS-upgrade
// handshake in internal/handshake (see PROTOCOL.md). Both sides construct
// their Multiplexer with sendAuth=false/expectAuth=false, so no FrameAuth is
// ever sent - this avoids reintroducing the "fixed-size frame right after the
// TLS handshake" signature that v1 had.
const (
	FrameData     FrameType = 0x00
	FrameOpen     FrameType = 0x01
	FrameClose    FrameType = 0x02
	FramePing     FrameType = 0x03
	FrameSettings FrameType = 0x04
	FramePadding  FrameType = 0x05
	FrameAuth     FrameType = 0x06
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
	bucket := chooseBucket(total)

	padded := make([]byte, bucket)
	binary.BigEndian.PutUint16(padded[0:lengthPrefixSize], uint16(len(payload)))
	copy(padded[lengthPrefixSize:total], payload)

	if bucket > total {
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
