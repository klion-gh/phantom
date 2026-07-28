package tunnel

import (
	"errors"
	"io"
	"net"
	"sync"

	"phantom/internal/logx"
	"phantom/internal/protocol"
)

type Multiplexer struct {
	conn         net.Conn
	crypto       *protocol.SessionCrypto
	streams      map[uint16]*Stream
	mu           sync.RWMutex
	nextClientID uint16
	nextServerID uint16
	closed       chan struct{}
	writeCh      chan *writeRequest
	acceptCh     chan *Stream
	closeOnce    sync.Once
}

type writeRequest struct {
	frame *protocol.Frame
	errCh chan error
}

// NewMultiplexer starts a bidirectional frame multiplexer over conn. Session
// authentication and per-session key derivation have already happened out of
// band (internal/handshake, before conn ever reaches here), so there is no
// in-band auth handshake - the first thing on the wire is application frames,
// which deliberately avoids the "fixed-size frame right after the TLS
// handshake" signature v1 had (see PROTOCOL.md).
func NewMultiplexer(conn net.Conn, crypto *protocol.SessionCrypto) *Multiplexer {
	m := &Multiplexer{
		conn:         conn,
		crypto:       crypto,
		streams:      make(map[uint16]*Stream),
		nextClientID: 1,
		nextServerID: 2,
		closed:       make(chan struct{}),
		writeCh:      make(chan *writeRequest, 256),
		acceptCh:     make(chan *Stream, 64),
	}

	go m.readLoop()
	go m.writeLoop()

	return m
}

func (m *Multiplexer) Open(target string) (*Stream, error) {
	return m.openStream(target, false)
}

func (m *Multiplexer) OpenUDP(target string) (*Stream, error) {
	return m.openStream(target, true)
}

func (m *Multiplexer) openStream(target string, udp bool) (*Stream, error) {
	select {
	case <-m.closed:
		return nil, ErrSessionClosed
	default:
	}

	s := newStream(0, m, target)
	s.isUDP = udp

	id, err := m.allocStreamID(s)
	if err != nil {
		return nil, err
	}
	s.id = id

	var flags protocol.Flags
	if udp {
		flags = protocol.FlagUDP
	}

	openFrame := &protocol.Frame{
		Type:     protocol.FrameOpen,
		StreamID: id,
		Flags:    flags,
		Payload:  []byte(target),
	}

	if err := m.sendFrame(openFrame); err != nil {
		m.removeStream(id)
		return nil, err
	}

	return s, nil
}

// ErrStreamIDsExhausted means every ID in this side's half of the 16-bit space is
// currently in use - 32768 simultaneously open streams, which in practice means
// streams are being leaked rather than that a legitimate workload needs more.
var ErrStreamIDsExhausted = errors.New("tunnel: no free stream IDs")

// allocStreamID reserves a free ID for s and registers it, walking forward from
// the last one handed out and skipping any that are still live.
//
// The counter used to be a bare `nextClientID += 2` on a uint16, with the new
// stream then written into the map unconditionally. Since a single pooled
// connection carries every stream for its whole lifetime, a busy client gets
// through 32768 streams in hours of ordinary browsing - at which point the
// counter wrapped back onto IDs that were still open and silently overwrote
// them, splicing two unrelated connections' data together.
//
// The walk steps by 2 from an odd start, so allocated IDs stay odd across the
// uint16 wrap (65535+2 == 1) and 0 - reserved for session-level frames like PING
// - is never handed out. Only the initiating side ever calls this; the
// nextServerID field is vestigial (the server accepts streams, never opens
// them), so if a future change ever has the server initiate, it must take the
// even parity or the two sides will collide.
func (m *Multiplexer) allocStreamID(s *Stream) (uint16, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 32768 candidates: every value of this side's parity.
	for i := 0; i < 1<<15; i++ {
		id := m.nextClientID
		m.nextClientID += 2
		if _, inUse := m.streams[id]; inUse {
			continue
		}
		m.streams[id] = s
		return id, nil
	}
	return 0, ErrStreamIDsExhausted
}

func (m *Multiplexer) Accept() (*Stream, error) {
	select {
	case s := <-m.acceptCh:
		return s, nil
	case <-m.closed:
		return nil, ErrSessionClosed
	}
}

