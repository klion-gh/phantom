package tunnel

import (
	"errors"
	"io"
	"sync"
)

type Stream struct {
	id        uint16
	mux       *Multiplexer
	target    string
	readBuf   []byte
	readCh    chan []byte
	closeCh   chan struct{}
	closed    bool
	isIncoming bool
	isUDP     bool
	accepted  bool
	mu        sync.Mutex
}

func newStream(id uint16, mux *Multiplexer, target string) *Stream {
	return &Stream{
		id:     id,
		mux:    mux,
		target: target,
		readCh: make(chan []byte, 64),
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
			return 0, io.EOF
		}
	}
}

func (s *Stream) Write(p []byte) (int, error) {
	select {
	case <-s.closeCh:
		return 0, errors.New("stream closed")
	default:
	}

	err := s.mux.sendData(s.id, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.closeCh)
	s.mu.Unlock()

	s.mux.sendClose(s.id)
	s.mux.removeStream(s.id)
	return nil
}

func (s *Stream) close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.closeCh)
	}
	s.mu.Unlock()
}

func (s *Stream) receiveData(data []byte) {
	select {
	case s.readCh <- data:
	case <-s.closeCh:
		s.readCh <- data
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
