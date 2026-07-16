package transport

import "sync/atomic"

// Server-side counters for operator visibility - there's no per-user identity
// or payload here, just aggregate connection outcomes. A sudden spike in
// DecoyHits or NonHTTP relative to HandshakeOK is the earliest signal a server
// has that someone is scanning/probing it (every failed auth attempt lands in
// one of those two), which is exactly what a censor mapping the endpoint looks
// like from the server's side. cmd/server logs a periodic snapshot; nothing
// else consumes these.
var (
	metricHandshakeOK atomic.Int64 // authenticated, handed to the tunnel handler
	metricDecoyHits   atomic.Int64 // parsed as HTTP but failed auth -> decoy site
	metricNonHTTP     atomic.Int64 // not even valid HTTP -> closed
	metricRateLimited atomic.Int64 // over per-IP budget -> decoy without auth (see ratelimit.go)
	metricActiveNow   atomic.Int64 // currently-open authenticated sessions
)

// ServerMetrics is a point-in-time snapshot of the counters above.
type ServerMetrics struct {
	HandshakeOK int64
	DecoyHits   int64
	NonHTTP     int64
	RateLimited int64
	ActiveNow   int64
}

// MetricsSnapshot returns the current counter values. Safe to call concurrently.
func MetricsSnapshot() ServerMetrics {
	return ServerMetrics{
		HandshakeOK: metricHandshakeOK.Load(),
		DecoyHits:   metricDecoyHits.Load(),
		NonHTTP:     metricNonHTTP.Load(),
		RateLimited: metricRateLimited.Load(),
		ActiveNow:   metricActiveNow.Load(),
	}
}
