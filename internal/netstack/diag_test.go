package netstack

import (
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// pipeRW is a flow whose far side is controlled by the test.
type pipeRW struct {
	*io.PipeReader
	w io.Writer
}

func (p pipeRW) Write(b []byte) (int, error) { return len(b), nil }
func (p pipeRW) Close() error                { return p.PipeReader.Close() }

// A flow that is written to and never answered must be reported - that is the
// whole signature of a frozen tunnel: nothing errors, nothing closes, requests
// just go unanswered.
func TestMeterReportsUnansweredFlow(t *testing.T) {
	r, _ := io.Pipe()
	var unanswered atomic.Int32
	m := &meter{
		ReadWriteCloser: pipeRW{PipeReader: r},
		wait:            30 * time.Millisecond,
		onUnanswered:    func(time.Duration) { unanswered.Add(1) },
	}
	defer m.Close()

	m.Write([]byte("query"))
	time.Sleep(120 * time.Millisecond)
	if unanswered.Load() != 1 {
		t.Fatalf("expected one unanswered report, got %d", unanswered.Load())
	}
}

// An answered flow is timed and not reported.
func TestMeterTimesAnsweredFlow(t *testing.T) {
	r, w := io.Pipe()
	var unanswered atomic.Int32
	var gotLatency atomic.Bool
	m := &meter{
		ReadWriteCloser: pipeRW{PipeReader: r},
		wait:            80 * time.Millisecond,
		onUnanswered:    func(time.Duration) { unanswered.Add(1) },
		onFirstByte:     func(time.Duration) { gotLatency.Store(true) },
	}
	defer m.Close()

	m.Write([]byte("query"))
	go w.Write([]byte("answer"))
	buf := make([]byte, 16)
	if _, err := m.Read(buf); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if unanswered.Load() != 0 {
		t.Fatal("an answered flow was reported as unanswered")
	}
	if !gotLatency.Load() {
		t.Fatal("the first answer's latency was not recorded")
	}
}
