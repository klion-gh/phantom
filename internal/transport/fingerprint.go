package transport

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"

	"phantom/internal/diag"
)

// Why the ClientHello and the pacing of handshakes matter as much as they do:
// as reverse-engineered in June 2026 ("О схеме ограничений РКН в июне 2026-го",
// Habr), TSPU freezes every connection to a server for ~120s when *all three*
// of these hold for one TLS connection attempt:
//
//  1. the server's IP is in a "suspicious" subnet (foreign and, since June,
//     several Russian datacenters);
//  2. the ClientHello fingerprint is on its list - Chrome, Safari, iOS - while
//     Firefox, Android OkHttp, Edge, 360 and QQ pass on most operators;
//  3. more than 3 attempts to the same SNI arrived less than ~350-400ms apart
//     within the last 60s.
//
// Breaking any one of them is enough. Condition 1 is the operator's choice of
// server, not this code's; the other two are handled here: a non-listed
// fingerprint by default (AutoFingerprint), and a floor on the spacing between
// handshakes to one SNI (waitHandshakeTurn). The source is one author's
// black-box reconstruction and varies by region/operator - which is also why
// the fingerprint stays user-selectable rather than hard-coded.

// AutoFingerprint is what "auto" (and a config with no fingerprint at all)
// resolves to: Firefox, the passing profile with the most reports of actually
// working in June 2026. A var rather than a constant so it can move without
// every config having to be edited, if that stops being true.
//
// Not OkHttp, despite "Android OkHttp" being on the passing list and looking
// like any other Android app: uTLS's only OkHttp profile (Android 11) offers
// TLS 1.2 at most, and the server only speaks TLS 1.3 - it can't connect at
// all (TestEveryFingerprintCompletesTheFullHandshake caught exactly that).
var AutoFingerprint = "firefox"

// getFingerprint maps a config's fingerprint name to a uTLS ClientHelloID.
//
// Deliberately not auto-rotated on failure: a fingerprint change while a
// freeze is in effect was reported to extend it from 120s to 600s. Pick one
// that passes and keep it.
//
// Chrome/Safari stay available for explicit opt-in; their ClientHellos (Chrome
// with its X25519MLKEM768 share especially, ~1.8KB - two TCP segments) are the
// ones currently targeted, and on some operators only the first segment of a
// large ClientHello was seen to arrive at all.
func getFingerprint(name string) (utls.ClientHelloID, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || n == "auto" {
		n = AutoFingerprint
	}
	switch n {
	case "firefox", "firefox120", "firefox130":
		return utls.HelloFirefox_120, nil
	case "edge":
		return utls.HelloEdge_106, nil
	case "360":
		return utls.Hello360_11_0, nil
	case "qq":
		return utls.HelloQQ_11_1, nil
	case "chrome133", "chrome":
		return utls.HelloChrome_133, nil
	case "chrome131":
		return utls.HelloChrome_131, nil
	case "chrome120":
		return utls.HelloChrome_120, nil
	case "safari16", "safari18", "safari":
		return utls.HelloSafari_16_0, nil
	default:
		return utls.HelloFirefox_120, fmt.Errorf("unknown fingerprint %q", name)
	}
}

// handshakeSpacing is the minimum gap between two TLS handshakes to the same
// SNI from this process - comfortably above the ~350-400ms that makes
// consecutive attempts count as "parallel" (condition 3 above). With it, this
// process alone can never produce the burst, however many things want a
// connection at once: the tunnel redialing, a ping per config, an auto-select
// probe round, all right after a network change.
const handshakeSpacing = 500 * time.Millisecond

var handshakeGate = &gate{next: map[string]time.Time{}}

var spacedLog = diag.NewLimiter(5, time.Minute)

type gate struct {
	mu   sync.Mutex
	next map[string]time.Time
}

// waitHandshakeTurn blocks until a handshake to sni may start, reserving the
// slot after it for whoever asks next. A small random jitter is added on top
// so queued handshakes don't go out on an exact metronome either.
func waitHandshakeTurn(ctx context.Context, sni string) error {
	return handshakeGate.wait(ctx, sni, handshakeSpacing+time.Duration(rand.IntN(150))*time.Millisecond)
}

func (g *gate) wait(ctx context.Context, key string, spacing time.Duration) error {
	g.mu.Lock()
	now := time.Now()
	at := g.next[key]
	if at.Before(now) {
		at = now
	}
	g.next[key] = at.Add(spacing)
	// Entries only matter within one spacing window; drop stale ones so the
	// map can't grow with every server ever dialed.
	for k, t := range g.next {
		if t.Before(now) {
			delete(g.next, k)
		}
	}
	g.mu.Unlock()

	delay := time.Until(at)
	if delay <= 0 {
		return nil
	}
	if ok, _ := spacedLog.Allow(key); ok {
		diag.Event(diag.CatTunnel, "handshakeSpaced", "sni", key, "waitMs", delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