func (m *Multiplexer) sendFrame(f *protocol.Frame) error {
	req := &writeRequest{
		frame: f,
		errCh: make(chan error, 1),
	}

	select {
	case m.writeCh <- req:
		// Wait for the result, but not forever. Both cases of the select above
		// are ready once the multiplexer is closed - writeCh is buffered - and Go
		// picks between ready cases at random, so roughly half the time a frame
		// gets queued onto a channel whose writeLoop has already returned. Nobody
		// then writes req.errCh, and a bare `<-req.errCh` blocked permanently:
		// not only Stream.Write but Open itself, which is what made a dropped
		// connection still wedge callers even after Close learned to wake streams.
		select {
		case err := <-req.errCh:
			return err
		case <-m.closed:
			return ErrSessionClosed
		}
	case <-m.closed:
		return ErrSessionClosed
	}
}

func (m *Multiplexer) readLoop() {
	defer func() {
		m.Close()
	}()

	for {
		select {
		case <-m.closed:
			return
		default:
		}

		headerBuf := make([]byte, protocol.FrameHeaderSize)
		if _, err := io.ReadFull(m.conn, headerBuf); err != nil {
			if !errors.Is(err, io.EOF) {
				// Debug: an ordinary disconnect (reset, interface gone, pool
				// recycle) lands here, so at Info this was one line per closed
				// connection - noise that buried anything real.
				logx.Debugf("[mux] read header error: %v", err)
			}
			return
		}

		payloadLen := uint16(headerBuf[4])<<8 | uint16(headerBuf[5])

		var fullFrame []byte
		if payloadLen > 0 {
			payloadBuf := make([]byte, payloadLen)
			if _, err := io.ReadFull(m.conn, payloadBuf); err != nil {
				logx.Debugf("[mux] read payload error: %v", err)
				return
			}
			fullFrame = append(headerBuf, payloadBuf...)
		} else {
			fullFrame = headerBuf
		}

		frame, err := protocol.Decode(fullFrame)
		if err != nil {
			// Warn, not Debug: inside an authenticated, AEAD-protected tunnel a
			// frame that won't parse shouldn't be possible, so it means a real
			// anomaly (corruption, or a bug like the header-length overflow this
			// used to be reachable through) and an operator should see it.
			logx.Warnf("[mux] decode error: %v", err)
			continue
		}

		if frame.Type.IsEncrypted() && m.crypto != nil {
			aad := protocol.FrameAAD(frame.Type, frame.Flags, frame.StreamID)
			decrypted, err := m.crypto.DecryptFrame(aad, frame.Payload)
			if err != nil {
				logx.Warnf("[mux] decrypt error: %v", err)
				continue
			}
			frame.Payload = decrypted
		}

		m.handleFrame(frame)
	}
}

func (m *Multiplexer) handleFrame(f *protocol.Frame) {
	switch f.Type {
	case protocol.FrameOpen:
		m.handleOpen(f)
	case protocol.FrameData:
		m.handleData(f)
	case protocol.FrameClose:
		m.handleClose(f)
	case protocol.FramePing:
		m.handlePing(f)
	case protocol.FrameSettings:
		m.handleSettings(f)
	case protocol.FramePadding:
		// ignore
	}
}

func (m *Multiplexer) handleOpen(f *protocol.Frame) {
	m.mu.Lock()
	id := f.StreamID
	if _, exists := m.streams[id]; exists {
		m.mu.Unlock()
		return
	}

	s := newStream(id, m, string(f.Payload))
	s.isIncoming = true
	s.isUDP = f.Flags&protocol.FlagUDP != 0
	m.streams[id] = s
	m.mu.Unlock()

	select {
	case m.acceptCh <- s:
	case <-m.closed:
	}

	// Debug, not Info: this fires once per stream on the server (clients never
	// accept streams) and names the destination, so at Info it was half of the
	// per-user browsing history the server used to keep. See internal/proxy's
	// note in direct.go.
	logx.Debugf("[mux] opened stream %d -> %s", id, string(f.Payload))
}

func (m *Multiplexer) handleData(f *protocol.Frame) {
	m.mu.RLock()
	s, ok := m.streams[f.StreamID]
	m.mu.RUnlock()

	if !ok {
		return
	}

	// A stream whose reader stalled long enough to be reset (see receiveData) is
	// dropped here and its peer told to stop sending, so the connection keeps
	// serving every other stream instead of being held hostage by this one.
	if !s.receiveData(f.Payload) {
		logx.Warnf("[mux] stream %d reset: reader stalled for %s", f.StreamID, streamStallTimeout)
		m.removeStream(f.StreamID)
		// Asynchronously: sendFrame waits on writeLoop, which can itself be
		// blocked on a full socket send buffer, and readLoop must not queue up
		// behind that.
		go m.sendClose(f.StreamID)
	}
}

