package tunnel

import (
	"errors"
	"io"
	"sync"
	"time"

	"phantom/internal/protocol"
)

type Stream struct {
	id      uint16
	mux     *Multiplexer
	target  string
	readBuf []byte
	readCh  chan []byte
	closeCh chan struct{}
	closed  bool
	// closeErr is why this stream ended: nil for an ordinary close (the peer sent
	// CLOSE, or we did), non-nil when the whole session died under it. Read
	// reports it once the buffered data is drained, so an interrupted transfer
	// surfaces as an error instead of a clean io.EOF that io.Copy would report as
	// success.
	closeErr error
	isUDP    bool
	mu       sync.Mutex
}

func newStream(id uint16, mux *Multiplexer, target string) *Stream {
	return &Stream{
		id:      id,
		mux:     mux,
		target:  target,
		readCh:  make(chan []byte, 64),
		closeCh: make(chan struct{}),
	}
}

func (s *Stream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if len(s.readBuf) > 0 {
		n := copy(p, s.readBuf)
		s.readBuf = s.readBuf[n:]
		s.mu.Unlock()
		return n, nil
	}
	s.mu.Unlock()

	select {
	case data := <-s.readCh:
		n := copy(p, data)
		if n < len(data) {
			s.mu.Lock()
			s.readBuf = append(s.readBuf, data[n:]...)
			s.mu.Unlock()
		}
		return n, nil
	case <-s.closeCh:
		select {
		case data := <-s.readCh:
			n := copy(p, data)
			if n < len(data) {
				s.mu.Lock()
				s.readBuf = append(s.readBuf, data[n:]...)
				s.mu.Unlock()
			}
			return n, nil
		default:
			return 0, s.endErr()
		}
	}
}

// endErr is what Read reports once the stream is closed and drained: io.EOF for
// an ordinary close, or the session's error if the connection died under it.
func (s *Stream) endErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeErr != nil {
		return s.closeErr
	}
	return io.EOF
}

// ErrStreamClosed is returned by Write on a stream that was closed normally.
// Distinct from io.EOF, which on the write side would be a category error.
var ErrStreamClosed = errors.New("tunnel: stream closed")

// writeErr is endErr's write-side counterpart.
func (s *Stream) writeErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeErr != nil {
		return s.closeErr
	}
	return ErrStreamClosed
}

// ErrDatagramTooLarge is returned by Write on a UDP stream for a datagram too
// large to fit a single frame. Splitting isn't an option - the tunnel carries one
// datagram per frame, so a split would silently deliver two shorter datagrams
// instead of one - so the only correct answer is the one a real network gives an
// oversized datagram: drop it. UDP relays check for this and drop that one packet
// rather than tearing the whole flow down.
var ErrDatagramTooLarge = errors.New("tunnel: datagram exceeds maximum frame payload")

func (s *Stream) Write(p []byte) (int, error) {
	select {
	case <-s.closeCh:
		return 0, s.writeErr()
	default:
	}

	// A UDP stream carries exactly one datagram per frame - every relay
	// (internal/proxy, internal/netstack) does one Write per datagram - so its
	// message boundaries have to survive, and an oversized one is dropped.
	if s.isUDP {
		if len(p) > protocol.MaxDataPlaintext {
			return 0, ErrDatagramTooLarge
		}
		if err := s.mux.sendData(s.id, p); err != nil {
			return 0, err
		}
		return len(p), nil
	}

	// A byte stream has no boundaries to preserve, so a large write is split
	// across frames. Before this it became one frame whose length overflowed the
	// header's uint16 and silently desynchronised the multiplexer for every
	// stream on the connection - see protocol.MaxFramePayload. Chunking at
	// DataChunkSize (rather than the higher hard limit) also keeps every frame
	// inside the range padding can still fully cover.
	for sent := 0; sent < len(p); {
		end := sent + protocol.DataChunkSize
		if end > len(p) {
			end = len(p)
		}
		if err := s.mux.sendData(s.id, p[sent:end]); err != nil {
			return sent, err
		}
		sent = end
	}
	return len(p), nil
}

func (s *Stream) Close() error {
	// Already closed (peer's CLOSE, or the session died): nothing to announce.
	if !s.closeWithErr(nil) {
		return nil
	}

	s.mux.sendClose(s.id)
	s.mux.removeStream(s.id)
	return nil
}

// close ends the stream the ordinary way - the peer sent CLOSE - so a subsequent
// Read reports io.EOF once whatever already arrived is drained.
func (s *Stream) close() {
	s.closeWithErr(nil)
}

// closeWithErr ends the stream, recording why. A non-nil err (see
// ErrSessionClosed) is reported to Read/Write instead of io.EOF, so a transfer
// cut off by the connection dying isn't mistaken for one that finished. Reports
// whether this call was the one that closed it; a second close is a no-op.
func (s *Stream) closeWithErr(err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.closed = true
	s.closeErr = err
	close(s.closeCh)
	return true
}

// ErrStreamStalled means this stream's reader stopped consuming for
// streamStallTimeout while its receive buffer stayed full, so the stream was reset
// to keep the shared connection moving. See receiveData.
var ErrStreamStalled = errors.New("tunnel: stream reader stalled, stream reset")

// streamStallTimeout bounds how long one stream's full receive buffer may hold up
// the multiplexer's single readLoop.
//
// There is no protocol-level flow control yet (no per-stream window, no credit
// frames - FrameSettings is still vestigial), so the receiving side has no way to
// tell the sender to slow down for one stream in particular. That leaves three
// possible behaviours when a local reader stops consuming: block the readLoop
// (freezes every other stream on the connection), buffer without limit (unbounded
// memory), or reset that one stream. The timeout picks a middle path: transient
// stalls - the normal case, milliseconds while a consumer catches up - are
// absorbed exactly as before, and only a reader that is genuinely stuck loses its
// stream, after costing the others a bounded delay instead of an unbounded one.
//
// Proper credit-based windowing belongs in the protocol change that also encrypts
// FrameOpen; this is the interim fix that stops one stuck stream from taking the
// whole tunnel down with it.
const streamStallTimeout = 5 * time.Second

// receiveData hands a frame's payload to whoever is reading this stream. Reports
// false if the stream was reset because it couldn't take the data.
//
// Called from the multiplexer's readLoop, so it must never block indefinitely.
// Two separate ways it used to:
//
//   - the closed-stream branch was a plain blocking send on a channel nothing
//     would ever read again, so 64 buffered frames later readLoop wedged
//     permanently and froze every other stream on the connection;
//   - a live-but-unread stream blocked it just as hard, for as long as the
//     reader stayed stuck (head-of-line blocking across unrelated streams).
func (s *Stream) receiveData(data []byte) bool {
	// Fast path, and the only path taken when the reader is keeping up.
	select {
	case s.readCh <- data:
		return true
	default:
	}

	timer := time.NewTimer(streamStallTimeout)
	defer timer.Stop()

	select {
	case s.readCh <- data:
		return true
	case <-s.closeCh:
		// Gone while we waited; dropping is correct, and the point is that we
		// aren't blocking on it.
		return true
	case <-timer.C:
		s.closeWithErr(ErrStreamStalled)
		return false
	}
}

func (s *Stream) ID() uint16 {
	return s.id
}

func (s *Stream) Target() string {
	return s.target
}

func (s *Stream) IsUDP() bool {
	return s.isUDP
}

var _ io.ReadWriteCloser = (*Stream)(nil)
