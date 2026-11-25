package transport

import (
	"context"
	"testing"
	"time"
)

func fastRotation(t *testing.T) {
	oldMin, oldMax, oldCheck, oldDrain := rotateAgeMin, rotateAgeMax, rotateCheck, drainLimit
	rotateAgeMin, rotateAgeMax, rotateCheck, drainLimit = 50*time.Millisecond, 60*time.Millisecond, 10*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { rotateAgeMin, rotateAgeMax, rotateCheck, drainLimit = oldMin, oldMax, oldCheck, oldDrain })
}

// An old connection is replaced and closed, and the pool keeps one working.
func TestAgedConnectionIsReplaced(t *testing.T) {
	fastRotation(t)
	dial, dials := pipeDialer(t)
	pool := NewConnPool(4, dial)
	defer pool.Close()
	first, err := pool.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first connection to close", first.IsClosed)
	if dials.Load() < 2 {
		t.Fatalf("%d dials, want a replacement", dials.Load())
	}
	m := pool.Pick()
	if m == nil || m == first || m.IsClosed() {
		t.Fatal("no fresh connection to pick")
	}
}

// A draining connection gets no new streams but keeps the ones it has until
// they end or drainLimit passes.
func TestDrainingConnectionKeepsItsStreams(t *testing.T) {
	fastRotation(t)
	dial, _ := pipeDialer(t)
	pool := NewConnPool(4, dial)
	defer pool.Close()
	first, err := pool.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Open("example.org:443"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a replacement", func() bool { m := pool.Pick(); return m != nil && m != first })
	if first.IsClosed() {
		t.Fatal("closed while a stream was still on it")
	}
	waitFor(t, "drainLimit to close it", first.IsClosed)
}

func TestRotationLimitsWithinBounds(t *testing.T) {
	for i := 0; i < 1000; i++ {
		age, bytes := currentRotation().limits()
		if age < rotateAgeMin || age > rotateAgeMax || bytes < rotateBytesMin || bytes > rotateBytesMax {
			t.Fatalf("%s %d out of bounds", age, bytes)
		}
	}
}
