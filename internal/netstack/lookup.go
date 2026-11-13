package netstack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"phantom/internal/netcheck"
)

// tunnelResolver is who LookupIP asks: a public resolver, reached from the
// server's side of the tunnel.
const tunnelResolver = "1.1.1.1:53"

// LookupIP resolves host through the tunnel - the public resolver asked from
// the server's side, so neither the network's own resolver nor anything on
// the path between the device and the server sees or answers the question.
// The smart-routing engine uses it to learn listed sites' addresses up front
// (routing.Engine.SetResolver): in Russia the network's resolver answers
// blocked names with nothing, which left those sites unlearned.
func (t *Tunnel) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	session := t.currentSession()
	if session == nil {
		return nil, errNoSession
	}
	name, err := dnsmessage.NewName(strings.TrimSuffix(host, ".") + ".")
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	var lastErr error
	for i, qtype := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		stream, err := session.OpenUDP(tunnelResolver)
		if err != nil {
			return nil, err
		}
		got, err := exchange(ctx, stream, name, qtype, uint16(0x5150+i))
		stream.Close()
		if err != nil {
			lastErr = err
			continue
		}
		ips = append(ips, got...)
	}
	if len(ips) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("no addresses for %s", host)
		}
		return nil, lastErr
	}
	return ips, nil
}

// exchange sends one query over a datagram stream and reads its answer.
func exchange(ctx context.Context, rw interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
}, name dnsmessage.Name, qtype dnsmessage.Type, id uint16) ([]net.IP, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: qtype, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	q, err := b.Finish()
	if err != nil {
		return nil, err
	}
	if _, err := rw.Write(q); err != nil {
		return nil, err
	}

	type result struct {
		b   []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := rw.Read(buf)
		done <- result{buf[:n], err}
	}()
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		rw.Close() // unblocks the read
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		rw.Close()
		return nil, errors.New("no answer through the tunnel")
	}
	if r.err != nil {
		return nil, r.err
	}

	var p dnsmessage.Parser
	h, err := p.Start(r.b)
	if err != nil {
		return nil, err
	}
	if h.ID != id {
		return nil, errors.New("answer to another query")
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, err
	}
	var ips []net.IP
	for {
		ah, err := p.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch ah.Type {
		case dnsmessage.TypeA:
			a, err := p.AResource()
			if err != nil {
				return nil, err
			}
			ips = append(ips, net.IP(a.A[:]))
		case dnsmessage.TypeAAAA:
			a, err := p.AAAAResource()
			if err != nil {
				return nil, err
			}
			ips = append(ips, net.IP(a.AAAA[:]))
		default:
			if err := p.SkipAnswer(); err != nil {
				return nil, err
			}
		}
	}
	return ips, nil
}

// CheckInternet fetches a connectivity-check page through the live tunnel
// (see internal/netcheck) and reports how long the answer took. It goes
// through the same session every flow uses - refreshed first if it died - so
// a nil error means traffic really gets through the server right now, which
// a handshake-level "alive" can't promise: after a network blip a session can
// stay open while nothing comes back on it.
func (t *Tunnel) CheckInternet(ctx context.Context) (time.Duration, error) {
	session := t.currentSession()
	if session == nil {
		return 0, errNoSession
	}
	return netcheck.Through(ctx, func(addr string) (io.ReadWriteCloser, error) {
		return session.Open(addr)
	})
}
