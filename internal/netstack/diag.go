package netstack

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"phantom/internal/diag"
)

// Diagnostics for the question "why did the internet go down": every flow on
// the device crosses openRemote, so this is the one place that sees which
// ones went direct, which went through the tunnel, which were DNS, and which
// of them got nothing back.
//
// Counted, not logged per flow: a phone opens hundreds of connections a
// minute, and a line per destination would be both unreadable and a browsing
// history. What is logged individually is failures, rate-limited, as ip:port
// only - see package diag for the privacy line.

const (
	summaryEvery   = 30 * time.Second
	heartbeatEvery = 5 * time.Minute
	// dnsAnswerWait matches how long a stub resolver typically waits before
	// retrying - a query unanswered this long is one the user felt.
	dnsAnswerWait = 5 * time.Second
	// firstByteWait is how long a TCP flow may go without a single byte back
	// after sending its first request (a TLS ClientHello, usually) before it
	// counts as unanswered.
	firstByteWait = 10 * time.Second
)

type flowStats struct {
	tcpTunnel, tcpDirect, udpTunnel, udpDirect, bypass atomic.Int64

	directFail     atomic.Int64 // direct dial failed; the flow fell back to the tunnel
	tunnelOpenFail atomic.Int64 // opening a stream in the tunnel failed; the flow was dropped
	noSession      atomic.Int64 // no live session to open a stream on; the flow was dropped

	dnsQueries, dnsAnswers, dnsUnanswered atomic.Int64
	dnsLatSumMs, dnsLatCount, dnsLatMaxMs atomic.Int64

	tunnelUnanswered, directUnanswered atomic.Int64
	tunnelTTFBSumMs, tunnelTTFBCount   atomic.Int64
	directTTFBSumMs, directTTFBCount   atomic.Int64
}

var failureLog = diag.NewLimiter(10, time.Minute)

// logFailure writes one rate-limited failure line.
func logFailure(ev, network, target string, err error, kv ...any) {
	ok, dropped := failureLog.Allow(ev)
	if !ok {
		return
	}
	fields := []any{"proto", network, "target", target}
	if err != nil {
		fields = append(fields, "err", err)
	}
	fields = append(fields, kv...)
	if dropped > 0 {
		fields = append(fields, "suppressedBefore", dropped)
	}
	diag.Event(diag.CatNet, ev, fields...)
}

// SetDiagExtra adds platform-supplied key/value pairs (the routing engine's
// own counters, say) to every periodic summary line.
func (t *Tunnel) SetDiagExtra(fn func() []any) {
	t.sessionMu.Lock()
	t.diagExtra = fn
	t.sessionMu.Unlock()
}

func (t *Tunnel) countFlow(network string, tunneled bool) {
	switch {
	case network == "tcp" && tunneled:
		t.stats.tcpTunnel.Add(1)
	case network == "tcp":
		t.stats.tcpDirect.Add(1)
	case tunneled:
		t.stats.udpTunnel.Add(1)
	default:
		t.stats.udpDirect.Add(1)
	}
}

// meterFlow wraps an opened flow so its first answer (or the lack of one) is
// measured. DNS and TCP only: those are the flows that always expect a reply
// to what they send first; plain UDP (QUIC, games, calls) doesn't have to.
func (t *Tunnel) meterFlow(network, target string, tunneled, dns bool, rw io.ReadWriteCloser) io.ReadWriteCloser {
	route := "direct"
	if tunneled {
		route = "tunnel"
	}
	switch {
	case dns:
		return &meter{
			ReadWriteCloser: rw,
			wait:            dnsAnswerWait,
			onWrite:         func() { t.stats.dnsQueries.Add(1) },
			onRead:          func() { t.stats.dnsAnswers.Add(1) },
			onFirstByte: func(d time.Duration) {
				ms := d.Milliseconds()
				t.stats.dnsLatSumMs.Add(ms)
				t.stats.dnsLatCount.Add(1)
				storeMax(&t.stats.dnsLatMaxMs, ms)
			},
			onUnanswered: func(waited time.Duration) {
				t.stats.dnsUnanswered.Add(1)
				logFailure("dnsUnanswered", network, target, nil, "route", route, "waitedMs", waited)
			},
		}
	case network == "tcp":
		return &meter{
			ReadWriteCloser: rw,
			wait:            firstByteWait,
			onFirstByte: func(d time.Duration) {
				if tunneled {
					t.stats.tunnelTTFBSumMs.Add(d.Milliseconds())
					t.stats.tunnelTTFBCount.Add(1)
				} else {
					t.stats.directTTFBSumMs.Add(d.Milliseconds())
					t.stats.directTTFBCount.Add(1)
				}
			},
			onUnanswered: func(waited time.Duration) {
				if tunneled {
					t.stats.tunnelUnanswered.Add(1)
				} else {
					t.stats.directUnanswered.Add(1)
				}
				logFailure("tcpUnanswered", network, target, nil, "route", route, "waitedMs", waited)
			},
		}
	}
	return rw
}

// meter times the first reply to a flow's first write, and reports a flow
// that got none within wait.
type meter struct {
	io.ReadWriteCloser
	wait            time.Duration
	onWrite, onRead func()
	onFirstByte     func(time.Duration)
	onUnanswered    func(time.Duration)

	mu       sync.Mutex
	started  time.Time
	answered bool
	timer    *time.Timer
}

