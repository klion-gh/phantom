// Package relaypath decides, per server, whether to go through the app's
// relay or straight to the server.
//
// A relay (cmd/relay) sits in Russia and reaches servers over IPv4. When a
// server is blocked by its IPv4 address the relay can't reach it either -
// but the phone may still reach it directly over IPv6 (see
// transport/dualstack.go). Through the relay alone such a server is dead even
// though a direct connection would work, so both paths are tried:
//
//   - a probe (Ping) starts on the preferred path and, if that hasn't
//     answered within HeadStart, races the other one alongside it;
//   - a connect (Order/Report) tries the preferred path first and the other
//     one if it fails.
//
// Whichever path answered is remembered for the server's domain for a while,
// so later probes and the connect go straight to it. A working relay answers
// well within HeadStart, so with nothing blocked the direct path is never
// dialed and nothing changes.
package relaypath

import (
	"sync"
	"time"

	"phantom/internal/config"
	"phantom/internal/pingcheck"
)

// HeadStart is how long the preferred path gets before the other one joins.
var HeadStart = 1500 * time.Millisecond

// remember is how long a domain stays on the direct path once the relay
// failed it and a direct connection worked - long enough to stop paying for
// the failing relay attempt on every probe, short enough for the relay to be
// tried again once its route recovers.
var remember = 30 * time.Minute

var (
	mu     sync.Mutex
	direct = map[string]time.Time{} // domain -> direct preferred until
	now    = time.Now
)

func domainOf(configYAML string) string {
	cfg, err := config.ParseClientConfig([]byte(configYAML))
	if err != nil {
		return ""
	}
	return cfg.Domain
}

func preferDirect(domain string) bool {
	if domain == "" {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	until, ok := direct[domain]
	if ok && now().After(until) {
		delete(direct, domain)
		return false
	}
	return ok
}

func record(domain string, viaDirect bool) {
	if domain == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if viaDirect {
		direct[domain] = now().Add(remember)
	} else {
		delete(direct, domain)
	}
}

// Order returns the two configs to try for a connect, preferred first:
// relayedYAML (the config with the relay added) and directYAML (as imported).
// With no relay (the two are equal) there's only one.
func Order(directYAML, relayedYAML string) []string {
	if directYAML == relayedYAML {
		return []string{directYAML}
	}
	if preferDirect(domainOf(directYAML)) {
		return []string{directYAML, relayedYAML}
	}
	return []string{relayedYAML, directYAML}
}

// Report records which of Order's configs connected.
func Report(directYAML, used string) {
	record(domainOf(directYAML), used == directYAML)
}

// Ping probes a server the way fn does (pingcheck.PingWith, Internet, ...),
// through the relay or directly - see the package comment.
func Ping(directYAML, relayedYAML string, fn func(configYAML string) (pingcheck.Result, error)) (pingcheck.Result, error) {
	order := Order(directYAML, relayedYAML)
	if len(order) == 1 {
		return fn(order[0])
	}
	type answer struct {
		yaml string
		res  pingcheck.Result
		err  error
	}
	results := make(chan answer, 2)
	run := func(y string) {
		go func() {
			r, err := fn(y)
			results <- answer{y, r, err}
		}()
	}
	run(order[0])
	timer := time.NewTimer(HeadStart)
	defer timer.Stop()
	started, pending := 1, 1
	var firstErr error
	for pending > 0 {
		select {
		case a := <-results:
			pending--
			if a.err == nil {
				Report(directYAML, a.yaml)
				return a.res, nil
			}
			if firstErr == nil {
				firstErr = a.err
			}
			if started < 2 {
				started++
				pending++
				run(order[1])
			}
		case <-timer.C:
			if started < 2 {
				started++
				pending++
				run(order[1])
			}
		}
	}
	return pingcheck.Result{}, firstErr
}
