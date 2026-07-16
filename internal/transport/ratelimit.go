package transport

import (
	"sync"
	"time"
)

// rateLimiter throttles how often a single source IP may attempt the disguised
// auth handshake (§5), via a per-IP token bucket. The goal is anti-enumeration,
// not anti-DoS: an IP that blows its budget doesn't get its connection dropped
// (that would be a distinguishing behavior in itself, and a bare TCP reset
// under load is exactly the kind of tell this whole project avoids) - instead
// it's served the decoy site without spending an ECDH auth attempt on it, so a
// scanner hammering the endpoint sees nothing but an ordinary, boring website
// no matter how hard it tries, and can't map the auth path's behavior by
// volume. Genuine TLS-flood DoS is deliberately out of scope here - that
// belongs at the firewall/fail2ban layer, not inside the handshake.
//
// Defaults are loose on purpose: a legitimate client opens a small pool of
// connections on connect and one more per periodic ping (~every 6s), and many
// legitimate users can share one IP behind carrier-grade NAT, so the burst is
// generous and the sustained rate only needs to be low enough to defang a
// scanner doing thousands per second.
type rateLimiter struct {
	ratePerSec float64
	burst      float64

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens   float64
	lastSeen time.Time
}

// newRateLimiter builds a limiter. Non-positive values fall back to defaults
// (2 tokens/sec sustained, 60 burst) so an operator can leave the config fields
// unset and still get sane protection.
func newRateLimiter(ratePerSec, burst float64) *rateLimiter {
	if ratePerSec <= 0 {
		ratePerSec = 2
	}
	if burst <= 0 {
		burst = 60
	}
	rl := &rateLimiter{
		ratePerSec: ratePerSec,
		burst:      burst,
		buckets:    make(map[string]*tokenBucket),
	}
	go rl.reapLoop()
	return rl
}

// allow reports whether ip may attempt a handshake right now, consuming one
// token if so. A brand-new IP starts with a full burst. A nil limiter (e.g. a
// TLSServerConfig built by hand in a test, bypassing ListenAndServe) allows
// everything - the throttle is a production add-on, never a correctness gate.
func (rl *rateLimiter) allow(ip string) bool {
	if rl == nil {
		return true
	}
	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[ip]
	if !ok {
		rl.buckets[ip] = &tokenBucket{tokens: rl.burst - 1, lastSeen: now}
		return true
	}

	// Refill for the elapsed time, capped at burst.
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens = min(rl.burst, b.tokens+elapsed*rl.ratePerSec)
	b.lastSeen = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// reapLoop periodically drops buckets that have been idle long enough to have
// refilled to full anyway, so the map doesn't grow unbounded across the churn
// of many distinct scanning IPs.
func (rl *rateLimiter) reapLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-10 * time.Minute)
		rl.mu.Lock()
		for ip, b := range rl.buckets {
			if b.lastSeen.Before(cutoff) {
				delete(rl.buckets, ip)
			}
		}
		rl.mu.Unlock()
	}
}