func (m *meter) Write(p []byte) (int, error) {
	n, err := m.ReadWriteCloser.Write(p)
	if err != nil {
		return n, err
	}
	if m.onWrite != nil {
		m.onWrite()
	}
	m.mu.Lock()
	if m.started.IsZero() && !m.answered {
		m.started = time.Now()
		m.timer = time.AfterFunc(m.wait, m.expire)
	}
	m.mu.Unlock()
	return n, err
}

func (m *meter) Read(p []byte) (int, error) {
	n, err := m.ReadWriteCloser.Read(p)
	if n > 0 {
		if m.onRead != nil {
			m.onRead()
		}
		m.mu.Lock()
		first := !m.answered
		m.answered = true
		started := m.started
		if m.timer != nil {
			m.timer.Stop()
		}
		m.mu.Unlock()
		if first && !started.IsZero() && m.onFirstByte != nil {
			m.onFirstByte(time.Since(started))
		}
	}
	return n, err
}

func (m *meter) expire() {
	m.mu.Lock()
	answered, started := m.answered, m.started
	m.mu.Unlock()
	if !answered {
		m.onUnanswered(time.Since(started))
	}
}

func (m *meter) Close() error {
	m.mu.Lock()
	if m.timer != nil {
		m.timer.Stop()
	}
	m.mu.Unlock()
	return m.ReadWriteCloser.Close()
}

func storeMax(v *atomic.Int64, x int64) {
	for {
		cur := v.Load()
		if x <= cur || v.CompareAndSwap(cur, x) {
			return
		}
	}
}

func swap(v *atomic.Int64) int64 { return v.Swap(0) }

func avg(sum, count int64) int64 {
	if count == 0 {
		return 0
	}
	return sum / count
}

// runSummaries writes the periodic NET summary until stop closes: every
// summaryEvery while anything is happening, every heartbeatEvery when idle -
// so a quiet night costs a dozen lines, not thousands, but a gap in the log
// still means the process wasn't running rather than that nothing happened.
func (t *Tunnel) runSummaries(stop <-chan struct{}) {
	ticker := time.NewTicker(summaryEvery)
	defer ticker.Stop()
	lastWritten := time.Now()
	var lastUp, lastDown int64
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			s := &t.stats
			tcpT, tcpD := swap(&s.tcpTunnel), swap(&s.tcpDirect)
			udpT, udpD, byp := swap(&s.udpTunnel), swap(&s.udpDirect), swap(&s.bypass)
			dirFail, openFail, noSess := swap(&s.directFail), swap(&s.tunnelOpenFail), swap(&s.noSession)
			dnsQ, dnsA, dnsU := swap(&s.dnsQueries), swap(&s.dnsAnswers), swap(&s.dnsUnanswered)
			dnsSum, dnsCnt, dnsMax := swap(&s.dnsLatSumMs), swap(&s.dnsLatCount), swap(&s.dnsLatMaxMs)
			tunU, dirU := swap(&s.tunnelUnanswered), swap(&s.directUnanswered)
			tunSum, tunCnt := swap(&s.tunnelTTFBSumMs), swap(&s.tunnelTTFBCount)
			dirSum, dirCnt := swap(&s.directTTFBSumMs), swap(&s.directTTFBCount)

			up, down := atomic.LoadInt64(&t.bytesUp), atomic.LoadInt64(&t.bytesDown)
			active := tcpT+tcpD+udpT+udpD+byp+dnsQ > 0
			troubled := dirFail+openFail+noSess+dnsU+tunU+dirU > 0
			if !active && !troubled && now.Sub(lastWritten) < heartbeatEvery {
				continue
			}
			lastWritten = now

			t.sessionMu.Lock()
			session, router, extra := t.session, t.router, t.diagExtra
			t.sessionMu.Unlock()

			fields := []any{
				"smartRouter", router != nil,
				"tcpTunnel", tcpT, "tcpDirect", tcpD,
				"udpTunnel", udpT, "udpDirect", udpD, "bypass", byp,
				"dnsQ", dnsQ, "dnsAns", dnsA, "dnsUnanswered", dnsU,
				"dnsAvgMs", avg(dnsSum, dnsCnt), "dnsMaxMs", dnsMax,
				"tunnelTTFBms", avg(tunSum, tunCnt), "tunnelUnanswered", tunU,
				"directTTFBms", avg(dirSum, dirCnt), "directUnanswered", dirU,
				"directFail", dirFail, "tunnelOpenFail", openFail, "noSession", noSess,
				"upKB", (up - lastUp) / 1024, "downKB", (down - lastDown) / 1024,
			}
			lastUp, lastDown = up, down
			if session != nil {
				a := session.Multiplexer().Activity()
				fields = append(fields,
					"sessionAlive", session.IsAlive(),
					"serverSilentMs", now.Sub(a.LastRx),
					"lastTxAgoMs", now.Sub(a.LastTx))
			} else {
				fields = append(fields, "sessionAlive", false)
			}
			if extra != nil {
				fields = append(fields, extra()...)
			}
			diag.Event(diag.CatNet, "summary", fields...)
		}
	}
}
