// Package diag writes structured diagnostic events - one `cat=X ev=Y k=v ...`
// line each - through the standard log package, so they land wherever a
// platform points it: phantom logs next to the exe on Windows, the app's own
// log file on Android (via mobile.SetLogSink). Same shape as the Android
// Diag.kt and the Windows frontend's diag.js, so one grep reads all of them.
//
// What must not go in here: anything that turns the log into a browsing
// history. Aggregate counts, the user's own listed sites, and the ip:port of
// connections that *failed* (rate-limited) are fine; the name of every
// domain looked up or every destination connected to is not. The log is on
// the user's device, but it is also exactly what gets shared in a bug report.
package diag

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Categories, kept in step with Android's Diag.Cat and windows/diag.go.
const (
	CatApp    = "APP"    // process start, environment
	CatVPN    = "VPN"    // tunnel lifecycle as the platform sees it
	CatRoute  = "ROUTE"  // smart-routing engine, auto-select
	CatNet    = "NET"    // per-flow routing inside the tunnel: counts, DNS, failures
	CatTunnel = "TUNNEL" // the connection to the server: dials, deaths, stalls
)

// Event writes one structured line. kv is alternating key, value.
func Event(cat, ev string, kv ...any) {
	var b strings.Builder
	fmt.Fprintf(&b, "cat=%s ev=%s", cat, ev)
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, " %v=%v", kv[i], format(kv[i+1]))
	}
	if len(kv)%2 != 0 {
		fmt.Fprintf(&b, " MALFORMED_TRAILING=%v", kv[len(kv)-1])
	}
	log.Print(b.String())
}

func format(v any) any {
	switch x := v.(type) {
	case time.Duration:
		return x.Milliseconds()
	case error:
		if x == nil {
			return "nil"
		}
		// Errors can carry spaces and "=", which would break k=v parsing;
		// quoting keeps one field one field.
		return fmt.Sprintf("%q", x.Error())
	case string:
		if strings.ContainsAny(x, " =\"") {
			return fmt.Sprintf("%q", x)
		}
		if x == "" {
			return `""`
		}
		return x
	}
	return v
}

// Limiter caps how often one kind of event is written: at most n per window
// per key, with the number dropped reported on the next one let through. The
// events it guards (a failed dial, a DNS query with no answer) are exactly the
// ones that arrive in hundreds when something is badly wrong - the first few
// say what happened, the rest would only push everything else out of the log.
type Limiter struct {
	n      int
	window time.Duration

	mu    sync.Mutex
	state map[string]*bucket
}

type bucket struct {
	start      time.Time
	count      int
	suppressed int
}

func NewLimiter(n int, window time.Duration) *Limiter {
	return &Limiter{n: n, window: window, state: map[string]*bucket{}}
}

// Allow reports whether an event under key may be written now, and how many
// were suppressed since the last one that was.
func (l *Limiter) Allow(key string) (ok bool, suppressed int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.state[key]
	if b == nil {
		b = &bucket{start: now}
		l.state[key] = b
	}
	if now.Sub(b.start) >= l.window {
		b.start = now
		b.count = 0
	}
	if b.count >= l.n {
		b.suppressed++
		return false, 0
	}
	b.count++
	suppressed = b.suppressed
	b.suppressed = 0
	return true, suppressed
}