func (m *Multiplexer) handleClose(f *protocol.Frame) {
	m.mu.Lock()
	if s, ok := m.streams[f.StreamID]; ok {
		s.close()
		delete(m.streams, f.StreamID)
	}
	m.mu.Unlock()
}

func (m *Multiplexer) handlePing(f *protocol.Frame) {
	pong := &protocol.Frame{
		Type:     protocol.FramePing,
		StreamID: 0,
		Payload:  f.Payload,
	}
	m.sendFrame(pong)
}

func (m *Multiplexer) handleSettings(f *protocol.Frame) {}

func (m *Multiplexer) writeLoop() {
	for {
		select {
		case req := <-m.writeCh:
			f := req.frame
			if f.Type.IsEncrypted() && m.crypto != nil {
				aad := protocol.FrameAAD(f.Type, f.Flags, f.StreamID)
				encrypted, err := m.crypto.EncryptFrame(aad, f.Payload)
				if err != nil {
					req.errCh <- err
					continue
				}
				f = &protocol.Frame{
					Type:     f.Type,
					StreamID: f.StreamID,
					Flags:    f.Flags,
					Payload:  encrypted,
				}
			}

			data, err := f.Encode()
			if err != nil {
				req.errCh <- err
				continue
			}

			_, err = m.conn.Write(data)
			req.errCh <- err

		case <-m.closed:
			return
		}
	}
}

func (m *Multiplexer) removeStream(id uint16) {
	m.mu.Lock()
	delete(m.streams, id)
	m.mu.Unlock()
}

func (m *Multiplexer) sendClose(id uint16) {
	closeFrame := &protocol.Frame{
		Type:     protocol.FrameClose,
		StreamID: id,
	}
	m.sendFrame(closeFrame)
}

func (m *Multiplexer) sendData(id uint16, data []byte) error {
	f := &protocol.Frame{
		Type:     protocol.FrameData,
		StreamID: id,
		Payload:  data,
	}
	return m.sendFrame(f)
}

// ErrSessionClosed is what a stream's pending Read/Write fails with when the
// whole multiplexer died underneath it, as opposed to the peer closing that one
// stream (which is an ordinary io.EOF). The distinction matters to callers: an
// interrupted transfer must not look like a complete one.
var ErrSessionClosed = errors.New("tunnel: session closed")

// Close shuts the multiplexer down and wakes every stream still open on it.
//
// Waking the streams is the part that used to be missing: Close only closed
// m.closed and the socket, so a Stream blocked in Read sat on a closeCh that
// nothing would ever close - the CLOSE frame that normally closes it can't
// arrive from a dead connection. Every in-flight stream leaked its goroutine and
// its local socket, and the io.Copy pairs in internal/proxy/internal/netstack
// blocked forever, so a dropped connection showed up to the user as tabs hanging
// indefinitely rather than failing fast - even though ConnPool had already
// redialed a healthy replacement in the background.
func (m *Multiplexer) Close() error {
	m.closeOnce.Do(func() {
		close(m.closed)
		m.conn.Close()

		m.mu.Lock()
		streams := make([]*Stream, 0, len(m.streams))
		for id, s := range m.streams {
			streams = append(streams, s)
			delete(m.streams, id)
		}
		m.mu.Unlock()

		// Outside the lock: closeWithErr takes the stream's own lock, and there's
		// no reason to hold m.mu across all of them.
		for _, s := range streams {
			s.closeWithErr(ErrSessionClosed)
		}
	})
	return nil
}

func (m *Multiplexer) IsClosed() bool {
	select {
	case <-m.closed:
		return true
	default:
		return false
	}
}

// Done returns a channel that closes the instant this multiplexer dies -
// readLoop hitting a real I/O error (its underlying connection's interface
// disappearing, a reset, etc.) closes it via Close(), same as an explicit
// Close() call. Lets a caller (ConnPool.monitorConn) react immediately
// instead of only discovering a dead connection on its own polling schedule.
func (m *Multiplexer) Done() <-chan struct{} {
	return m.closed
}
