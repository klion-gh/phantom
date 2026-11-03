package tunnel

import (
	"math/rand/v2"
	"time"

	"phantom/internal/protocol"
)

// Shaping: how much padding each frame gets.
//
// What a tunnel record gives away is mostly in the first few kilobytes of
// each stream it carries. A page opening a connection through the tunnel
// sends a TLS ClientHello (500-1800 bytes) and gets a ServerHello and
// certificate chain back (3-6 KB) - inner handshake sizes that show through
// the outer TLS and are what TLS-in-TLS classifiers key on. After that, data
// is data, and bulk transfers leave as full 16 KB records like any other.
//
// So frames in a stream's opening phase are drawn towards the sizes a
// browser's own records actually have at the start of a connection, measured
// with tools/flowlab (an Edge profile over 20 sites, 3 November 2026): a
// target record size is sampled from that distribution, the frame is padded
// up to it if it is smaller, and a random extra of up to a quarter of its
// length (at most openingJitterMax) blurs the exact size of whatever it
// carries. A frame can only be padded, never shrunk, so the many tiny browser
// records (HTTP/2 control frames, under 60 bytes) stay out of reach - but no
// longer is every record inflated past 300 bytes, as the old fixed buckets
// did. After the opening phase a frame gets at most tailPadMax bytes.
//
// The direction comes from the stream, not from which end is the client:
// frames of the side that opened the stream are requests, the other side's
// are responses.

// requestRecordSizes and responseRecordSizes are the 0th, 5th, ... 100th
// percentiles of TLS record lengths (as the record header gives them) in the
// first 30 application-data records of a browser's connections, requests
// (upstream) and responses (downstream).
var requestRecordSizes = [...]int{23, 26, 26, 30, 30, 33, 37, 41, 41, 50, 50, 57, 77, 89, 108, 154, 361, 501, 658, 907, 11872}
var responseRecordSizes = [...]int{19, 26, 26, 37, 57, 60, 140, 266, 298, 345, 487, 808, 1386, 1395, 1425, 3713, 5326, 8210, 16401, 16401, 16408}

const (
	// shapeBytes is a stream's opening phase, per direction: enough to cover
	// the inner TLS handshake and the first request and response.
	shapeBytes = 8 << 10
	// openingJitterMax caps the random extra in the opening phase.
	openingJitterMax = 1024
	// tailPadMax is the most padding a frame gets after the opening phase.
	tailPadMax = 32
	// recordOverhead is what TLS 1.3 adds to a record's content in the
	// length its header gives: the inner content type byte and the AEAD tag.
	recordOverhead = 1 + 16
	// openHoldback is how long a new stream's OPEN waits for data to ride
	// with before going out alone (see Multiplexer.openStream).
	openHoldback = 25 * time.Millisecond
)

// padFor returns how many padding bytes a frame carrying n plaintext bytes
// gets. request: the frame belongs to the side that opened its stream.
// opening: the stream is still in its opening phase in this direction.
func padFor(n int, request, opening bool) int {
	if !opening {
		return rand.IntN(tailPadMax + 1)
	}
	sizes := responseRecordSizes[:]
	if request {
		sizes = requestRecordSizes[:]
	}
	pad := sampleSize(sizes) - recordOverhead - protocol.SealedFrameOverhead - n
	if pad < 0 {
		pad = 0
	}
	jitter := n / 4
	if jitter > openingJitterMax {
		jitter = openingJitterMax
	}
	return pad + rand.IntN(jitter+1)
}

// sampleSize draws a size from a distribution given as evenly spaced
// percentiles, interpolating between neighbours.
func sampleSize(q []int) int {
	r := rand.Float64() * float64(len(q)-1)
	i := int(r)
	if i >= len(q)-1 {
		return q[len(q)-1]
	}
	return q[i] + int((r-float64(i))*float64(q[i+1]-q[i]))
}
