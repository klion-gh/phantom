package tunnel

import (
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"

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

	authPending  bool
	authResultCh chan error
	sendAuth     bool
}

type writeRequest struct {
	frame *protocol.Frame
	errCh chan error
}

func NewMultiplexer(conn net.Conn, crypto *protocol.SessionCrypto, sendAuth bool, expectAuth ...bool) *Multiplexer {
	auth := false
	if len(expectAuth) > 0 {
		auth = expectAuth[0]
	}
	m := &Multiplexer{
		conn:         conn,
		crypto:       crypto,
		streams:      make(map[uint16]*Stream),
		nextClientID: 1,
		nextServerID: 2,
		closed:       make(chan struct{}),
		writeCh:      make(chan *writeRequest, 256),
		acceptCh:     make(chan *Stream, 64),
		authPending:  auth,
		authResultCh: make(chan error, 1),
		sendAuth:     sendAuth,
	}

	go m.readLoop()
	go m.writeLoop()

	return m
}

func (m *Multiplexer) WaitForAuth(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-m.authResultCh:
		return err
	case <-timer.C:
		return errors.New("auth timeout")
	case <-m.closed:
		return errors.New("connection closed")
	}
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
		return nil, errors.New("multiplexer closed")
	default:
	}

	m.mu.Lock()
	id := m.nextClientID
	m.nextClientID += 2
	m.mu.Unlock()

	s := newStream(id, m, target)
	s.isUDP = udp
	m.mu.Lock()
	m.streams[id] = s
	m.mu.Unlock()

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

func (m *Multiplexer) Accept() (*Stream, error) {
	select {
	case s := <-m.acceptCh:
		return s, nil
	case <-m.closed:
		return nil, errors.New("multiplexer closed")
	}
}

func (m *Multiplexer) sendFrame(f *protocol.Frame) error {
	req := &writeRequest{
		frame: f,
		errCh: make(chan error, 1),
	}

	select {
	case m.writeCh <- req:
		return <-req.errCh
	case <-m.closed:
		return errors.New("multiplexer closed")
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
				log.Printf("[mux] read header error: %v", err)
			}
			return
		}

		payloadLen := uint16(headerBuf[4])<<8 | uint16(headerBuf[5])

		var fullFrame []byte
		if payloadLen > 0 {
			payloadBuf := make([]byte, payloadLen)
			if _, err := io.ReadFull(m.conn, payloadBuf); err != nil {
				log.Printf("[mux] read payload error: %v", err)
				return
			}
			fullFrame = append(headerBuf, payloadBuf...)
		} else {
			fullFrame = headerBuf
		}

		frame, err := protocol.Decode(fullFrame)
		if err != nil {
			log.Printf("[mux] decode error: %v", err)
			continue
		}

		if frame.Type == protocol.FrameData && m.crypto != nil {
			streamIDAAD := make([]byte, 2)
			streamIDAAD[0] = byte(frame.StreamID >> 8)
			streamIDAAD[1] = byte(frame.StreamID)
			decrypted, err := m.crypto.DecryptFrame(streamIDAAD, frame.Payload)
			if err != nil {
				log.Printf("[mux] decrypt error: %v", err)
				continue
			}
			frame.Payload = decrypted
		}

		if frame.Type == protocol.FrameAuth && m.authPending {
			m.handleAuth(frame)
			continue
		}

		if m.authPending {
			continue
		}

		m.handleFrame(frame)
	}
}

func (m *Multiplexer) handleAuth(f *protocol.Frame) {
	if protocol.VerifyAuthTag(m.crypto.AuthKey[:], []byte("Phantom-auth"), f.Payload) {
		log.Printf("[mux] auth verified")
		m.authPending = false
		m.authResultCh <- nil
	} else {
		log.Printf("[mux] auth failed")
		m.authResultCh <- errors.New("auth verification failed")
		m.Close()
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

	log.Printf("[mux] opened stream %d -> %s", id, string(f.Payload))
}

func (m *Multiplexer) handleData(f *protocol.Frame) {
	m.mu.RLock()
	s, ok := m.streams[f.StreamID]
	m.mu.RUnlock()

	if ok {
		s.receiveData(f.Payload)
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
	if m.sendAuth && m.crypto != nil {
		tag := protocol.ComputeAuthTag(m.crypto.AuthKey[:], []byte("Phantom-auth"))
		authFrame := &protocol.Frame{
			Type:     protocol.FrameAuth,
			StreamID: 0,
			Payload:  tag,
		}
		data, err := authFrame.Encode()
		if err == nil {
			m.conn.Write(data)
		}
	}

	for {
		select {
		case req := <-m.writeCh:
			f := req.frame
			if f.Type == protocol.FrameData && m.crypto != nil {
				streamIDAAD := make([]byte, 2)
				streamIDAAD[0] = byte(f.StreamID >> 8)
				streamIDAAD[1] = byte(f.StreamID)
				encrypted, err := m.crypto.EncryptFrame(streamIDAAD, f.Payload)
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

func (m *Multiplexer) Close() error {
	m.closeOnce.Do(func() {
		close(m.closed)
		m.conn.Close()
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
