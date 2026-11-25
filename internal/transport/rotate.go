package transport

import (
	"context"
	"math/rand/v2"
	"time"

	"phantom/internal/diag"
)

// Rotation by age and volume.
//
// A browser's connection to a site lives minutes and carries megabytes; the
// tunnel's used to live as long as the network did - hours, gigabytes, one
// TCP connection. Duration and the ratio of bytes each way are among the
// signs a censor's traffic analysis looks at, and a connection that has been
// up for hours carrying a whole household's traffic stands out. So each
// connection gets an age and a volume - random, so the pool doesn't rotate on
// a beat - and past either one it is retired: new streams go elsewhere (a
// replacement is dialed first if it was the only one), and it is closed once
// its last stream ends, or after drainLimit at the latest.
var (
	rotateAgeMin   = 15 * time.Minute
	rotateAgeMax   = 45 * time.Minute
	rotateBytesMin = int64(200 << 20)
	rotateBytesMax = int64(600 << 20)

	// drainLimit bounds how long a retired connection may keep carrying
	// the streams it already has - a long download or a websocket would
	// otherwise keep it alive indefinitely.
	drainLimit = 10 * time.Minute

	rotateCheck = 15 * time.Second
)

// rotation is the settings above as one pool took them when it was made, so
// its background goroutines never read the package variables (tests change
// them).
type rotation struct {
	ageMin, ageMax     time.Duration
	bytesMin, bytesMax int64
	drainLimit, check  time.Duration
}

func currentRotation() rotation {
	return rotation{rotateAgeMin, rotateAgeMax, rotateBytesMin, rotateBytesMax, drainLimit, rotateCheck}
}

func randBetween(lo, hi int64) int64 { return lo + rand.Int64N(hi-lo+1) }

// limits picks one connection's age and volume - see above.
func (r rotation) limits() (time.Duration, int64) {
	return time.Duration(randBetween(int64(r.ageMin), int64(r.ageMax))),
		randBetween(r.bytesMin, r.bytesMax)
}

// watchAge retires pc once it is past its age or volume.
func (p *ConnPool) watchAge(pc *poolConn) {
	ticker := time.NewTicker(p.rot.check)
	defer ticker.Stop()
	var retryAt time.Time
	for {
		select {
		case <-pc.mux.Done():
			return
		case <-p.life.Done():
			return
		case now := <-ticker.C:
			a := pc.mux.Activity()
			if now.Sub(a.Created) < pc.maxAge && a.BytesRx+a.BytesTx < pc.maxBytes {
				continue
			}
			if now.Before(retryAt) {
				continue
			}
			if p.rotateOut(pc) {
				return
			}
			retryAt = now.Add(growRetry) // the replacement dial failed
		}
	}
}

// rotateOut moves new streams off pc - onto a fresh connection if pc was the
// only one - and closes it once drained. false when the replacement couldn't
// be dialed: pc stays in service and it's tried again later.
func (p *ConnPool) rotateOut(pc *poolConn) bool {
	p.mu.Lock()
	others := 0
	for _, c := range p.conns {
		if c != pc && c.isHealthy() {
			others++
		}
	}
	p.mu.Unlock()

	if others == 0 {
		ctx, cancel := context.WithTimeout(p.life, 15*time.Second)
		fresh, err := p.newConn(ctx)
		cancel()
		if err != nil {
			return false
		}
		p.mu.Lock()
		if p.isClosed() {
			p.mu.Unlock()
			fresh.retire()
			return true
		}
		p.conns = append(p.conns, fresh)
		p.mu.Unlock()
	}

	pc.mu.Lock()
	pc.draining = true
	pc.mu.Unlock()
	a := pc.mux.Activity()
	diag.Event(diag.CatTunnel, "rotate",
		"conn", pc.id,
		"ageS", int(time.Since(a.Created).Seconds()),
		"bytes", a.BytesRx+a.BytesTx,
		"replaced", others == 0)
	go p.drain(pc)
	return true
}

// drain closes pc once its last stream has ended, or after drainLimit.
func (p *ConnPool) drain(pc *poolConn) {
	deadline := time.NewTimer(p.rot.drainLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for pc.mux.NumStreams() > 0 {
		select {
		case <-pc.mux.Done():
			p.forget(pc)
			return
		case <-p.life.Done():
			return
		case <-deadline.C:
			p.forget(pc)
			pc.retire()
			return
		case <-ticker.C:
		}
	}
	p.forget(pc)
	pc.retire()
}

// forget drops pc from the pool's list.
func (p *ConnPool) forget(pc *poolConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, c := range p.conns {
		if c == pc {
			p.conns = append(p.conns[:i], p.conns[i+1:]...)
			return
		}
	}
}
