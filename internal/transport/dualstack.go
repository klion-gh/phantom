package transport

import (
	"context"
	"errors"
	"net"
	"time"
)

// IPv6 as a way around an IPv4 block.
//
// In the November 2026 wave several servers were blocked from Russia by IPv4
// address while their IPv6 stayed reachable. A client with IPv6 can only use
// that if it (1) asks for AAAA records at all and (2) doesn't sit out a full
// connect timeout on the dead IPv4 address before trying the IPv6 one. Both
// halves live here: LookupDual and dialRace - the same idea as RFC 8305
// ("Happy Eyeballs"), sized for one server rather than a website.

// v6Grace is how long a lookup waits for the AAAA answer once the A answer is
// in. Some networks answer AAAA slowly or not at all; without a bound they
// would slow down every connect on an IPv4-only network.
var v6Grace = 400 * time.Millisecond

// raceStagger is the head start each address gets before the next one is
// tried alongside it (RFC 8305 recommends 250ms). A reachable IPv4 server
// answers well within it, so on a normal network nothing changes; behind an
// IPv4 block the IPv6 attempt starts 300ms in instead of after a 10s timeout.
var raceStagger = 300 * time.Millisecond

// LookupDual resolves host to its IPv4 and IPv6 addresses, asked in parallel,
// ordered IPv4 first and then alternating. It waits for IPv6 at most v6Grace
// after IPv4 answered, and fully when IPv4 failed. A literal IP passes
// straight through.
func LookupDual(ctx context.Context, resolver IPLookuper, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	type answer struct {
		ips []net.IP
		err error
	}
	v4ch, v6ch := make(chan answer, 1), make(chan answer, 1)
	go func() { ips, err := resolver.LookupIP(ctx, "ip4", host); v4ch <- answer{ips, err} }()
	go func() { ips, err := resolver.LookupIP(ctx, "ip6", host); v6ch <- answer{ips, err} }()

	var v4, v6 answer
	gotV4, gotV6 := false, false
	var grace <-chan time.Time
	for !(gotV4 && gotV6) {
		select {
		case v4 = <-v4ch:
			gotV4 = true
			if v4.err == nil && len(v4.ips) > 0 && !gotV6 {
				grace = time.After(v6Grace)
			}
		case v6 = <-v6ch:
			gotV6 = true
		case <-grace:
			gotV6 = true // IPv4 is enough; don't wait out a slow AAAA
		}
	}
	ips := interleave(onlyFamily(v4.ips, true), onlyFamily(v6.ips, false))
	if len(ips) == 0 {
		err := v4.err
		if err == nil {
			err = v6.err
		}
		if err == nil {
			err = errors.New("no addresses")
		}
		return nil, err
	}
	return ips, nil
}

func onlyFamily(ips []net.IP, v4 bool) []net.IP {
	var out []net.IP
	for _, ip := range ips {
		if (ip.To4() != nil) == v4 {
			out = append(out, ip)
		}
	}
	return out
}

// interleave orders addresses v4, v6, v4, v6, ... - RFC 8305 §4.
func interleave(v4, v6 []net.IP) []net.IP {
	out := make([]net.IP, 0, len(v4)+len(v6))
	for i := 0; i < len(v4) || i < len(v6); i++ {
		if i < len(v4) {
			out = append(out, v4[i])
		}
		if i < len(v6) {
			out = append(out, v6[i])
		}
	}
	return out
}

// dialRace connects to the first of targets that answers: the first is dialed
// at once, each next one raceStagger later or as soon as the previous attempt
// failed, whichever comes first. The winner is returned and every other
// attempt is cancelled (a late success is closed).
func dialRace(ctx context.Context, dial func(ctx context.Context, target string) (net.Conn, error), targets []string) (net.Conn, error) {
	if len(targets) == 1 {
		return dial(ctx, targets[0])
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		conn net.Conn
		err  error
	}
	results := make(chan result, len(targets))
	next, running := 0, 0
	start := func() {
		target := targets[next]
		next++
		running++
		go func() {
			c, err := dial(ctx, target)
			results <- result{c, err}
		}()
	}
	start()
	timer := time.NewTimer(raceStagger)
	defer timer.Stop()

	var firstErr error
	for running > 0 {
		select {
		case r := <-results:
			running--
			if r.err == nil {
				cancel()
				// Close whatever else still connects; they're not needed.
				go func(n int) {
					for i := 0; i < n; i++ {
						if late := <-results; late.conn != nil {
							late.conn.Close()
						}
					}
				}(running)
				return r.conn, nil
			}
			if firstErr == nil {
				firstErr = r.err
			}
			if next < len(targets) {
				start()
				timer.Reset(raceStagger)
			}
		case <-timer.C:
			if next < len(targets) {
				start()
				timer.Reset(raceStagger)
			}
		}
	}
	return nil, firstErr
}

// StaticResolver answers lookups for the hosts in it from addresses already
// resolved - used where the caller must know every address a dial may use in
// advance (Windows adds a bypass route per IPv4 address before connecting).
// Unknown hosts fail rather than leaking to the system resolver.
type StaticResolver map[string][]net.IP

func (s StaticResolver) LookupIP(_ context.Context, network, host string) ([]net.IP, error) {
	var out []net.IP
	for _, ip := range s[host] {
		switch {
		case network == "ip4" && ip.To4() == nil, network == "ip6" && ip.To4() != nil:
			continue
		}
		out = append(out, ip)
	}
	if len(out) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return out, nil
}
